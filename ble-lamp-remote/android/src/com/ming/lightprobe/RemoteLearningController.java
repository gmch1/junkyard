package com.ming.lightprobe;

import android.Manifest;
import android.bluetooth.BluetoothAdapter;
import android.bluetooth.BluetoothManager;
import android.bluetooth.le.ScanCallback;
import android.bluetooth.le.ScanRecord;
import android.bluetooth.le.ScanResult;
import android.bluetooth.le.ScanSettings;
import android.content.Context;
import android.content.pm.PackageManager;
import android.os.Build;
import android.os.Handler;
import android.os.Looper;
import android.os.ParcelUuid;
import android.os.SystemClock;
import android.util.Log;
import android.util.SparseArray;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Automatic remote-learning state machine.
 *
 * One physical press is a BLE burst containing many duplicate advertisements.
 * A press is finalized only after a quiet gap; three packets from one burst never
 * count as three presses.
 */
public final class RemoteLearningController {
    private static final String TAG = "REMOTE_LEARNING";
    private static final long BASELINE_MS = 1800L;
    private static final long BASELINE_CONTINUOUS_MS = 1200L;
    private static final long BURST_QUIET_MS = 550L;
    private static final long STEP_TRANSITION_MS = 650L;
    private static final int MIN_BURST_OBSERVATIONS = 3;
    private static final int MAX_UNIQUE_PAYLOADS = 12;

    public static final Step[] STEPS = new Step[] {
            new Step("on", "开启", "请按遥控器的开启键", LightCommandDispatcher.ACTION_ON, false),
            new Step("off", "关闭", "请按遥控器的关闭键", LightCommandDispatcher.ACTION_OFF, false),
            new Step("brightness_up", "亮度增加", "请按亮度增加键", LightCommandDispatcher.ACTION_BRIGHTER, false),
            new Step("brightness_down", "亮度降低", "请按亮度降低键", LightCommandDispatcher.ACTION_DIMMER, false),
            new Step("temperature_warmer", "色温偏暖", "请按色温偏暖键", LightCommandDispatcher.ACTION_WARMER, false),
            new Step("temperature_cooler", "色温偏冷", "请按色温偏冷键", LightCommandDispatcher.ACTION_COOLER, false),
            new Step("preset_toggle", "全亮 / 半亮", "请按全亮 / 半亮键", null, true)
    };

    public interface Listener {
        void onCalibrating(int secondsRemaining);
        void onStepChanged(int index, int total, Step step, int acceptedPresses,
                String message);
        void onCompleted(Result result);
        void onError(String message);
    }

    public static final class Step {
        public final String key;
        public final String title;
        public final String instruction;
        final String expectedAction;
        final boolean alternatingPreset;

        Step(String key, String title, String instruction,
                String expectedAction, boolean alternatingPreset) {
            this.key = key;
            this.title = title;
            this.instruction = instruction;
            this.expectedAction = expectedAction;
            this.alternatingPreset = alternatingPreset;
        }
    }

    public static final class Result {
        public final JSONObject recordings;
        public final FanLampRemoteProtocol.Profile fanLampProfile;
        public final String protocolName;

        Result(JSONObject recordings, FanLampRemoteProtocol.Profile fanLampProfile,
                String protocolName) {
            this.recordings = recordings;
            this.fanLampProfile = fanLampProfile;
            this.protocolName = protocolName;
        }

        public boolean isUsable() {
            return fanLampProfile != null;
        }
    }

    private final Context context;
    private final Listener listener;
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final Map<String, BaselineSource> baselineSources = new HashMap<>();
    private final Map<String, BurstBuilder> candidates = new HashMap<>();
    private final JSONObject recordings = new JSONObject();
    private final ScanCallback scanCallback = new ScanCallback() {
        @Override
        public void onScanResult(int callbackType, ScanResult result) {
            consume(result);
        }

        @Override
        public void onBatchScanResults(List<ScanResult> results) {
            for (ScanResult result : results) {
                consume(result);
            }
        }

        @Override
        public void onScanFailed(int errorCode) {
            handler.post(new Runnable() {
                @Override
                public void run() {
                    listener.onError("蓝牙扫描启动失败（" + errorCode + "）");
                }
            });
        }
    };

    private android.bluetooth.le.BluetoothLeScanner scanner;
    private long baselineEndsAt;
    private long burstGeneration;
    private boolean running;
    private boolean acceptingPresses;
    private String lockedSource;
    private int stepIndex;
    private int acceptedPresses;
    private String stepReferenceSignature;
    private JSONArray stepBursts = new JSONArray();
    private FanLampRemoteProtocol.Profile learnedFanLampProfile;

    public RemoteLearningController(Context context, Listener listener) {
        this.context = context.getApplicationContext();
        this.listener = listener;
    }

    public void start() {
        if (running) {
            return;
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S
                && context.checkSelfPermission(Manifest.permission.BLUETOOTH_SCAN)
                != PackageManager.PERMISSION_GRANTED) {
            listener.onError("缺少附近设备扫描权限");
            return;
        }
        BluetoothManager manager = (BluetoothManager)
                context.getSystemService(Context.BLUETOOTH_SERVICE);
        BluetoothAdapter adapter = manager == null ? null : manager.getAdapter();
        scanner = adapter == null ? null : adapter.getBluetoothLeScanner();
        if (adapter == null || !adapter.isEnabled() || scanner == null) {
            listener.onError("蓝牙不可用，请先开启蓝牙");
            return;
        }

        resetSession();
        running = true;
        baselineEndsAt = SystemClock.elapsedRealtime() + BASELINE_MS;
        ScanSettings settings = new ScanSettings.Builder()
                .setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY)
                .setCallbackType(ScanSettings.CALLBACK_TYPE_ALL_MATCHES)
                .setReportDelay(0)
                .build();
        scanner.startScan(null, settings, scanCallback);
        listener.onCalibrating(2);
        handler.postDelayed(new Runnable() {
            @Override
            public void run() {
                if (!running) return;
                listener.onCalibrating(1);
            }
        }, 900L);
        handler.postDelayed(new Runnable() {
            @Override
            public void run() {
                if (!running) return;
                acceptingPresses = true;
                announceCurrentStep("等待第 1 次独立按压");
            }
        }, BASELINE_MS);
    }

    public void stop() {
        running = false;
        acceptingPresses = false;
        handler.removeCallbacksAndMessages(null);
        if (scanner != null) {
            try {
                if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S
                        || context.checkSelfPermission(Manifest.permission.BLUETOOTH_SCAN)
                        == PackageManager.PERMISSION_GRANTED) {
                    scanner.stopScan(scanCallback);
                }
            } catch (IllegalStateException | SecurityException ignored) {
                // Bluetooth may have been turned off while recording.
            }
        }
        scanner = null;
    }

    public void skipCurrentStep() {
        if (!running || !acceptingPresses || stepIndex >= STEPS.length) {
            return;
        }
        try {
            JSONObject skipped = new JSONObject();
            skipped.put("skipped", true);
            recordings.put(STEPS[stepIndex].key, skipped);
        } catch (JSONException impossible) {
            listener.onError("无法保存录制状态");
            return;
        }
        advanceStep();
    }

    private void resetSession() {
        baselineSources.clear();
        candidates.clear();
        lockedSource = null;
        stepIndex = 0;
        acceptedPresses = 0;
        stepReferenceSignature = null;
        stepBursts = new JSONArray();
        learnedFanLampProfile = null;
        burstGeneration = 0;
        acceptingPresses = false;
    }

    private void consume(ScanResult result) {
        if (!running || result == null || result.getScanRecord() == null) {
            return;
        }
        List<Observation> observations = observations(result);
        long now = SystemClock.elapsedRealtime();
        if (now < baselineEndsAt) {
            for (Observation observation : observations) {
                BaselineSource source = baselineSources.get(observation.sourceKey);
                if (source == null) {
                    source = new BaselineSource(now);
                    baselineSources.put(observation.sourceKey, source);
                }
                source.observe(now);
            }
            return;
        }
        if (!acceptingPresses) {
            return;
        }
        boolean acceptedObservation = false;
        for (Observation observation : observations) {
            if (lockedSource != null && !lockedSource.equals(observation.sourceKey)) {
                continue;
            }
            if (lockedSource == null
                    && baselineSources.containsKey(observation.sourceKey)
                    && baselineSources.get(observation.sourceKey).isContinuous()) {
                continue;
            }
            BurstBuilder builder = candidates.get(observation.sourceKey);
            if (builder == null) {
                if (candidates.size() >= 32) {
                    continue;
                }
                builder = new BurstBuilder(observation);
                candidates.put(observation.sourceKey, builder);
            }
            builder.add(observation);
            acceptedObservation = true;
        }
        if (!acceptedObservation) {
            return;
        }

        final long generation = ++burstGeneration;
        handler.postDelayed(new Runnable() {
            @Override
            public void run() {
                if (running && acceptingPresses && generation == burstGeneration) {
                    finalizeBurst();
                }
            }
        }, BURST_QUIET_MS);
    }

    private List<Observation> observations(ScanResult result) {
        List<Observation> values = new ArrayList<>();
        ScanRecord record = result.getScanRecord();
        SparseArray<byte[]> manufacturerData = record.getManufacturerSpecificData();
        for (int i = 0; manufacturerData != null && i < manufacturerData.size(); i++) {
            int manufacturerId = manufacturerData.keyAt(i);
            byte[] payload = manufacturerData.valueAt(i);
            if (payload != null && payload.length >= 4) {
                values.add(new Observation(
                        "mfg:" + manufacturerId,
                        "manufacturer",
                        manufacturerId,
                        payload,
                        result.getRssi()));
            }
        }
        Map<ParcelUuid, byte[]> serviceData = record.getServiceData();
        if (serviceData != null) {
            for (Map.Entry<ParcelUuid, byte[]> entry : serviceData.entrySet()) {
                byte[] payload = entry.getValue();
                if (payload != null && payload.length >= 4) {
                    values.add(new Observation(
                            "service:" + entry.getKey().toString(),
                            "service",
                            -1,
                            payload,
                            result.getRssi()));
                }
            }
        }
        return values;
    }

    private void finalizeBurst() {
        if (candidates.isEmpty()) {
            return;
        }
        BurstSample best = null;
        for (BurstBuilder builder : candidates.values()) {
            BurstSample sample = builder.finish();
            if (sample.observationCount < MIN_BURST_OBSERVATIONS) {
                continue;
            }
            if (best == null || sample.score > best.score) {
                best = sample;
            }
        }
        candidates.clear();
        if (best == null) {
            announceCurrentStep("信号太短，未计入；请短按一次");
            return;
        }
        if (lockedSource == null) {
            lockedSource = best.sourceKey;
            Log.i(TAG, "Locked BLE source " + lockedSource);
        }
        acceptBurst(best);
    }

    private void acceptBurst(BurstSample sample) {
        Step step = STEPS[stepIndex];
        if (sample.fanLampProfile != null) {
            if (learnedFanLampProfile == null) {
                learnedFanLampProfile = sample.fanLampProfile;
            } else if (!learnedFanLampProfile.fingerprint().equals(
                    sample.fanLampProfile.fingerprint())) {
                announceCurrentStep("检测到另一只遥控器，已忽略");
                return;
            }
        }

        if (sample.action != null) {
            if (step.alternatingPreset) {
                if (!"preset_full".equals(sample.action)
                        && !"preset_half".equals(sample.action)) {
                    announceCurrentStep("检测到其他按键，请按“" + step.title + "”");
                    return;
                }
            } else if (!step.expectedAction.equals(sample.action)) {
                announceCurrentStep("检测到其他按键，请按“" + step.title + "”");
                return;
            }
        } else if (!step.alternatingPreset) {
            if (stepReferenceSignature == null) {
                stepReferenceSignature = sample.signature;
            } else if (!stepReferenceSignature.equals(sample.signature)) {
                announceCurrentStep("三次报文不一致，本次未计入");
                return;
            }
        }

        acceptedPresses++;
        Log.i(TAG, "Accepted " + step.key + " press " + acceptedPresses
                + "/3, action=" + sample.action + ", packets="
                + sample.observationCount);
        stepBursts.put(sample.toJson());
        if (acceptedPresses < 3) {
            announceCurrentStep("已识别 " + acceptedPresses + "/3，请再按一次");
            return;
        }
        try {
            JSONObject stepValue = new JSONObject();
            stepValue.put("title", step.title);
            stepValue.put("bursts", stepBursts);
            recordings.put(step.key, stepValue);
        } catch (JSONException impossible) {
            listener.onError("无法保存录制结果");
            return;
        }
        advanceStep();
    }

    private void advanceStep() {
        acceptingPresses = false;
        stepIndex++;
        acceptedPresses = 0;
        stepReferenceSignature = null;
        stepBursts = new JSONArray();
        candidates.clear();
        burstGeneration++;
        if (stepIndex >= STEPS.length) {
            complete();
            return;
        }
        handler.postDelayed(new Runnable() {
            @Override
            public void run() {
                if (!running) return;
                acceptingPresses = true;
                announceCurrentStep("等待第 1 次独立按压");
            }
        }, STEP_TRANSITION_MS);
    }

    private void complete() {
        String protocolName = learnedFanLampProfile == null
                ? "未识别的 BLE 广播" : "FanLamp V1（可直接使用）";
        Result result = new Result(recordings, learnedFanLampProfile, protocolName);
        stop();
        listener.onCompleted(result);
    }

    private void announceCurrentStep(String message) {
        Log.i(TAG, "Step " + (stepIndex + 1) + "/" + STEPS.length + " "
                + STEPS[stepIndex].key + ": " + message);
        listener.onStepChanged(stepIndex, STEPS.length, STEPS[stepIndex],
                acceptedPresses, message);
    }

    private static final class BaselineSource {
        final long firstSeen;
        long lastSeen;
        int count;

        BaselineSource(long now) {
            firstSeen = now;
            lastSeen = now;
        }

        void observe(long now) {
            count++;
            lastSeen = now;
        }

        boolean isContinuous() {
            return count >= 3 && lastSeen - firstSeen >= BASELINE_CONTINUOUS_MS;
        }
    }

    private static final class Observation {
        final String sourceKey;
        final String kind;
        final int manufacturerId;
        final byte[] payload;
        final int rssi;
        final FanLampRemoteProtocol.DecodedFrame decoded;

        Observation(String sourceKey, String kind, int manufacturerId,
                byte[] payload, int rssi) {
            this.sourceKey = sourceKey;
            this.kind = kind;
            this.manufacturerId = manufacturerId;
            this.payload = payload.clone();
            this.rssi = rssi;
            this.decoded = manufacturerId == FanLampRemoteProtocol.MANUFACTURER_ID
                    ? FanLampRemoteProtocol.decodeAny(payload) : null;
        }
    }

    private static final class BurstBuilder {
        final String sourceKey;
        final String kind;
        final int manufacturerId;
        final LinkedHashMap<String, Integer> payloadCounts = new LinkedHashMap<>();
        final LinkedHashMap<String, FanLampRemoteProtocol.DecodedFrame> decodedFrames =
                new LinkedHashMap<>();
        int observationCount;
        int strongestRssi = -127;

        BurstBuilder(Observation first) {
            sourceKey = first.sourceKey;
            kind = first.kind;
            manufacturerId = first.manufacturerId;
        }

        void add(Observation observation) {
            observationCount++;
            strongestRssi = Math.max(strongestRssi, observation.rssi);
            String raw = FanLampRemoteProtocol.toHex(observation.payload);
            if (payloadCounts.containsKey(raw)) {
                payloadCounts.put(raw, payloadCounts.get(raw) + 1);
            } else if (payloadCounts.size() < MAX_UNIQUE_PAYLOADS) {
                payloadCounts.put(raw, 1);
            }
            if (observation.decoded != null) {
                String frameKey = observation.decoded.txCount + ":"
                        + observation.decoded.normalizedSignature();
                decodedFrames.put(frameKey, observation.decoded);
            }
        }

        BurstSample finish() {
            String action = null;
            FanLampRemoteProtocol.Profile profile = null;
            boolean actionConflict = false;
            for (FanLampRemoteProtocol.DecodedFrame frame : decodedFrames.values()) {
                if (profile == null) {
                    profile = frame.profile;
                }
                String frameAction = frame.actionKey();
                if (frameAction == null) continue;
                if (action == null) {
                    action = frameAction;
                } else if (!action.equals(frameAction)) {
                    actionConflict = true;
                }
            }
            if (actionConflict) {
                action = null;
            }
            String signature;
            if (action != null) {
                signature = "fanlamp:" + action;
            } else if (!decodedFrames.isEmpty()) {
                StringBuilder decoded = new StringBuilder("fanlamp:");
                for (FanLampRemoteProtocol.DecodedFrame frame : decodedFrames.values()) {
                    if (decoded.length() > 8) decoded.append('|');
                    decoded.append(frame.normalizedSignature());
                }
                signature = decoded.toString();
            } else {
                signature = mostFrequentPayload();
            }
            int score = observationCount + strongestRssi + 127;
            if (manufacturerId >= 0) score += 500;
            if (profile != null) score += 10_000;
            return new BurstSample(
                    sourceKey, kind, manufacturerId, observationCount,
                    strongestRssi, payloadCounts, decodedFrames,
                    signature, action, profile, score);
        }

        private String mostFrequentPayload() {
            String best = "";
            int bestCount = -1;
            for (Map.Entry<String, Integer> entry : payloadCounts.entrySet()) {
                if (entry.getValue() > bestCount) {
                    best = entry.getKey();
                    bestCount = entry.getValue();
                }
            }
            return best;
        }
    }

    private static final class BurstSample {
        final String sourceKey;
        final String kind;
        final int manufacturerId;
        final int observationCount;
        final int strongestRssi;
        final LinkedHashMap<String, Integer> payloadCounts;
        final LinkedHashMap<String, FanLampRemoteProtocol.DecodedFrame> decodedFrames;
        final String signature;
        final String action;
        final FanLampRemoteProtocol.Profile fanLampProfile;
        final int score;

        BurstSample(String sourceKey, String kind, int manufacturerId,
                int observationCount, int strongestRssi,
                LinkedHashMap<String, Integer> payloadCounts,
                LinkedHashMap<String, FanLampRemoteProtocol.DecodedFrame> decodedFrames,
                String signature, String action,
                FanLampRemoteProtocol.Profile fanLampProfile, int score) {
            this.sourceKey = sourceKey;
            this.kind = kind;
            this.manufacturerId = manufacturerId;
            this.observationCount = observationCount;
            this.strongestRssi = strongestRssi;
            this.payloadCounts = payloadCounts;
            this.decodedFrames = decodedFrames;
            this.signature = signature;
            this.action = action;
            this.fanLampProfile = fanLampProfile;
            this.score = score;
        }

        JSONObject toJson() {
            JSONObject value = new JSONObject();
            try {
                value.put("source", sourceKey);
                value.put("kind", kind);
                if (manufacturerId >= 0) value.put("manufacturer_id", manufacturerId);
                value.put("observations", observationCount);
                value.put("rssi", strongestRssi);
                value.put("signature", signature);
                if (action != null) value.put("action", action);
                JSONArray payloads = new JSONArray();
                for (Map.Entry<String, Integer> entry : payloadCounts.entrySet()) {
                    JSONObject payload = new JSONObject();
                    payload.put("hex", entry.getKey());
                    payload.put("copies", entry.getValue());
                    payloads.put(payload);
                }
                value.put("payloads", payloads);
                JSONArray frames = new JSONArray();
                for (FanLampRemoteProtocol.DecodedFrame frame : decodedFrames.values()) {
                    JSONObject decoded = new JSONObject();
                    decoded.put("tx_count", frame.txCount);
                    decoded.put("command", frame.command);
                    decoded.put("parameter", frame.parameter);
                    decoded.put("arg0", frame.arg0);
                    decoded.put("arg1", frame.arg1);
                    decoded.put("arg2", frame.arg2);
                    frames.put(decoded);
                }
                value.put("decoded_frames", frames);
            } catch (JSONException impossible) {
                throw new IllegalStateException("Unable to serialize burst", impossible);
            }
            return value;
        }
    }
}
