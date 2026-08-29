package com.ming.lightprobe;

import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.os.Build;
import android.os.Handler;
import android.os.Looper;

/** Process-wide dispatcher shared by the phone UI and the LAN API. */
public final class LightCommandDispatcher {
    public static final String ACTION_ON = "on";
    public static final String ACTION_OFF = "off";
    public static final String ACTION_BRIGHTER = "brightness_up";
    public static final String ACTION_DIMMER = "brightness_down";
    public static final String ACTION_WARMER = "temperature_warmer";
    public static final String ACTION_COOLER = "temperature_cooler";
    public static final String ACTION_PRESET_FULL = "preset_full";
    public static final String ACTION_PRESET_HALF = "preset_half";
    public static final String ACTION_PRESET_TOGGLE = "preset_toggle";

    private static final String PREF_NEXT_PRESET_FULL = "next_preset_full";
    private static final int FRAME_GAP_MS = 180;
    private static final int FINAL_BURST_MS = 700;
    private static final Handler HANDLER = new Handler(Looper.getMainLooper());
    private static final Object LOCK = new Object();

    private LightCommandDispatcher() {
    }

    public static String dispatchNamed(Context context, String action) {
        if (ACTION_ON.equals(action)) {
            sendSequence(context, "开灯", new int[][] {{0x10, 0, 0, 0, 0}});
            return "开灯";
        }
        if (ACTION_OFF.equals(action)) {
            sendSequence(context, "关灯", new int[][] {{0x11, 0, 0, 0, 0}});
            return "关灯";
        }
        if (ACTION_BRIGHTER.equals(action)) {
            sendSequence(context, "亮度增加", new int[][] {
                    {0x39, 0, 0, 0, 0}, {0x21, 0x14, 0, 2, 0}});
            return "亮度增加";
        }
        if (ACTION_DIMMER.equals(action)) {
            sendSequence(context, "亮度降低", new int[][] {
                    {0x39, 1, 0, 0, 0}, {0x21, 0x28, 0, 2, 0}});
            return "亮度降低";
        }
        if (ACTION_WARMER.equals(action)) {
            sendSequence(context, "色温偏暖", new int[][] {
                    {0x39, 3, 0, 0, 0}, {0x21, 0x18, 0, 2, 0}});
            return "色温偏暖";
        }
        if (ACTION_COOLER.equals(action)) {
            sendSequence(context, "色温偏冷", new int[][] {
                    {0x39, 2, 0, 0, 0}, {0x21, 0x24, 0, 2, 0}});
            return "色温偏冷";
        }
        if (ACTION_PRESET_FULL.equals(action)) {
            sendSequence(context, "全亮", new int[][] {{0x21, 2, 255, 255, 0}});
            return "全亮";
        }
        if (ACTION_PRESET_HALF.equals(action)) {
            sendSequence(context, "半亮", new int[][] {{0x21, 1, 127, 127, 0}});
            return "半亮";
        }
        if (ACTION_PRESET_TOGGLE.equals(action)) {
            SharedPreferences preferences = context.getSharedPreferences(
                    FanLampRemoteProtocol.PREFS_NAME, Context.MODE_PRIVATE);
            boolean sendFull;
            synchronized (LOCK) {
                sendFull = preferences.getBoolean(PREF_NEXT_PRESET_FULL, true);
                preferences.edit().putBoolean(PREF_NEXT_PRESET_FULL, !sendFull).apply();
            }
            if (sendFull) {
                sendSequence(context, "全亮", new int[][] {{0x21, 2, 255, 255, 0}});
                return "全亮";
            }
            sendSequence(context, "半亮", new int[][] {{0x21, 1, 127, 127, 0}});
            return "半亮";
        }
        return null;
    }

    public static void sendSequence(Context source, String label, int[][] commands) {
        final Context context = source.getApplicationContext();
        final String[] payloads = new String[commands.length];
        synchronized (LOCK) {
            HANDLER.removeCallbacksAndMessages(null);
            SharedPreferences preferences = context.getSharedPreferences(
                    FanLampRemoteProtocol.PREFS_NAME, Context.MODE_PRIVATE);
            RemoteProfileStore.RemoteProfile activeProfile = RemoteProfileStore.active(context);
            FanLampRemoteProtocol.Profile protocolProfile = activeProfile.fanLampProfile == null
                    ? FanLampRemoteProtocol.builtinProfile() : activeProfile.fanLampProfile;
            int sequence = preferences.getInt(FanLampRemoteProtocol.PREF_TX_COUNT, 0);
            for (int i = 0; i < commands.length; i++) {
                sequence = (sequence + 1) % FanLampRemoteProtocol.TX_COUNT_MODULUS;
                int[] command = commands[i];
                payloads[i] = FanLampRemoteProtocol.toHex(FanLampRemoteProtocol.encode(
                        protocolProfile, command[0], command[1], command[2],
                        command[3], command[4], sequence));
            }
            preferences.edit()
                    .putInt(FanLampRemoteProtocol.PREF_TX_COUNT, sequence)
                    .putString(LightApiService.PREF_LAST_ACTION, label)
                    .putLong(LightApiService.PREF_LAST_ACTION_AT, System.currentTimeMillis())
                    .apply();

            for (int i = 0; i < payloads.length; i++) {
                final int index = i;
                HANDLER.postDelayed(new Runnable() {
                    @Override
                    public void run() {
                        int durationMs = index == payloads.length - 1
                                ? FINAL_BURST_MS : FRAME_GAP_MS - 10;
                        startManufacturerAdvertisement(context, payloads[index], durationMs);
                    }
                }, i * (long) FRAME_GAP_MS);
            }
        }
    }

    private static void startManufacturerAdvertisement(Context context,
            String payload, int durationMs) {
        Intent serviceIntent = new Intent(context, BleAdvertiseService.class);
        serviceIntent.putExtra(BleAdvertiseService.EXTRA_MANUFACTURER_DATA, payload);
        serviceIntent.putExtra(BleAdvertiseService.EXTRA_DURATION_MS, durationMs);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            context.startForegroundService(serviceIntent);
        } else {
            context.startService(serviceIntent);
        }
    }
}
