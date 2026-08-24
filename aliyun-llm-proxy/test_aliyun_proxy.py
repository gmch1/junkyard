import json
import logging
import threading
import unittest
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import aliyun_proxy


def make_config(upstream_url, port=0):
    return {
        "version": 1,
        "host": "127.0.0.1",
        "port": port,
        "upstream_base_url": upstream_url,
        "model_alias": "aliyun-translate-auto",
        "request_timeout_seconds": 5,
        "route_wait_seconds": 0.1,
        "rpm_safety_ratio": 1,
        "default_cooldown_seconds": 60,
        "models": [
            {"id": "model-a", "enabled": True, "rpm": 600, "tpm": 1000000},
            {"id": "model-b", "enabled": True, "rpm": 600, "tpm": 1000000},
            {"id": "model-c", "enabled": True, "rpm": 600, "tpm": 1000000},
        ],
    }


class FakeUpstream:
    def __init__(self, responder):
        self.calls = []
        self.bodies = []
        calls = self.calls
        bodies = self.bodies

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(length).decode())
                calls.append(body["model"])
                bodies.append(body)
                status, payload, headers = responder(body)
                encoded = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                for key, value in headers.items():
                    self.send_header(key, value)
                self.end_headers()
                self.wfile.write(encoded)

            def log_message(self, _fmt, *_args):
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return "http://127.0.0.1:%s/v1" % self.server.server_address[1]

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class RunningProxy:
    def __init__(self, config):
        self.server = aliyun_proxy.build_server(
            config, "local-key", "upstream-key", unavailable_file=None
        )
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return "http://127.0.0.1:%s" % self.server.server_address[1]

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


def proxy_post(proxy, body):
    request = urllib.request.Request(
        proxy.url + "/v1/chat/completions",
        data=json.dumps(body).encode(),
        method="POST",
        headers={"Authorization": "Bearer local-key", "Content-Type": "application/json"},
    )
    return urllib.request.urlopen(request, timeout=5)


class ModelPoolTests(unittest.TestCase):
    def test_round_robin_reservation(self):
        pool = aliyun_proxy.ModelPool(make_config("http://unused"))
        chosen = []
        for _ in range(3):
            state = pool.acquire(set())
            self.assertIsNotNone(state)
            chosen.append(state.model_id)
            pool.success(state, 1, {})
        self.assertEqual(chosen, ["model-a", "model-b", "model-c"])

    def test_low_frequency_models_are_preferred_then_skipped_during_interval(self):
        config = make_config("http://unused")
        config["models"] = [
            {"id": "general", "enabled": True, "rpm": 600, "routing_priority": 10},
            {
                "id": "mt-a",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "min_interval_seconds": 30,
            },
            {
                "id": "mt-b",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "min_interval_seconds": 30,
            },
        ]
        pool = aliyun_proxy.ModelPool(config)
        chosen = []
        for _ in range(3):
            state = pool.acquire(set())
            self.assertIsNotNone(state)
            chosen.append(state.model_id)
            pool.success(state, 1, {})
        self.assertEqual(chosen, ["mt-a", "mt-b", "general"])

    def test_read_frog_names_map_to_qwen_mt_codes_and_model_capabilities(self):
        self.assertEqual(len(aliyun_proxy.QWEN_MT_CODE_TO_NAME), 92)
        self.assertEqual(len(aliyun_proxy.QWEN_MT_LITE_CODES), 31)
        self.assertEqual(
            aliyun_proxy.qwen_mt_language_code("Simplified Mandarin Chinese"), "zh"
        )
        self.assertEqual(
            aliyun_proxy.qwen_mt_language_code("Traditional Mandarin Chinese"), "zh_tw"
        )
        self.assertEqual(aliyun_proxy.qwen_mt_language_code("Standard Arabic"), "ar")
        self.assertEqual(aliyun_proxy.qwen_mt_language_code("Iranian Persian"), "fa")
        self.assertIsNone(aliyun_proxy.qwen_mt_language_code("Hawaiian"))
        self.assertFalse(aliyun_proxy.qwen_mt_model_supports("qwen-mt-lite", "yue"))
        self.assertTrue(aliyun_proxy.qwen_mt_model_supports("qwen-mt-flash", "yue"))


class ProxyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        logging.disable(logging.CRITICAL)

    @classmethod
    def tearDownClass(cls):
        logging.disable(logging.NOTSET)

    def test_429_switches_to_next_model_and_cools_first(self):
        def respond(body):
            if body["model"] == "model-a":
                return 429, {
                    "error": {
                        "code": "Throttling.RateQuota",
                        "message": "Requests rate limit exceeded, please try again later",
                    }
                }, {"Retry-After": "7"}
            return 200, {
                "id": "ok",
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "译文"}}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 3},
            }, {}

        upstream = FakeUpstream(respond)
        proxy = RunningProxy(make_config(upstream.url))
        try:
            with proxy_post(proxy, {"model": "ignored", "messages": [{"role": "user", "content": "text"}]}) as response:
                payload = json.load(response)
                self.assertEqual(response.status, 200)
                self.assertEqual(response.headers["X-Proxy-Model"], "model-b")
                self.assertEqual(response.headers["X-Proxy-Attempts"], "model-a,model-b")
            self.assertEqual(payload["model"], "model-b")
            self.assertEqual(upstream.calls, ["model-a", "model-b"])
            snapshot = proxy.server.proxy.pool.snapshot()
            self.assertEqual(snapshot[0]["throttles"], 1)
            self.assertGreater(snapshot[0]["cooldown_seconds"], 6)
            self.assertEqual(snapshot[1]["successes"], 1)
        finally:
            proxy.close()
            upstream.close()

    def test_concurrent_requests_are_sent_to_different_models(self):
        barrier = threading.Barrier(2)

        def respond(body):
            barrier.wait(timeout=3)
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "ok"}}],
            }, {}

        upstream = FakeUpstream(respond)
        proxy = RunningProxy(make_config(upstream.url))

        def request_once(index):
            with proxy_post(
                proxy,
                {"model": "ignored", "messages": [{"role": "user", "content": str(index)}]},
            ) as response:
                return json.load(response)["model"]

        try:
            with ThreadPoolExecutor(max_workers=2) as executor:
                selected = list(executor.map(request_once, (1, 2)))
            self.assertEqual(set(selected), {"model-a", "model-b"})
            self.assertEqual(set(upstream.calls), {"model-a", "model-b"})
        finally:
            proxy.close()
            upstream.close()

    def test_low_frequency_429_skips_peer_and_preserves_request_body(self):
        def respond(body):
            if body["model"] == "low-a":
                return 429, {
                    "error": {"code": "Throttling.RateQuota", "message": "limited"}
                }, {}
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "译文"}}],
            }, {}

        config = make_config("placeholder")
        config["models"] = [
            {
                "id": "low-a",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "min_interval_seconds": 30,
                "rate_class": "low-frequency",
            },
            {
                "id": "low-b",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "min_interval_seconds": 30,
                "rate_class": "low-frequency",
            },
            {"id": "general", "enabled": True, "rpm": 600, "routing_priority": 10},
        ]
        upstream = FakeUpstream(respond)
        config["upstream_base_url"] = upstream.url
        proxy = RunningProxy(config)
        try:
            request_body = {
                "model": "caller-can-use-any-name",
                "messages": [
                    {
                        "role": "system",
                        "content": "Return only the translation and preserve paragraph formatting.",
                    },
                    {
                        "role": "user",
                        "content": "Translate to Simplified Mandarin Chinese:\n\nHello",
                    },
                ],
                "temperature": 0.25,
                "max_tokens": 321,
                "top_p": 0.9,
                "stream": False,
            }
            with proxy_post(proxy, request_body) as response:
                self.assertEqual(json.load(response)["model"], "general")
                self.assertEqual(response.headers["X-Proxy-Attempts"], "low-a,general")
            self.assertEqual(upstream.calls, ["low-a", "general"])
            for model, forwarded in zip(("low-a", "general"), upstream.bodies):
                expected = dict(request_body, model=model)
                self.assertEqual(forwarded, expected)
        finally:
            proxy.close()
            upstream.close()

    def test_mt_adapter_is_the_only_request_body_exception(self):
        def respond(body):
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "你好"}}],
            }, {}

        config = make_config("placeholder")
        config["models"] = [
            {
                "id": "qwen-mt-flash",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "min_interval_seconds": 30,
                "adapter": "qwen-mt",
                "rate_class": "low-frequency",
                "default_target_language": "Chinese",
            }
        ]
        upstream = FakeUpstream(respond)
        config["upstream_base_url"] = upstream.url
        proxy = RunningProxy(config)
        try:
            request_body = {
                "model": "anything",
                "messages": [
                    {
                        "role": "system",
                        "content": "Target language: English. Return only the translation.",
                    },
                    {
                        "role": "user",
                        "content": "Translate to Simplified Mandarin Chinese:\n\nHello",
                    },
                ],
                "temperature": 0.5,
                "max_tokens": 123,
                "stream": False,
            }
            with proxy_post(proxy, request_body) as response:
                self.assertEqual(json.load(response)["model"], "qwen-mt-flash")
            self.assertEqual(
                upstream.bodies,
                [
                    {
                        "model": "qwen-mt-flash",
                        "messages": [{"role": "user", "content": "Hello"}],
                        "translation_options": {
                            "source_lang": "auto",
                            "target_lang": "zh",
                        },
                        "stream": False,
                    }
                ],
            )
        finally:
            proxy.close()
            upstream.close()

    def test_mt_lite_is_skipped_when_only_full_mt_models_support_language(self):
        def respond(body):
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "你好"}}],
            }, {}

        config = make_config("placeholder")
        config["models"] = [
            {
                "id": "qwen-mt-lite",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "adapter": "qwen-mt",
            },
            {
                "id": "qwen-mt-plus",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "adapter": "qwen-mt",
            },
            {"id": "general", "enabled": True, "rpm": 600, "routing_priority": 10},
        ]
        upstream = FakeUpstream(respond)
        config["upstream_base_url"] = upstream.url
        proxy = RunningProxy(config)
        try:
            body = {
                "model": "anything",
                "messages": [
                    {"role": "user", "content": "Translate to Cantonese:\n\nHello"}
                ],
            }
            with proxy_post(proxy, body) as response:
                self.assertEqual(json.load(response)["model"], "qwen-mt-plus")
                self.assertEqual(
                    response.headers["X-Proxy-Attempts"], "qwen-mt-lite,qwen-mt-plus"
                )
            self.assertEqual(upstream.calls, ["qwen-mt-plus"])
            self.assertEqual(upstream.bodies[0]["translation_options"]["target_lang"], "yue")
        finally:
            proxy.close()
            upstream.close()

    def test_language_unsupported_by_all_mt_models_goes_directly_to_general(self):
        def respond(body):
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "ok"}}],
            }, {}

        config = make_config("placeholder")
        config["models"] = [
            {
                "id": "qwen-mt-flash",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "adapter": "qwen-mt",
            },
            {"id": "general", "enabled": True, "rpm": 600, "routing_priority": 10},
        ]
        upstream = FakeUpstream(respond)
        config["upstream_base_url"] = upstream.url
        proxy = RunningProxy(config)
        try:
            body = {
                "model": "anything",
                "messages": [
                    {"role": "user", "content": "Translate to Hawaiian:\n\nHello"}
                ],
            }
            with proxy_post(proxy, body) as response:
                self.assertEqual(json.load(response)["model"], "general")
                self.assertEqual(
                    response.headers["X-Proxy-Attempts"], "qwen-mt-flash,general"
                )
            self.assertEqual(upstream.calls, ["general"])
            self.assertEqual(upstream.bodies[0], dict(body, model="general"))
        finally:
            proxy.close()
            upstream.close()

    def test_mt_language_400_falls_back_to_general_model(self):
        def respond(body):
            if body["model"] == "qwen-mt-flash":
                return 400, {
                    "error": {
                        "code": "invalid_parameter_error",
                        "message": "暂时不支持当前设置的语种！",
                    }
                }, {}
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "ok"}}],
            }, {}

        config = make_config("placeholder")
        config["models"] = [
            {
                "id": "qwen-mt-flash",
                "enabled": True,
                "rpm": 60,
                "routing_priority": 0,
                "adapter": "qwen-mt",
            },
            {"id": "general", "enabled": True, "rpm": 600, "routing_priority": 10},
        ]
        upstream = FakeUpstream(respond)
        config["upstream_base_url"] = upstream.url
        proxy = RunningProxy(config)
        try:
            body = {
                "model": "anything",
                "messages": [
                    {
                        "role": "user",
                        "content": "Translate to Simplified Mandarin Chinese:\n\nHello",
                    }
                ],
            }
            with proxy_post(proxy, body) as response:
                self.assertEqual(json.load(response)["model"], "general")
                self.assertEqual(
                    response.headers["X-Proxy-Attempts"], "qwen-mt-flash,general"
                )
            self.assertEqual(upstream.calls, ["qwen-mt-flash", "general"])
            self.assertEqual(upstream.bodies[1], dict(body, model="general"))
        finally:
            proxy.close()
            upstream.close()

    def test_exhausted_quota_persistently_disables_probe_model(self):
        def respond(body):
            if body["model"] == "deepseek-v4-flash":
                return 403, {
                    "error": {
                        "code": "AllocationQuota.FreeTierOnly",
                        "message": "The free tier of the model has been exhausted.",
                    }
                }, {}
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "ok"}}],
            }, {}

        config = make_config("placeholder")
        config["models"] = [
            {
                "id": "deepseek-v4-flash",
                "enabled": True,
                "rpm": 15000,
                "routing_priority": -10,
                "disable_on_allocation_quota": True,
                "disable_on_access_denied": True,
            },
            {"id": "general", "enabled": True, "rpm": 600, "routing_priority": 10},
        ]
        upstream = FakeUpstream(respond)
        config["upstream_base_url"] = upstream.url
        proxy = RunningProxy(config)
        body = {"model": "ignored", "messages": [{"role": "user", "content": "Hello"}]}
        try:
            with proxy_post(proxy, body) as response:
                self.assertEqual(json.load(response)["model"], "general")
                self.assertEqual(response.headers["X-Proxy-Attempts"], "deepseek-v4-flash,general")
            with proxy_post(proxy, body) as response:
                self.assertEqual(json.load(response)["model"], "general")
                self.assertEqual(response.headers["X-Proxy-Attempts"], "general")
            self.assertEqual(upstream.calls, ["deepseek-v4-flash", "general", "general"])
            saved = proxy.server.proxy.unavailable_store.snapshot()
            self.assertEqual(
                saved["deepseek-v4-flash"]["code"], "AllocationQuota.FreeTierOnly"
            )
        finally:
            proxy.close()
            upstream.close()

    def test_401_is_not_retried_on_other_models(self):
        def respond(_body):
            return 401, {"error": {"code": "InvalidApiKey", "message": "bad key"}}, {}

        upstream = FakeUpstream(respond)
        proxy = RunningProxy(make_config(upstream.url))
        try:
            with self.assertRaises(urllib.error.HTTPError) as raised:
                proxy_post(proxy, {"model": "ignored", "messages": [{"role": "user", "content": "text"}]})
            self.assertEqual(raised.exception.code, 401)
            self.assertEqual(upstream.calls, ["model-a"])
        finally:
            proxy.close()
            upstream.close()

    def test_models_exposes_new_and_legacy_aliases(self):
        upstream = FakeUpstream(lambda _body: (200, {}, {}))
        proxy = RunningProxy(make_config(upstream.url))
        try:
            request = urllib.request.Request(
                proxy.url + "/v1/models",
                headers={"Authorization": "Bearer local-key"},
            )
            with urllib.request.urlopen(request, timeout=5) as response:
                ids = [model["id"] for model in json.load(response)["data"]]
            self.assertEqual(ids, ["aliyun-translate-auto", "translategemma-4b-it"])
        finally:
            proxy.close()
            upstream.close()

    def test_dashboard_and_runtime_metrics_are_available_without_auth(self):
        def respond(body):
            return 200, {
                "model": body["model"],
                "choices": [{"message": {"role": "assistant", "content": "ok"}}],
                "usage": {"prompt_tokens": 8, "completion_tokens": 2},
            }, {}

        upstream = FakeUpstream(respond)
        proxy = RunningProxy(make_config(upstream.url))
        try:
            with urllib.request.urlopen(proxy.url + "/v1", timeout=5) as response:
                page = response.read().decode("utf-8")
                self.assertEqual(response.headers.get_content_type(), "text/html")
                self.assertIn('<div id="root"></div>', page)

            with proxy_post(
                proxy,
                {"model": "anything", "messages": [{"role": "user", "content": "hello"}]},
            ) as response:
                self.assertEqual(response.status, 200)

            with urllib.request.urlopen(
                proxy.url + "/v1/proxy/dashboard-data", timeout=5
            ) as response:
                metrics = json.load(response)

            self.assertEqual(metrics["client"]["requests"], 1)
            self.assertEqual(metrics["client"]["successes"], 1)
            self.assertGreater(metrics["client"]["average_latency_ms"], 0)
            self.assertEqual(metrics["totals"]["model_successes"], 1)
            self.assertEqual(metrics["totals"]["input_tokens"], 8)
            self.assertIn("rss_mb", metrics["process"])
            self.assertNotIn("upstream-key", json.dumps(metrics))

            with self.assertRaises(urllib.error.HTTPError) as raised:
                urllib.request.urlopen(
                    proxy.url + "/dashboard-assets/%2e%2e/README.md", timeout=5
                )
            self.assertEqual(raised.exception.code, 404)
        finally:
            proxy.close()
            upstream.close()


if __name__ == "__main__":
    unittest.main()
