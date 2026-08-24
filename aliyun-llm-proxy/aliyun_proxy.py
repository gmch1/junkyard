#!/usr/bin/env python3
"""Manage a loopback-only OpenAI-compatible Aliyun multi-model proxy."""

from __future__ import annotations

import argparse
import copy
import getpass
import hmac
import json
import logging
import mimetypes
import os
import queue
import re
import secrets
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import deque
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Deque, Dict, List, Optional, Set, Tuple


ROOT = Path(__file__).resolve().parent
STATE_DIR = ROOT / ".aliyun-proxy"
CONFIG_FILE = STATE_DIR / "proxy.json"
CLIENT_KEY_FILE = STATE_DIR / "client.key"
UPSTREAM_KEY_FILE = STATE_DIR / "dashscope.key"
PID_FILE = STATE_DIR / "proxy.pid"
LOG_FILE = STATE_DIR / "proxy.log"
UNAVAILABLE_FILE = STATE_DIR / "unavailable_models.json"
LEGACY_CLIENT_KEY_FILE = ROOT / ".translategemma" / "server.key"
DASHBOARD_DIST = ROOT / "dashboard" / "dist"

DEFAULT_CONFIG: Dict[str, Any] = {
    "version": 6,
    "host": "127.0.0.1",
    "port": 39281,
    "upstream_base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
    "model_alias": "aliyun-translate-auto",
    "request_timeout_seconds": 120,
    "route_wait_seconds": 2,
    "rpm_safety_ratio": 0.90,
    "default_cooldown_seconds": 60,
    "hedging": {
        "enabled": True,
        "delay_seconds": 5,
        "max_concurrent_backups": 4,
    },
    "models": [
        {
            "id": "deepseek-v4-flash",
            "enabled": True,
            "rpm": 15000,
            "tpm": 1200000,
            "min_interval_seconds": 30,
            "routing_priority": -10,
            "role": "quota-probe",
            "disable_on_allocation_quota": True,
            "disable_on_access_denied": True,
        },
        {
            "id": "qwen3.6-flash-2026-04-16",
            "enabled": True,
            "rpm": 600,
            "tpm": 1000000,
            "routing_priority": 10,
            "role": "fast",
        },
        {
            "id": "qwen3.5-flash-2026-02-23",
            "enabled": True,
            "rpm": 600,
            "tpm": 1000000,
            "routing_priority": 10,
            "role": "fast",
        },
        {
            "id": "qwen3.7-plus-2026-05-26",
            "enabled": True,
            "rpm": 600,
            "tpm": 1000000,
            "routing_priority": 10,
            "role": "quality",
        },
        {
            "id": "qwen3.6-plus-2026-04-02",
            "enabled": True,
            "rpm": 600,
            "tpm": 1000000,
            "routing_priority": 10,
            "role": "quality",
        },
        {
            "id": "qwen3-30b-a3b-instruct-2507",
            "enabled": True,
            "rpm": 600,
            "tpm": 1000000,
            "routing_priority": 10,
            "role": "instruct-fallback",
        },
        {
            "id": "qwen-mt-flash",
            "enabled": True,
            "rpm": 60,
            "tpm": 35000,
            "adapter": "qwen-mt",
            "min_interval_seconds": 30,
            "routing_priority": 0,
            "rate_class": "low-frequency",
            "default_target_language": "Chinese",
            "stream_compatible": True,
            "role": "dedicated-translation",
        },
        {
            "id": "qwen-mt-lite",
            "enabled": True,
            "rpm": 60,
            "tpm": 100000,
            "adapter": "qwen-mt",
            "min_interval_seconds": 30,
            "routing_priority": 0,
            "rate_class": "low-frequency",
            "default_target_language": "Chinese",
            "stream_compatible": True,
            "role": "dedicated-translation",
        },
        {
            "id": "qwen-mt-plus",
            "enabled": True,
            "rpm": 60,
            "tpm": 25000,
            "adapter": "qwen-mt",
            "min_interval_seconds": 30,
            "routing_priority": 0,
            "rate_class": "low-frequency",
            "default_target_language": "Chinese",
            "stream_compatible": False,
            "role": "dedicated-translation-quality",
        },
        {
            "id": "qwen-mt-turbo",
            "enabled": True,
            "rpm": 60,
            "tpm": 35000,
            "adapter": "qwen-mt",
            "min_interval_seconds": 30,
            "routing_priority": 0,
            "rate_class": "low-frequency",
            "default_target_language": "Chinese",
            "stream_compatible": False,
            "role": "dedicated-translation",
        },
        {
            "id": "qwen-flash-2025-07-28",
            "enabled": True,
            "rpm": 60,
            "tpm": 1000000,
            "min_interval_seconds": 30,
            "routing_priority": 0,
            "rate_class": "low-frequency",
            "role": "low-frequency-fast",
        },
        {
            "id": "qwen-plus-2025-09-11",
            "enabled": True,
            "rpm": 60,
            "tpm": 1000000,
            "min_interval_seconds": 30,
            "routing_priority": 0,
            "rate_class": "low-frequency",
            "role": "low-frequency-quality",
        },
        {
            "id": "qwen-plus-2025-07-28",
            "enabled": True,
            "rpm": 60,
            "tpm": 1000000,
            "min_interval_seconds": 30,
            "routing_priority": 0,
            "rate_class": "low-frequency",
            "role": "low-frequency-quality",
        },
    ],
}

RETRYABLE_HTTP_STATUSES = {429, 500, 502, 503, 504}
MODEL_UNAVAILABLE_CODES = {
    "ModelNotFound",
    "Model.AccessDenied",
    "model_not_found",
}
THROTTLE_CODES = {
    "Throttling",
    "Throttling.RateQuota",
    "Throttling.AllocationQuota",
    "Throttling.BurstRate",
    "Throttling.Concurrency",
    "LimitRequests",
    "limit_requests",
    "limit_burst_rate",
    "ResourceExhausted",
    "insufficient_quota",
}

# Official Qwen-MT language table (Aliyun Model Studio, checked 2026-08-24).
# Codes are used upstream because they are less ambiguous than display names.
QWEN_MT_CODE_TO_NAME = {
    "en": "English",
    "zh": "Chinese",
    "zh_tw": "Traditional Chinese",
    "ru": "Russian",
    "ja": "Japanese",
    "ko": "Korean",
    "es": "Spanish",
    "fr": "French",
    "pt": "Portuguese",
    "de": "German",
    "it": "Italian",
    "th": "Thai",
    "vi": "Vietnamese",
    "id": "Indonesian",
    "ms": "Malay",
    "ar": "Arabic",
    "hi": "Hindi",
    "he": "Hebrew",
    "my": "Burmese",
    "ta": "Tamil",
    "ur": "Urdu",
    "bn": "Bengali",
    "pl": "Polish",
    "nl": "Dutch",
    "ro": "Romanian",
    "tr": "Turkish",
    "km": "Khmer",
    "lo": "Lao",
    "yue": "Cantonese",
    "cs": "Czech",
    "el": "Greek",
    "sv": "Swedish",
    "hu": "Hungarian",
    "da": "Danish",
    "fi": "Finnish",
    "uk": "Ukrainian",
    "bg": "Bulgarian",
    "sr": "Serbian",
    "te": "Telugu",
    "af": "Afrikaans",
    "hy": "Armenian",
    "as": "Assamese",
    "ast": "Asturian",
    "eu": "Basque",
    "be": "Belarusian",
    "bs": "Bosnian",
    "ca": "Catalan",
    "ceb": "Cebuano",
    "hr": "Croatian",
    "arz": "Egyptian Arabic",
    "et": "Estonian",
    "gl": "Galician",
    "ka": "Georgian",
    "gu": "Gujarati",
    "is": "Icelandic",
    "jv": "Javanese",
    "kn": "Kannada",
    "kk": "Kazakh",
    "lv": "Latvian",
    "lt": "Lithuanian",
    "lb": "Luxembourgish",
    "mk": "Macedonian",
    "mai": "Maithili",
    "mt": "Maltese",
    "mr": "Marathi",
    "acm": "Mesopotamian Arabic",
    "ary": "Moroccan Arabic",
    "ars": "Najdi Arabic",
    "ne": "Nepali",
    "az": "North Azerbaijani",
    "apc": "North Levantine Arabic",
    "uz": "Northern Uzbek",
    "nb": "Norwegian Bokmål",
    "nn": "Norwegian Nynorsk",
    "oc": "Occitan",
    "or": "Odia",
    "pag": "Pangasinan",
    "scn": "Sicilian",
    "sd": "Sindhi",
    "si": "Sinhala",
    "sk": "Slovak",
    "sl": "Slovenian",
    "ajp": "South Levantine Arabic",
    "sw": "Swahili",
    "tl": "Tagalog",
    "acq": "Ta’izzi-Adeni Arabic",
    "sq": "Tosk Albanian",
    "aeb": "Tunisian Arabic",
    "vec": "Venetian",
    "war": "Waray",
    "cy": "Welsh",
    "fa": "Western Persian",
}
QWEN_MT_NAME_TO_CODE = {
    re.sub(r"\s+", " ", name).strip().casefold(): code
    for code, name in QWEN_MT_CODE_TO_NAME.items()
}
QWEN_MT_LITE_CODES = frozenset(
    {
        "en", "zh", "zh_tw", "ru", "ja", "ko", "es", "fr", "pt", "de", "it",
        "th", "vi", "id", "ms", "ar", "hi", "he", "ur", "bn", "pl", "nl",
        "tr", "km", "cs", "sv", "hu", "da", "fi", "tl", "fa",
    }
)

# Read Frog @read-frog/definitions@0.4.4 names that differ from Aliyun names.
READ_FROG_QWEN_MT_ALIASES = {
    "simplified mandarin chinese": "zh",
    "traditional mandarin chinese": "zh_tw",
    "simplified chinese": "zh",
    "chinese (simplified)": "zh",
    "traditional chinese": "zh_tw",
    "chinese (traditional)": "zh_tw",
    "standard arabic": "ar",
    "javanese (javanese)": "jv",
    "iranian persian": "fa",
    "persian": "fa",
    "swahili (individual language)": "sw",
    "bosnian (cyrillic)": "bs",
    "serbian (cyrillic)": "sr",
    "northern uzbek (cyrillic)": "uz",
    "malay (individual language) (arabic)": "ms",
    "nepali (individual language)": "ne",
    "north azerbaijani (cyrillic)": "az",
    "modern greek (1453-)": "el",
    "albanian": "sq",
    "简体中文": "zh",
    "中文": "zh",
    "繁体中文": "zh_tw",
    "英语": "en",
    "英文": "en",
    "日语": "ja",
    "韩语": "ko",
    "法语": "fr",
    "德语": "de",
    "西班牙语": "es",
}


def qwen_mt_language_code(value: str) -> Optional[str]:
    key = re.sub(r"\s+", " ", value).strip().casefold()
    if not key:
        return None
    aliased = READ_FROG_QWEN_MT_ALIASES.get(key)
    if aliased:
        return aliased
    normalized_code = key.replace("-", "_")
    if normalized_code in QWEN_MT_CODE_TO_NAME:
        return normalized_code
    return QWEN_MT_NAME_TO_CODE.get(key)


def qwen_mt_model_supports(model_id: str, language_code: str) -> bool:
    if language_code not in QWEN_MT_CODE_TO_NAME:
        return False
    if model_id == "qwen-mt-lite":
        return language_code in QWEN_MT_LITE_CODES
    return True


def is_qwen_mt_language_error(
    status: int,
    code: str,
    message: str,
    model_config: Dict[str, Any],
) -> bool:
    if status != 400 or model_config.get("adapter") != "qwen-mt":
        return False
    normalized_code = code.strip().casefold()
    normalized_message = message.strip().casefold()
    if normalized_code not in ("invalid_parameter_error", "invalidparameter"):
        return False
    return (
        "不支持当前设置的语种" in message
        or "unsupported language" in normalized_message
        or ("language" in normalized_message and "not support" in normalized_message)
    )


class QwenMTUnsupportedLanguage(ValueError):
    def __init__(self, raw_target: str, language_code: Optional[str], model_id: str) -> None:
        self.raw_target = raw_target
        self.language_code = language_code
        self.model_id = model_id
        super().__init__(raw_target)


def ensure_state_dir() -> None:
    STATE_DIR.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(str(STATE_DIR), 0o700)


def write_secret(path: Path, value: str) -> None:
    ensure_state_dir()
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(value.strip() + "\n", encoding="utf-8")
    os.chmod(str(temporary), 0o600)
    temporary.replace(path)
    os.chmod(str(path), 0o600)


def read_secret(path: Path) -> str:
    try:
        value = path.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        return ""
    os.chmod(str(path), 0o600)
    return value


def ensure_client_key() -> str:
    ensure_state_dir()
    current = read_secret(CLIENT_KEY_FILE)
    if current:
        return current
    legacy = read_secret(LEGACY_CLIENT_KEY_FILE)
    value = legacy or ("ap-" + secrets.token_urlsafe(32))
    write_secret(CLIENT_KEY_FILE, value)
    return value


def ensure_config() -> Dict[str, Any]:
    ensure_state_dir()
    if not CONFIG_FILE.exists():
        temporary = CONFIG_FILE.with_suffix(".tmp")
        temporary.write_text(
            json.dumps(DEFAULT_CONFIG, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        temporary.replace(CONFIG_FILE)
    try:
        config = json.loads(CONFIG_FILE.read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        raise SystemExit("Invalid proxy config %s: %s" % (CONFIG_FILE, error))
    if int(config.get("version", 1)) < 2:
        for model in config.get("models", []):
            model.setdefault("routing_priority", 10)
        existing_ids = {str(model.get("id", "")) for model in config.get("models", [])}
        for model in DEFAULT_CONFIG["models"]:
            if model["id"].startswith("qwen-mt-") and model["id"] not in existing_ids:
                config["models"].append(copy.deepcopy(model))
        config["version"] = 2
        temporary = CONFIG_FILE.with_suffix(".tmp")
        temporary.write_text(
            json.dumps(config, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        temporary.replace(CONFIG_FILE)
    if int(config.get("version", 1)) < 3:
        for model in config.get("models", []):
            if int(model.get("rpm", 0)) == 60:
                model.setdefault("min_interval_seconds", 30)
                model.setdefault("routing_priority", 0)
                model.setdefault("rate_class", "low-frequency")
        existing_ids = {str(model.get("id", "")) for model in config.get("models", [])}
        for model in DEFAULT_CONFIG["models"]:
            if model.get("rate_class") == "low-frequency" and model["id"] not in existing_ids:
                config["models"].append(copy.deepcopy(model))
        config["version"] = 3
        temporary = CONFIG_FILE.with_suffix(".tmp")
        temporary.write_text(
            json.dumps(config, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        temporary.replace(CONFIG_FILE)
    if int(config.get("version", 1)) < 4:
        existing_ids = {str(model.get("id", "")) for model in config.get("models", [])}
        probe = next(model for model in DEFAULT_CONFIG["models"] if model["id"] == "deepseek-v4-flash")
        if probe["id"] not in existing_ids:
            config["models"].append(copy.deepcopy(probe))
        config["version"] = 4
        temporary = CONFIG_FILE.with_suffix(".tmp")
        temporary.write_text(
            json.dumps(config, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        temporary.replace(CONFIG_FILE)
    if int(config.get("version", 1)) < 5:
        for model in config.get("models", []):
            model.pop("force_no_thinking", None)
        config["version"] = 5
        temporary = CONFIG_FILE.with_suffix(".tmp")
        temporary.write_text(
            json.dumps(config, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        temporary.replace(CONFIG_FILE)
    if int(config.get("version", 1)) < 6:
        config.setdefault("hedging", copy.deepcopy(DEFAULT_CONFIG["hedging"]))
        config["version"] = 6
        temporary = CONFIG_FILE.with_suffix(".tmp")
        temporary.write_text(
            json.dumps(config, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        temporary.replace(CONFIG_FILE)
    validate_config(config)
    return config


def validate_config(config: Dict[str, Any]) -> None:
    required = ("host", "port", "upstream_base_url", "model_alias", "models")
    missing = [key for key in required if key not in config]
    if missing:
        raise SystemExit("Proxy config is missing: %s" % ", ".join(missing))
    if config["host"] != "127.0.0.1":
        raise SystemExit("For safety, proxy host must remain 127.0.0.1")
    if int(config["port"]) == 8080:
        raise SystemExit("Port 8080 is intentionally not supported; choose another port")
    enabled = [model for model in config["models"] if model.get("enabled", True)]
    if not enabled:
        raise SystemExit("At least one proxy model must be enabled")
    ids = [str(model.get("id", "")).strip() for model in enabled]
    if any(not model_id for model_id in ids) or len(ids) != len(set(ids)):
        raise SystemExit("Enabled model IDs must be non-empty and unique")
    hedging = config.get("hedging", {})
    if not isinstance(hedging, dict):
        raise SystemExit("hedging must be an object")
    if float(hedging.get("delay_seconds", 5)) <= 0:
        raise SystemExit("hedging.delay_seconds must be greater than zero")
    if int(hedging.get("max_concurrent_backups", 4)) < 1:
        raise SystemExit("hedging.max_concurrent_backups must be at least one")


class UnavailableStore:
    def __init__(self, path: Optional[Path]) -> None:
        self.path = path
        self.lock = threading.Lock()
        self.data: Dict[str, Dict[str, Any]] = {}
        if path is not None:
            try:
                payload = json.loads(path.read_text(encoding="utf-8"))
                models = payload.get("models", {})
                if isinstance(models, dict):
                    self.data = models
            except (FileNotFoundError, OSError, ValueError):
                self.data = {}

    def snapshot(self) -> Dict[str, Dict[str, Any]]:
        with self.lock:
            return copy.deepcopy(self.data)

    def _save(self) -> None:
        if self.path is None:
            return
        ensure_state_dir()
        temporary = self.path.with_suffix(".tmp")
        temporary.write_text(
            json.dumps({"version": 1, "models": self.data}, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        temporary.replace(self.path)

    def mark(self, model_id: str, status: int, code: str, message: str) -> None:
        with self.lock:
            self.data[model_id] = {
                "disabled_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
                "http_status": status,
                "code": code,
                "message": message[:500],
            }
            self._save()

    def clear(self, model_id: Optional[str] = None) -> int:
        with self.lock:
            if model_id is None:
                count = len(self.data)
                self.data.clear()
            else:
                count = 1 if self.data.pop(model_id, None) is not None else 0
            self._save()
            return count


def read_pid() -> Optional[int]:
    try:
        return int(PID_FILE.read_text(encoding="utf-8").strip())
    except (FileNotFoundError, ValueError):
        return None


def is_proxy_process(pid: Optional[int]) -> bool:
    if pid is None:
        return False
    try:
        os.kill(pid, 0)
    except (ProcessLookupError, PermissionError):
        return False
    result = subprocess.run(
        ["ps", "-p", str(pid), "-o", "command="],
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        encoding="utf-8",
    )
    return result.returncode == 0 and str(Path(__file__).resolve()) in result.stdout and "serve" in result.stdout


def tail_log(lines: int = 100) -> str:
    try:
        with LOG_FILE.open(encoding="utf-8", errors="replace") as log_file:
            return "".join(deque(log_file, maxlen=lines))
    except FileNotFoundError:
        return ""


@dataclass
class ModelState:
    config: Dict[str, Any]
    in_flight: int = 0
    successes: int = 0
    failures: int = 0
    throttles: int = 0
    total_latency_ms: float = 0.0
    input_tokens: int = 0
    output_tokens: int = 0
    adoptions: int = 0
    hedge_participations: int = 0
    hedge_wins: int = 0
    discarded_responses: int = 0
    cooldown_until: float = 0.0
    cooldown_reason: str = ""
    unavailable: bool = False
    unavailable_reason: str = ""
    request_times: Deque[float] = field(default_factory=deque)
    second_times: Deque[float] = field(default_factory=deque)
    latencies_ms: Deque[float] = field(default_factory=lambda: deque(maxlen=200))

    @property
    def model_id(self) -> str:
        return str(self.config["id"])


class ModelPool:
    def __init__(self, config: Dict[str, Any], unavailable: Optional[Dict[str, Dict[str, Any]]] = None) -> None:
        enabled = [copy.deepcopy(model) for model in config["models"] if model.get("enabled", True)]
        self.states = [ModelState(model) for model in enabled]
        for state in self.states:
            saved = (unavailable or {}).get(state.model_id)
            if saved:
                state.unavailable = True
                state.unavailable_reason = str(saved.get("code") or saved.get("message") or "unavailable")
        self.lock = threading.Lock()
        self.cursor = 0
        self.safety_ratio = float(config.get("rpm_safety_ratio", 0.90))
        self.route_wait_seconds = float(config.get("route_wait_seconds", 2))

    @staticmethod
    def _purge(state: ModelState, now: float) -> None:
        while state.request_times and state.request_times[0] <= now - 60:
            state.request_times.popleft()
        while state.second_times and state.second_times[0] <= now - 1:
            state.second_times.popleft()

    def _has_local_capacity(self, state: ModelState, now: float) -> bool:
        self._purge(state, now)
        minimum_interval = float(state.config.get("min_interval_seconds", 0))
        if minimum_interval > 0 and state.request_times:
            if state.request_times[-1] > now - minimum_interval:
                return False
        rpm = max(1, int(state.config.get("rpm", 600) * self.safety_ratio))
        rps = max(1, int((state.config.get("rpm", 600) / 60.0) * self.safety_ratio))
        return len(state.request_times) < rpm and len(state.second_times) < rps

    def acquire(
        self,
        excluded: Set[str],
        require_incremental_stream: bool = False,
        wait_seconds: Optional[float] = None,
    ) -> Optional[ModelState]:
        wait = self.route_wait_seconds if wait_seconds is None else max(0.0, wait_seconds)
        deadline = time.monotonic() + wait
        while True:
            now = time.monotonic()
            with self.lock:
                count = len(self.states)
                available = []
                for offset in range(count):
                    index = (self.cursor + offset) % count
                    state = self.states[index]
                    if state.model_id in excluded or state.unavailable or state.cooldown_until > now:
                        continue
                    if require_incremental_stream and not state.config.get("stream_compatible", True):
                        continue
                    if not self._has_local_capacity(state, now):
                        continue
                    available.append((int(state.config.get("routing_priority", 10)), offset, index, state))
                if available:
                    _priority, _offset, index, state = min(available)
                    state.request_times.append(now)
                    state.second_times.append(now)
                    state.in_flight += 1
                    self.cursor = (index + 1) % count
                    return state
            if now >= deadline:
                return None
            time.sleep(0.025)

    def success(self, state: ModelState, latency_ms: float, usage: Dict[str, Any]) -> None:
        with self.lock:
            state.in_flight = max(0, state.in_flight - 1)
            state.successes += 1
            state.total_latency_ms += latency_ms
            state.latencies_ms.append(latency_ms)
            state.input_tokens += int(usage.get("prompt_tokens", 0) or 0)
            state.output_tokens += int(usage.get("completion_tokens", 0) or 0)
            if state.cooldown_until <= time.monotonic():
                state.cooldown_until = 0
                state.cooldown_reason = ""

    def mark_hedge_participation(self, state: ModelState) -> None:
        with self.lock:
            state.hedge_participations += 1

    def adopt(self, state: ModelState, hedge_winner: bool) -> None:
        with self.lock:
            state.adoptions += 1
            if hedge_winner:
                state.hedge_wins += 1

    def discard(self, state: ModelState) -> None:
        with self.lock:
            state.discarded_responses += 1

    def failure(
        self,
        state: ModelState,
        reason: str,
        cooldown_seconds: float = 0,
        throttled: bool = False,
    ) -> None:
        with self.lock:
            state.in_flight = max(0, state.in_flight - 1)
            state.failures += 1
            if throttled:
                state.throttles += 1
            if cooldown_seconds > 0:
                state.cooldown_until = max(state.cooldown_until, time.monotonic() + cooldown_seconds)
                state.cooldown_reason = reason

    def disable(self, state: ModelState, reason: str) -> None:
        with self.lock:
            state.unavailable = True
            state.unavailable_reason = reason
            state.cooldown_until = 0
            state.cooldown_reason = ""

    def snapshot(self) -> List[Dict[str, Any]]:
        now = time.monotonic()
        output = []
        with self.lock:
            for state in self.states:
                self._purge(state, now)
                average = state.total_latency_ms / state.successes if state.successes else 0
                attempts = state.successes + state.failures
                adoption_rate = state.adoptions / attempts * 100 if attempts else 0
                hedge_win_rate = (
                    state.hedge_wins / state.hedge_participations * 100
                    if state.hedge_participations
                    else 0
                )
                latencies = sorted(state.latencies_ms)
                p50_index = max(0, int(len(latencies) * 0.50 + 0.9999) - 1)
                p95_index = max(0, int(len(latencies) * 0.95 + 0.9999) - 1)
                output.append(
                    {
                        "id": state.model_id,
                        "role": state.config.get("role", ""),
                        "routing_priority": state.config.get("routing_priority", 10),
                        "rpm": state.config.get("rpm", 600),
                        "tpm": state.config.get("tpm", 0),
                        "min_interval_seconds": state.config.get("min_interval_seconds", 0),
                        "in_flight": state.in_flight,
                        "requests_last_minute": len(state.request_times),
                        "successes": state.successes,
                        "failures": state.failures,
                        "throttles": state.throttles,
                        "average_latency_ms": round(average, 1),
                        "p50_latency_ms": round(latencies[p50_index], 1) if latencies else 0,
                        "p95_latency_ms": round(latencies[p95_index], 1) if latencies else 0,
                        "last_latency_ms": round(state.latencies_ms[-1], 1) if state.latencies_ms else 0,
                        "input_tokens": state.input_tokens,
                        "output_tokens": state.output_tokens,
                        "attempts": attempts,
                        "adoptions": state.adoptions,
                        "adoption_rate": round(adoption_rate, 1),
                        "hedge_participations": state.hedge_participations,
                        "hedge_wins": state.hedge_wins,
                        "hedge_win_rate": round(hedge_win_rate, 1),
                        "discarded_responses": state.discarded_responses,
                        "cooldown_seconds": round(max(0, state.cooldown_until - now), 1),
                        "cooldown_reason": state.cooldown_reason,
                        "unavailable": state.unavailable,
                        "unavailable_reason": state.unavailable_reason,
                    }
                )
        return output


@dataclass
class UpstreamResponse:
    status: int
    headers: Dict[str, str]
    body: bytes
    stream: Optional[Any] = None
    prefetched: bytes = b""


@dataclass
class AttemptResult:
    state: ModelState
    lane: str
    response: Optional[UpstreamResponse]
    success: bool
    retry: bool
    exclude_low_frequency: bool = False
    exclude_mt: bool = False


@dataclass
class RouteRace:
    results: Any = field(default_factory=queue.Queue)
    lock: Any = field(default_factory=threading.Lock)
    resolved: bool = False


def parse_error(body: bytes) -> Tuple[str, str]:
    try:
        payload = json.loads(body.decode("utf-8", errors="replace"))
    except (ValueError, UnicodeDecodeError):
        return "", body.decode("utf-8", errors="replace")[:500]
    error = payload.get("error", payload)
    if isinstance(error, dict):
        return str(error.get("code", payload.get("code", ""))), str(
            error.get("message", payload.get("message", ""))
        )
    return str(payload.get("code", "")), str(error)


def retry_after_seconds(headers: Dict[str, str], default: float) -> float:
    value = headers.get("retry-after", "").strip()
    try:
        return max(0.1, min(300.0, float(value)))
    except ValueError:
        return default


def classify_retry(
    status: int,
    code: str,
    headers: Dict[str, str],
    default_cooldown: float,
) -> Tuple[bool, float, bool]:
    normalized = code.strip()
    if status == 429 or normalized in THROTTLE_CODES or normalized.startswith("Throttling"):
        if "BurstRate" in normalized or normalized == "limit_burst_rate":
            cooldown = retry_after_seconds(headers, 5)
        elif "Concurrency" in normalized:
            cooldown = retry_after_seconds(headers, 2)
        else:
            cooldown = retry_after_seconds(headers, default_cooldown)
        return True, cooldown, True
    if status in RETRYABLE_HTTP_STATUSES:
        return True, retry_after_seconds(headers, 5), False
    if status in (403, 404) and normalized in MODEL_UNAVAILABLE_CODES:
        return True, retry_after_seconds(headers, 300), False
    return False, 0, False


def should_disable_model(
    status: int,
    code: str,
    message: str,
    model_config: Dict[str, Any],
) -> bool:
    normalized_code = code.lower()
    normalized_message = message.lower()
    strong_quota_markers = (
        "free allocated quota exceeded",
        "free tier of the model has been exhausted",
        "hour allocated quota exceeded",
        "week allocated quota exceeded",
        "month allocated quota exceeded",
        "quota has been exhausted",
    )
    if normalized_code == "allocationquota.freetieronly":
        return True
    if any(marker in normalized_message for marker in strong_quota_markers):
        return True
    if model_config.get("disable_on_allocation_quota", False):
        if "allocationquota" in normalized_code or "insufficient_quota" in normalized_code:
            return True
    if model_config.get("disable_on_access_denied", False) and status == 403:
        return True
    return False


def extract_usage(body: bytes) -> Dict[str, Any]:
    try:
        payload = json.loads(body.decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        return {}
    usage = payload.get("usage", {})
    return usage if isinstance(usage, dict) else {}


class AliyunProxy:
    def __init__(
        self,
        config: Dict[str, Any],
        client_key: str,
        upstream_key: str,
        unavailable_store: Optional[UnavailableStore] = None,
    ) -> None:
        self.config = config
        self.client_key = client_key
        self.upstream_key = upstream_key
        self.unavailable_store = unavailable_store or UnavailableStore(None)
        self.pool = ModelPool(config, self.unavailable_store.snapshot())
        self.hedging_config = config.get("hedging", {})
        self.hedging_enabled = bool(self.hedging_config.get("enabled", True))
        self.hedge_delay_seconds = float(
            self.hedging_config.get("delay_seconds", 5)
        )
        self.hedge_slots = threading.BoundedSemaphore(
            int(self.hedging_config.get("max_concurrent_backups", 4))
        )
        self.started_at = time.time()
        self.log = logging.getLogger("aliyun-proxy")
        self.metrics_lock = threading.Lock()
        self.client_requests = 0
        self.client_successes = 0
        self.client_failures = 0
        self.client_total_latency_ms = 0.0
        self.client_latencies_ms: Deque[float] = deque(maxlen=500)
        self.hedged_requests = 0
        self.process_sample_at = 0.0
        self.process_sample = {"rss_mb": 0.0, "cpu_percent": 0.0}

    @property
    def alias(self) -> str:
        return str(self.config["model_alias"])

    def authorized(self, authorization: str) -> bool:
        expected = "Bearer " + self.client_key
        return hmac.compare_digest(authorization.strip(), expected)

    @staticmethod
    def _message_text(content: Any) -> str:
        if isinstance(content, str):
            return content
        if isinstance(content, list):
            parts = []
            for item in content:
                if isinstance(item, dict) and item.get("type") == "text":
                    parts.append(str(item.get("text", "")))
            return "\n".join(part for part in parts if part)
        return str(content or "")

    @staticmethod
    def _infer_target_language(messages: List[Dict[str, Any]], fallback: str) -> str:
        instruction_messages = [
            message for message in reversed(messages) if message.get("role") == "user"
        ] + [
            message
            for message in messages
            if message.get("role") in ("system", "developer")
        ]
        prompt = "\n".join(
            AliyunProxy._message_text(message.get("content"))
            for message in instruction_messages
        )
        english_patterns = (
            r"(?:translate|translation).*?\b(?:into|to)\s+([A-Za-z][A-Za-z ()_-]{1,40}?)(?:[.,;:\n]|$)",
            r"target\s+language\s*[:：]\s*([A-Za-z][A-Za-z ()_-]{1,40}?)(?:[.,;:\n]|$)",
        )
        chinese_patterns = (
            r"(?:翻译成|翻译为|目标语言\s*[:：])\s*([^，。；;：:\n]{1,30})",
        )
        candidate = ""
        for pattern in english_patterns + chinese_patterns:
            match = re.search(pattern, prompt, flags=re.IGNORECASE | re.DOTALL)
            if match:
                candidate = match.group(1).strip()
                break
        return candidate or fallback

    @staticmethod
    def _mt_source_text(messages: List[Dict[str, Any]]) -> str:
        user_messages = [message for message in messages if message.get("role") == "user"]
        source_text = (
            AliyunProxy._message_text(user_messages[-1].get("content"))
            if user_messages
            else ""
        )
        wrapper_patterns = (
            r"^\s*translate\s+(?:the\s+following\s+(?:text|content)\s+)?(?:into|to)\s+[^:\n：]+\s*[:：]\s*(.+?)\s*$",
            r"^\s*(?:请)?(?:将)?(?:以下)?(?:文本|内容)?\s*翻译(?:成|为)\s*[^:\n：]+\s*[:：]\s*(.+?)\s*$",
        )
        for pattern in wrapper_patterns:
            match = re.match(pattern, source_text, flags=re.IGNORECASE | re.DOTALL)
            if match and match.group(1).strip():
                return match.group(1).strip()
        return source_text

    def _upstream_payload(self, body: Dict[str, Any], state: ModelState) -> Dict[str, Any]:
        if state.config.get("adapter") == "qwen-mt":
            incoming_messages = body.get("messages", [])
            messages = [message for message in incoming_messages if isinstance(message, dict)]
            source_text = self._mt_source_text(messages)
            raw_target = self._infer_target_language(
                messages,
                str(state.config.get("default_target_language", "Chinese")),
            )
            target = qwen_mt_language_code(raw_target)
            if target is None or not qwen_mt_model_supports(state.model_id, target):
                raise QwenMTUnsupportedLanguage(raw_target, target, state.model_id)
            log_target = raw_target.replace("\r", "\\r").replace("\n", "\\n")[:80]
            self.log.info(
                "model=%s adapter=qwen-mt target_lang_raw=%s target_lang=%s source_chars=%s",
                state.model_id,
                json.dumps(log_target, ensure_ascii=False),
                target,
                len(source_text),
            )
            payload: Dict[str, Any] = {
                "model": state.model_id,
                "messages": [{"role": "user", "content": source_text}],
                "translation_options": {"source_lang": "auto", "target_lang": target},
                "stream": bool(body.get("stream", False)),
            }
            if payload["stream"] and isinstance(body.get("stream_options"), dict):
                payload["stream_options"] = copy.deepcopy(body["stream_options"])
            return payload

        payload = copy.deepcopy(body)
        payload["model"] = state.model_id
        return payload

    def upstream_request(self, body: Dict[str, Any], state: ModelState, stream: bool) -> UpstreamResponse:
        payload = self._upstream_payload(body, state)
        encoded = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        base = str(self.config["upstream_base_url"]).rstrip("/")
        request = urllib.request.Request(
            base + "/chat/completions",
            data=encoded,
            method="POST",
            headers={
                "Authorization": "Bearer " + self.upstream_key,
                "Content-Type": "application/json",
                "Accept": "text/event-stream" if stream else "application/json",
                "Accept-Encoding": "identity",
                "User-Agent": "read-frog-aliyun-proxy/1.0",
            },
        )
        try:
            response = urllib.request.urlopen(
                request,
                timeout=float(self.config.get("request_timeout_seconds", 120)),
            )
            headers = {key.lower(): value for key, value in response.headers.items()}
            if stream:
                try:
                    reader = getattr(response, "read1", response.read)
                    first_chunk = reader(8192)
                except OSError:
                    response.close()
                    raise
                return UpstreamResponse(
                    response.status,
                    headers,
                    b"",
                    response,
                    first_chunk,
                )
            with response:
                return UpstreamResponse(response.status, headers, response.read())
        except urllib.error.HTTPError as error:
            headers = {key.lower(): value for key, value in error.headers.items()}
            return UpstreamResponse(error.code, headers, error.read())

    def _perform_attempt(
        self,
        body: Dict[str, Any],
        state: ModelState,
        stream: bool,
        lane: str,
    ) -> AttemptResult:
        started = time.monotonic()
        try:
            response = self.upstream_request(body, state, stream)
        except QwenMTUnsupportedLanguage as error:
            latency = (time.monotonic() - started) * 1000
            self.pool.failure(state, "unsupported_target_language", 0, False)
            safe_target = error.raw_target.replace("\r", "\\r").replace("\n", "\\n")[:80]
            self.log.info(
                "model=%s lane=%s adapter=qwen-mt skipped=true target_lang_raw=%s target_lang=%s reason=unsupported_by_model fallback=true latency_ms=%.1f",
                state.model_id,
                lane,
                json.dumps(safe_target, ensure_ascii=False),
                error.language_code or "unmapped",
                latency,
            )
            return AttemptResult(
                state,
                lane,
                None,
                False,
                True,
                exclude_mt=error.language_code is None,
            )
        except (OSError, urllib.error.URLError) as error:
            latency = (time.monotonic() - started) * 1000
            self.pool.failure(state, type(error).__name__, 5, False)
            self.log.warning(
                "model=%s lane=%s transport_error=%s latency_ms=%.1f fallback=true",
                state.model_id,
                lane,
                error,
                latency,
            )
            return AttemptResult(state, lane, None, False, True)
        except Exception as error:
            latency = (time.monotonic() - started) * 1000
            self.pool.failure(state, type(error).__name__, 5, False)
            self.log.exception(
                "model=%s lane=%s unexpected_error=%s latency_ms=%.1f fallback=true",
                state.model_id,
                lane,
                type(error).__name__,
                latency,
            )
            return AttemptResult(state, lane, None, False, True)

        latency = (time.monotonic() - started) * 1000
        if 200 <= response.status < 300:
            usage = {} if stream else extract_usage(response.body)
            self.pool.success(state, latency, usage)
            self.log.info(
                "model=%s lane=%s status=%s stream=%s response_ms=%.1f prompt_tokens=%s completion_tokens=%s",
                state.model_id,
                lane,
                response.status,
                str(stream).lower(),
                latency,
                usage.get("prompt_tokens", 0),
                usage.get("completion_tokens", 0),
            )
            return AttemptResult(state, lane, response, True, False)

        code, message = parse_error(response.body)
        mt_language_error = is_qwen_mt_language_error(
            response.status,
            code,
            message,
            state.config,
        )
        permanently_unavailable = should_disable_model(
            response.status, code, message, state.config
        )
        if permanently_unavailable:
            retry, cooldown, throttled = True, 0, False
        elif mt_language_error:
            retry, cooldown, throttled = True, 0, False
        else:
            retry, cooldown, throttled = classify_retry(
                response.status,
                code,
                response.headers,
                float(self.config.get("default_cooldown_seconds", 60)),
            )
        self.pool.failure(
            state,
            code or ("HTTP %s" % response.status),
            cooldown,
            throttled,
        )
        if permanently_unavailable:
            reason = code or message or ("HTTP %s" % response.status)
            self.pool.disable(state, reason)
            self.unavailable_store.mark(
                state.model_id,
                response.status,
                code,
                message,
            )
        self.log.warning(
            "model=%s lane=%s status=%s code=%s cooldown=%.1f fallback=%s message=%s",
            state.model_id,
            lane,
            response.status,
            code,
            cooldown,
            str(retry).lower(),
            message[:300].replace("\n", " "),
        )
        return AttemptResult(
            state,
            lane,
            response,
            False,
            retry,
            exclude_low_frequency=(
                throttled and state.config.get("rate_class") == "low-frequency"
            ),
            exclude_mt=mt_language_error,
        )

    def _discard_attempt(self, result: AttemptResult) -> None:
        if result.response is not None and result.response.stream is not None:
            try:
                result.response.stream.close()
            except OSError:
                pass
        self.pool.discard(result.state)
        self.log.info(
            "model=%s lane=%s discarded=true reason=faster_response_selected",
            result.state.model_id,
            result.lane,
        )

    def _publish_attempt(self, race: RouteRace, result: AttemptResult) -> None:
        with race.lock:
            if not race.resolved:
                race.results.put(result)
                return
        if result.success:
            self._discard_attempt(result)

    def _attempt_worker(
        self,
        race: RouteRace,
        body: Dict[str, Any],
        state: ModelState,
        stream: bool,
        lane: str,
        hedge_slot: bool,
    ) -> None:
        try:
            result = self._perform_attempt(body, state, stream, lane)
        finally:
            if hedge_slot:
                self.hedge_slots.release()
        self._publish_attempt(race, result)

    def _resolve_race(self, race: RouteRace) -> List[AttemptResult]:
        queued: List[AttemptResult] = []
        with race.lock:
            race.resolved = True
            while True:
                try:
                    queued.append(race.results.get_nowait())
                except queue.Empty:
                    break
        return queued

    def route(
        self,
        body: Dict[str, Any],
        stream: bool,
    ) -> Tuple[UpstreamResponse, Optional[ModelState], List[str]]:
        race = RouteRace()
        excluded: Set[str] = set()
        attempts: List[str] = []
        last_response: Optional[UpstreamResponse] = None
        active = 0
        hedge_checked = False
        hedge_launched = False
        route_started = time.monotonic()
        hedge_deadline = route_started + self.hedge_delay_seconds

        def start_attempt(lane: str, wait_seconds: Optional[float]) -> bool:
            nonlocal active
            hedge_slot = lane == "hedge"
            if hedge_slot and not self.hedge_slots.acquire(blocking=False):
                return False
            state = self.pool.acquire(
                excluded,
                require_incremental_stream=stream,
                wait_seconds=wait_seconds,
            )
            if state is None:
                if hedge_slot:
                    self.hedge_slots.release()
                return False
            excluded.add(state.model_id)
            attempts.append(state.model_id)
            if hedge_slot:
                self.pool.mark_hedge_participation(state)
            active += 1
            threading.Thread(
                target=self._attempt_worker,
                args=(race, body, state, stream, lane, hedge_slot),
                name="aliyun-proxy-%s-%s" % (lane, state.model_id),
                daemon=True,
            ).start()
            return True

        if not start_attempt("primary", None):
            active = 0

        while active > 0:
            timeout: Optional[float] = None
            if self.hedging_enabled and not hedge_checked:
                timeout = max(0.0, hedge_deadline - time.monotonic())
            try:
                result = race.results.get(timeout=timeout)
            except queue.Empty:
                hedge_checked = True
                if start_attempt("hedge", 0):
                    hedge_launched = True
                    with self.metrics_lock:
                        self.hedged_requests += 1
                    self.log.info(
                        "hedge_started=true delay_seconds=%.1f attempts=%s",
                        self.hedge_delay_seconds,
                        ",".join(attempts),
                    )
                continue

            active = max(0, active - 1)
            if result.exclude_low_frequency:
                excluded.update(
                    candidate.model_id
                    for candidate in self.pool.states
                    if candidate.config.get("rate_class") == "low-frequency"
                )
            if result.exclude_mt:
                excluded.update(
                    candidate.model_id
                    for candidate in self.pool.states
                    if candidate.config.get("adapter") == "qwen-mt"
                )

            if result.success and result.response is not None:
                queued = self._resolve_race(race)
                self.pool.adopt(result.state, result.lane == "hedge")
                for other in queued:
                    if other.success:
                        self._discard_attempt(other)
                self.log.info(
                    "selected_model=%s lane=%s hedged=%s attempts=%s elapsed_ms=%.1f",
                    result.state.model_id,
                    result.lane,
                    str(hedge_launched).lower(),
                    ",".join(attempts),
                    (time.monotonic() - route_started) * 1000,
                )
                return result.response, result.state, attempts

            if result.response is not None:
                last_response = result.response
            if not result.retry:
                queued = self._resolve_race(race)
                for other in queued:
                    if other.success:
                        self._discard_attempt(other)
                assert result.response is not None
                return result.response, None, attempts

            wait_seconds = None if active == 0 else 0
            start_attempt(result.lane, wait_seconds)

        self._resolve_race(race)
        if last_response is not None:
            return last_response, None, attempts
        payload = {
            "error": {
                "code": "proxy_model_pool_exhausted",
                "message": "All translation models are rate-limited, cooling down, or locally saturated.",
                "type": "proxy_error",
            }
        }
        return (
            UpstreamResponse(
                429,
                {"content-type": "application/json"},
                json.dumps(payload).encode(),
            ),
            None,
            attempts,
        )

    def record_client_response(self, status: int, latency_ms: float) -> None:
        with self.metrics_lock:
            self.client_requests += 1
            if 200 <= status < 300:
                self.client_successes += 1
            else:
                self.client_failures += 1
            self.client_total_latency_ms += latency_ms
            self.client_latencies_ms.append(latency_ms)

    def _client_metrics(self) -> Dict[str, Any]:
        with self.metrics_lock:
            latencies = sorted(self.client_latencies_ms)
            p95_index = max(0, int(len(latencies) * 0.95 + 0.9999) - 1)
            average = (
                self.client_total_latency_ms / self.client_requests
                if self.client_requests
                else 0
            )
            return {
                "requests": self.client_requests,
                "successes": self.client_successes,
                "failures": self.client_failures,
                "average_latency_ms": round(average, 1),
                "p95_latency_ms": round(latencies[p95_index], 1) if latencies else 0,
                "last_latency_ms": round(self.client_latencies_ms[-1], 1)
                if self.client_latencies_ms
                else 0,
            }

    def _process_metrics(self) -> Dict[str, float]:
        now = time.monotonic()
        with self.metrics_lock:
            if now - self.process_sample_at < 2.5:
                return dict(self.process_sample)
        try:
            result = subprocess.run(
                ["ps", "-p", str(os.getpid()), "-o", "rss=,%cpu="],
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
                encoding="utf-8",
                timeout=1,
            )
            fields = result.stdout.strip().split()
            sample = {
                "rss_mb": round(int(fields[0]) / 1024.0, 1),
                "cpu_percent": round(float(fields[1]), 1),
            }
        except (OSError, ValueError, IndexError, subprocess.TimeoutExpired):
            with self.metrics_lock:
                return dict(self.process_sample)
        with self.metrics_lock:
            self.process_sample_at = now
            self.process_sample = sample
            return dict(sample)

    def status(self) -> Dict[str, Any]:
        models = self.pool.snapshot()
        client = self._client_metrics()
        total_model_successes = sum(int(model["successes"]) for model in models)
        total_model_failures = sum(int(model["failures"]) for model in models)
        total_attempts = total_model_successes + total_model_failures
        total_adoptions = sum(int(model["adoptions"]) for model in models)
        total_hedge_wins = sum(int(model["hedge_wins"]) for model in models)
        total_discarded = sum(
            int(model["discarded_responses"]) for model in models
        )
        with self.metrics_lock:
            hedged_requests = self.hedged_requests
        return {
            "status": "running",
            "generated_at": time.strftime("%Y-%m-%d %H:%M:%S"),
            "uptime_seconds": round(time.time() - self.started_at, 1),
            "base_url": "http://%s:%s/v1" % (self.config["host"], self.config["port"]),
            "model_alias": self.alias,
            "upstream_base_url": self.config["upstream_base_url"],
            "hedging": {
                "enabled": self.hedging_enabled,
                "delay_seconds": self.hedge_delay_seconds,
                "max_concurrent_backups": int(
                    self.hedging_config.get("max_concurrent_backups", 4)
                ),
            },
            "client": client,
            "process": self._process_metrics(),
            "totals": {
                "model_successes": total_model_successes,
                "model_failures": total_model_failures,
                "upstream_attempts": total_attempts,
                "adoptions": total_adoptions,
                "adoption_rate": round(
                    total_adoptions / total_attempts * 100, 1
                )
                if total_attempts
                else 0,
                "hedged_requests": hedged_requests,
                "hedge_wins": total_hedge_wins,
                "hedge_win_rate": round(
                    total_hedge_wins / hedged_requests * 100, 1
                )
                if hedged_requests
                else 0,
                "discarded_responses": total_discarded,
                "in_flight": sum(int(model["in_flight"]) for model in models),
                "requests_last_minute": sum(
                    int(model["requests_last_minute"]) for model in models
                ),
                "input_tokens": sum(int(model["input_tokens"]) for model in models),
                "output_tokens": sum(int(model["output_tokens"]) for model in models),
                "throttles": sum(int(model["throttles"]) for model in models),
            },
            "models": models,
        }


class ProxyHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address: Tuple[str, int], proxy: AliyunProxy) -> None:
        self.proxy = proxy
        super().__init__(address, ProxyHandler)


class ProxyHandler(BaseHTTPRequestHandler):
    server_version = "ReadFrogAliyunProxy/1.0"
    protocol_version = "HTTP/1.1"

    @property
    def proxy(self) -> AliyunProxy:
        return self.server.proxy  # type: ignore[attr-defined]

    def log_message(self, fmt: str, *args: Any) -> None:
        logging.getLogger("aliyun-proxy.http").info("client=%s " + fmt, self.client_address[0], *args)

    def _cors_headers(self) -> None:
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Headers", "Authorization, Content-Type")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

    def _send_bytes(
        self,
        status: int,
        body: bytes,
        content_type: str = "application/json",
        extra_headers: Optional[Dict[str, str]] = None,
    ) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self._cors_headers()
        for key, value in (extra_headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _send_json(self, status: int, payload: Dict[str, Any]) -> None:
        self._send_bytes(status, json.dumps(payload, ensure_ascii=False).encode("utf-8"))

    def _serve_dashboard(self) -> None:
        index_file = DASHBOARD_DIST / "index.html"
        try:
            body = index_file.read_bytes()
        except OSError:
            self._send_json(
                503,
                {
                    "error": {
                        "code": "dashboard_not_built",
                        "message": "Dashboard is not built. Run: pnpm --dir dashboard run build",
                    }
                },
            )
            return
        self._send_bytes(
            200,
            body,
            "text/html; charset=utf-8",
            {"Cache-Control": "no-store"},
        )

    def _serve_dashboard_asset(self, path: str) -> None:
        relative = urllib.parse.unquote(path[len("/dashboard-assets/") :])
        root = DASHBOARD_DIST.resolve()
        candidate = (root / relative).resolve()
        try:
            candidate.relative_to(root)
        except ValueError:
            self._send_json(
                404,
                {"error": {"code": "not_found", "message": "Asset not found."}},
            )
            return
        if not candidate.is_file():
            self._send_json(
                404,
                {"error": {"code": "not_found", "message": "Asset not found."}},
            )
            return
        content_type = mimetypes.guess_type(str(candidate))[0] or "application/octet-stream"
        self._send_bytes(
            200,
            candidate.read_bytes(),
            content_type,
            {"Cache-Control": "public, max-age=31536000, immutable"},
        )

    def _require_auth(self) -> bool:
        if self.proxy.authorized(self.headers.get("Authorization", "")):
            return True
        self._send_json(
            401,
            {"error": {"code": "invalid_api_key", "message": "Invalid local proxy API key."}},
        )
        return False

    def do_OPTIONS(self) -> None:
        self.send_response(204)
        self.send_header("Content-Length", "0")
        self._cors_headers()
        self.end_headers()

    def do_GET(self) -> None:
        path = self.path.split("?", 1)[0].rstrip("/") or "/"
        if path == "/health":
            self._send_json(200, {"status": "ok"})
            return
        if path in ("/", "/v1", "/dashboard", "/v1/proxy/dashboard"):
            self._serve_dashboard()
            return
        if path.startswith("/dashboard-assets/"):
            self._serve_dashboard_asset(path)
            return
        if path == "/v1/proxy/dashboard-data":
            self._send_json(200, self.proxy.status())
            return
        if not self._require_auth():
            return
        if path == "/v1/models":
            aliases = [self.proxy.alias]
            if self.proxy.alias != "translategemma-4b-it":
                aliases.append("translategemma-4b-it")
            self._send_json(
                200,
                {
                    "object": "list",
                    "data": [
                        {"id": alias, "object": "model", "owned_by": "local-aliyun-proxy"}
                        for alias in aliases
                    ],
                },
            )
            return
        if path in ("/v1/proxy/status", "/status"):
            self._send_json(200, self.proxy.status())
            return
        self._send_json(404, {"error": {"code": "not_found", "message": "Endpoint not found."}})

    def do_POST(self) -> None:
        path = self.path.split("?", 1)[0].rstrip("/")
        if path != "/v1/chat/completions":
            self._send_json(404, {"error": {"code": "not_found", "message": "Endpoint not found."}})
            return
        if not self._require_auth():
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        if length <= 0 or length > 5 * 1024 * 1024:
            self._send_json(400, {"error": {"code": "invalid_request", "message": "Invalid request size."}})
            return
        try:
            body = json.loads(self.rfile.read(length).decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            self._send_json(400, {"error": {"code": "invalid_json", "message": "Request body must be JSON."}})
            return
        if not isinstance(body, dict) or not isinstance(body.get("messages"), list):
            self._send_json(400, {"error": {"code": "invalid_request", "message": "messages must be an array."}})
            return

        request_started = time.monotonic()
        stream = bool(body.get("stream", False))
        response, state, attempts = self.proxy.route(body, stream)
        proxy_headers = {
            "X-Proxy-Attempts": ",".join(attempts),
            "X-Proxy-Model": state.model_id if state else "",
        }
        if stream and state is not None and response.stream is not None:
            self.send_response(response.status)
            self.send_header("Content-Type", response.headers.get("content-type", "text/event-stream"))
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.close_connection = True
            self._cors_headers()
            for key, value in proxy_headers.items():
                self.send_header(key, value)
            self.end_headers()
            disconnected = False
            stream_failed = False
            try:
                with response.stream as upstream:
                    if response.prefetched:
                        self.wfile.write(response.prefetched)
                        self.wfile.flush()
                    reader = getattr(upstream, "read1", upstream.read)
                    while True:
                        chunk = reader(8192)
                        if not chunk:
                            break
                        self.wfile.write(chunk)
                        self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                disconnected = True
            except OSError as error:
                stream_failed = True
                logging.getLogger("aliyun-proxy").warning(
                    "model=%s stream_error=%s fallback=false reason=already_started",
                    state.model_id,
                    error,
                )
            finally:
                latency = (time.monotonic() - request_started) * 1000
                client_status = 502 if stream_failed else 499 if disconnected else response.status
                self.proxy.record_client_response(client_status, latency)
                if disconnected:
                    logging.getLogger("aliyun-proxy").info(
                        "model=%s client_disconnected=true stream=true", state.model_id
                    )
            return

        content_type = response.headers.get("content-type", "application/json").split(";", 1)[0]
        self.proxy.record_client_response(
            response.status,
            (time.monotonic() - request_started) * 1000,
        )
        self._send_bytes(response.status, response.body, content_type, proxy_headers)


def configure_logging() -> None:
    ensure_state_dir()
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
        handlers=[logging.FileHandler(str(LOG_FILE), encoding="utf-8")],
    )


def build_server(
    config: Dict[str, Any],
    client_key: str,
    upstream_key: str,
    unavailable_file: Optional[Path] = UNAVAILABLE_FILE,
) -> ProxyHTTPServer:
    proxy = AliyunProxy(config, client_key, upstream_key, UnavailableStore(unavailable_file))
    return ProxyHTTPServer((str(config["host"]), int(config["port"])), proxy)


def serve() -> int:
    config = ensure_config()
    client_key = ensure_client_key()
    upstream_key = read_secret(UPSTREAM_KEY_FILE)
    if not upstream_key:
        raise SystemExit("Aliyun API key is not configured. Run: python3 aliyun_proxy.py set-upstream-key")
    configure_logging()
    server = build_server(config, client_key, upstream_key)
    logging.getLogger("aliyun-proxy").info(
        "started host=%s port=%s models=%s",
        config["host"],
        config["port"],
        ",".join(state.model_id for state in server.proxy.pool.states),
    )

    stopping = threading.Event()

    def request_stop(signum: int, _frame: Any) -> None:
        if stopping.is_set():
            return
        stopping.set()
        logging.getLogger("aliyun-proxy").info("received_signal=%s stopping=true", signum)
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, request_stop)
    signal.signal(signal.SIGINT, request_stop)
    try:
        server.serve_forever(poll_interval=0.2)
    finally:
        server.server_close()
        logging.getLogger("aliyun-proxy").info("stopped")
    return 0


def local_health(config: Dict[str, Any], timeout: float = 1.0) -> bool:
    request = urllib.request.Request("http://%s:%s/health" % (config["host"], config["port"]))
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status == 200
    except (OSError, urllib.error.URLError):
        return False


def start() -> int:
    config = ensure_config()
    ensure_client_key()
    if not read_secret(UPSTREAM_KEY_FILE):
        sys.stderr.write("Aliyun API key is not configured.\n")
        sys.stderr.write("Run: python3 aliyun_proxy.py set-upstream-key\n")
        return 1
    current = read_pid()
    if is_proxy_process(current):
        print("Aliyun proxy is already running (PID %s)." % current)
        return 0
    PID_FILE.unlink(missing_ok=True)
    ensure_state_dir()
    with LOG_FILE.open("ab") as log_file:
        process = subprocess.Popen(
            [sys.executable, str(Path(__file__).resolve()), "serve"],
            cwd=str(ROOT),
            stdin=subprocess.DEVNULL,
            stdout=log_file,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    PID_FILE.write_text("%s\n" % process.pid, encoding="utf-8")
    for _ in range(100):
        if process.poll() is not None:
            PID_FILE.unlink(missing_ok=True)
            sys.stderr.write("Aliyun proxy exited during startup.\n")
            sys.stderr.write(tail_log(30))
            return process.returncode or 1
        if local_health(config):
            print("Aliyun proxy started (PID %s)." % process.pid)
            print("Base URL: http://%s:%s/v1" % (config["host"], config["port"]))
            print("Model: %s" % config["model_alias"])
            return 0
        time.sleep(0.05)
    stop_process(process.pid)
    PID_FILE.unlink(missing_ok=True)
    sys.stderr.write("Timed out waiting for Aliyun proxy startup.\n")
    return 1


def stop_process(pid: int) -> bool:
    if not is_proxy_process(pid):
        return True
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        return True
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if not is_proxy_process(pid):
            return True
        time.sleep(0.1)
    if is_proxy_process(pid):
        os.kill(pid, signal.SIGKILL)
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            if not is_proxy_process(pid):
                return True
            time.sleep(0.05)
    return not is_proxy_process(pid)


def stop() -> int:
    pid = read_pid()
    if not is_proxy_process(pid):
        PID_FILE.unlink(missing_ok=True)
        print("Aliyun proxy is not running.")
        return 0
    assert pid is not None
    if not stop_process(pid):
        sys.stderr.write("Proxy PID %s could not be terminated.\n" % pid)
        return 1
    PID_FILE.unlink(missing_ok=True)
    print("Aliyun proxy stopped.")
    return 0


def fetch_status(config: Dict[str, Any], client_key: str) -> Optional[Dict[str, Any]]:
    request = urllib.request.Request(
        "http://%s:%s/v1/proxy/status" % (config["host"], config["port"]),
        headers={"Authorization": "Bearer " + client_key},
    )
    try:
        with urllib.request.urlopen(request, timeout=2) as response:
            return json.load(response)
    except (OSError, ValueError, urllib.error.URLError):
        return None


def status() -> int:
    config = ensure_config()
    client_key = ensure_client_key()
    pid = read_pid()
    running = is_proxy_process(pid)
    payload = fetch_status(config, client_key) if running else None
    print("Status: %s" % ("running" if payload else "starting/unhealthy" if running else "stopped"))
    if running:
        print("PID: %s" % pid)
    print("Base URL: http://%s:%s/v1" % (config["host"], config["port"]))
    print("Model: %s" % config["model_alias"])
    print("Config: %s" % CONFIG_FILE)
    print("Log: %s" % LOG_FILE)
    if payload:
        hedging = payload["hedging"]
        totals = payload["totals"]
        print(
            "Hedging: enabled={enabled}, delay={delay_seconds}s, max_backups={max_concurrent_backups}".format(
                **hedging
            )
        )
        print(
            "Race: hedged={hedged_requests}, hedge_wins={hedge_wins}, discarded={discarded_responses}, adoptions={adoptions}".format(
                **totals
            )
        )
        for model in payload["models"]:
            print(
                "- {id}: in_flight={in_flight}, success={successes}, adopted={adoptions}, "
                "adoption_rate={adoption_rate}%, hedge_wins={hedge_wins}, discarded={discarded_responses}, "
                "throttles={throttles}, cooldown={cooldown_seconds}s, unavailable={unavailable}, "
                "avg={average_latency_ms}ms".format(
                    **model
                )
            )
    return 0 if payload else 1


def set_upstream_key(from_env: bool) -> int:
    if from_env:
        value = os.environ.get("DASHSCOPE_API_KEY", "").strip()
        if not value:
            sys.stderr.write("DASHSCOPE_API_KEY is empty.\n")
            return 1
    else:
        value = getpass.getpass("Aliyun DashScope API Key: ").strip()
    if not value:
        sys.stderr.write("API key cannot be empty.\n")
        return 1
    write_secret(UPSTREAM_KEY_FILE, value)
    print("Aliyun API key saved to %s (mode 0600)." % UPSTREAM_KEY_FILE)
    return 0


def show_logs() -> int:
    output = tail_log(100)
    print(output, end="" if output else "\n")
    return 0


def show_unavailable() -> int:
    models = UnavailableStore(UNAVAILABLE_FILE).snapshot()
    if not models:
        print("No models are persistently unavailable.")
        return 0
    for model_id, details in models.items():
        print(
            "%s: status=%s code=%s disabled_at=%s"
            % (
                model_id,
                details.get("http_status", ""),
                details.get("code", ""),
                details.get("disabled_at", ""),
            )
        )
    return 0


def reset_unavailable(model_id: Optional[str]) -> int:
    count = UnavailableStore(UNAVAILABLE_FILE).clear(model_id)
    print("Cleared %s unavailable model record(s)." % count)
    if is_proxy_process(read_pid()):
        print("Restart the proxy for this change to take effect.")
    return 0


def probe() -> int:
    config = ensure_config()
    payload = {
        "model": config["model_alias"],
        "messages": [
            {
                "role": "system",
                "content": "You are a professional Simplified Mandarin Chinese translator. Output only the translation.",
            },
            {
                "role": "user",
                "content": "Translate to Simplified Mandarin Chinese:\n\nHello.",
            },
        ],
        "temperature": 0,
        "max_tokens": 32,
        "stream": False,
    }
    request = urllib.request.Request(
        "http://%s:%s/v1/chat/completions" % (config["host"], config["port"]),
        data=json.dumps(payload).encode("utf-8"),
        method="POST",
        headers={
            "Authorization": "Bearer " + ensure_client_key(),
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=130) as response:
            body = json.load(response)
            print("HTTP: %s" % response.status)
            print("Attempts: %s" % response.headers.get("X-Proxy-Attempts", ""))
            print("Selected model: %s" % response.headers.get("X-Proxy-Model", ""))
            choices = body.get("choices", [])
            if choices:
                print("Output: %s" % choices[0].get("message", {}).get("content", ""))
            return 0
    except urllib.error.HTTPError as error:
        print("HTTP: %s" % error.code)
        print(error.read().decode("utf-8", errors="replace")[:1000])
        return 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command in (
        "start",
        "stop",
        "restart",
        "status",
        "key",
        "logs",
        "config",
        "serve",
        "unavailable",
        "probe",
    ):
        subparsers.add_parser(command)
    key_parser = subparsers.add_parser("set-upstream-key")
    key_parser.add_argument("--from-env", action="store_true")
    reset_parser = subparsers.add_parser("reset-unavailable")
    reset_parser.add_argument("model", nargs="?")
    args = parser.parse_args()

    if args.command == "serve":
        return serve()
    if args.command == "start":
        return start()
    if args.command == "stop":
        return stop()
    if args.command == "restart":
        stopped = stop()
        return stopped if stopped else start()
    if args.command == "status":
        return status()
    if args.command == "set-upstream-key":
        return set_upstream_key(args.from_env)
    if args.command == "unavailable":
        return show_unavailable()
    if args.command == "reset-unavailable":
        return reset_unavailable(args.model)
    if args.command == "probe":
        return probe()
    if args.command == "key":
        print(ensure_client_key())
        return 0
    if args.command == "config":
        ensure_config()
        print(CONFIG_FILE)
        return 0
    return show_logs()


if __name__ == "__main__":
    raise SystemExit(main())
