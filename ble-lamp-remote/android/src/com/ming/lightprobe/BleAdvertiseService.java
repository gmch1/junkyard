package com.ming.lightprobe;

import android.Manifest;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.bluetooth.BluetoothAdapter;
import android.bluetooth.BluetoothManager;
import android.bluetooth.le.AdvertiseCallback;
import android.bluetooth.le.AdvertiseData;
import android.bluetooth.le.AdvertiseSettings;
import android.bluetooth.le.BluetoothLeAdvertiser;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.os.ParcelUuid;
import android.util.Log;

import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

public final class BleAdvertiseService extends Service {
    public static final String EXTRA_SERVICE_DATA = "service_data";
    public static final String EXTRA_MANUFACTURER_DATA = "manufacturer_data";
    public static final String EXTRA_DURATION_MS = "duration_ms";

    private static final String TAG = "BLE_PROBE";
    private static final String CHANNEL_ID = "ble_probe_advertise";
    private static final int NOTIFICATION_ID = 1002;
    private static final int PARALLEL_ADVERTISERS = 4;
    private static final ParcelUuid SERVICE_UUID = new ParcelUuid(
            UUID.fromString("000008f0-0000-1000-8000-00805f9b34fb"));

    private final Handler handler = new Handler(Looper.getMainLooper());
    private BluetoothLeAdvertiser advertiser;
    private boolean advertising;
    private int startedAdvertisers;
    private int failedAdvertisers;
    private int requestedAdvertisers;
    private final List<AdvertiseCallback> callbacks = new ArrayList<>();
    private String serviceDataHex;
    private String manufacturerDataHex;

    @Override
    public void onCreate() {
        super.onCreate();
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL_ID, "BLE command advertising", NotificationManager.IMPORTANCE_LOW);
            getSystemService(NotificationManager.class).createNotificationChannel(channel);
        }
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        stopAdvertisement();
        serviceDataHex = intent == null ? null : intent.getStringExtra(EXTRA_SERVICE_DATA);
        manufacturerDataHex = intent == null ? null
                : intent.getStringExtra(EXTRA_MANUFACTURER_DATA);
        int durationMs = intent == null ? 2000
                : Math.max(200, Math.min(30000, intent.getIntExtra(EXTRA_DURATION_MS, 2000)));
        startForeground(NOTIFICATION_ID, buildNotification(durationMs));
        startAdvertisement(durationMs);
        return START_NOT_STICKY;
    }

    private Notification buildNotification(int durationMs) {
        Intent launch = new Intent(this, MainActivity.class);
        int flags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            flags |= PendingIntent.FLAG_IMMUTABLE;
        }
        PendingIntent pendingIntent = PendingIntent.getActivity(this, 0, launch, flags);
        Notification.Builder builder = Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        return builder
                .setContentTitle("正在发送灯控指令")
                .setContentText("蓝牙广播持续 " + durationMs + " ms")
                .setSmallIcon(android.R.drawable.stat_notify_sync)
                .setContentIntent(pendingIntent)
                .setOngoing(true)
                .build();
    }

    private void startAdvertisement(int durationMs) {
        boolean hasServiceData = serviceDataHex != null;
        boolean hasManufacturerData = manufacturerDataHex != null;
        if (!hasServiceData && !hasManufacturerData) {
            logEvent("advertise_failed", "reason", "provide_advertisement_data");
            stopSelf();
            return;
        }
        if (hasServiceData && serviceDataHex.length() != 48) {
            logEvent("advertise_failed", "reason", "service_data_must_be_24_bytes");
            stopSelf();
            return;
        }
        if (hasManufacturerData && manufacturerDataHex.length() != 54) {
            logEvent("advertise_failed", "reason", "manufacturer_data_must_be_27_bytes");
            stopSelf();
            return;
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S
                && checkSelfPermission(Manifest.permission.BLUETOOTH_ADVERTISE)
                != PackageManager.PERMISSION_GRANTED) {
            logEvent("advertise_failed", "reason", "missing_advertise_permission");
            stopSelf();
            return;
        }

        try {
            BluetoothManager manager = (BluetoothManager) getSystemService(BLUETOOTH_SERVICE);
            BluetoothAdapter adapter = manager == null ? null : manager.getAdapter();
            if (adapter == null || !adapter.isEnabled()) {
                logEvent("advertise_failed", "reason", "bluetooth_unavailable_or_disabled");
                stopSelf();
                return;
            }
            advertiser = adapter.getBluetoothLeAdvertiser();
            if (advertiser == null) {
                logEvent("advertise_failed", "reason", "advertiser_unavailable");
                stopSelf();
                return;
            }

            AdvertiseSettings settings = new AdvertiseSettings.Builder()
                    .setAdvertiseMode(AdvertiseSettings.ADVERTISE_MODE_LOW_LATENCY)
                    .setTxPowerLevel(AdvertiseSettings.ADVERTISE_TX_POWER_HIGH)
                    .setConnectable(false)
                    .setTimeout(durationMs)
                    .build();
            List<AdvertiseData> dataVariants = new ArrayList<>();
            if (hasServiceData) {
                dataVariants.add(new AdvertiseData.Builder()
                        .setIncludeDeviceName(false)
                        .setIncludeTxPowerLevel(false)
                        .addServiceData(SERVICE_UUID, fromHex(serviceDataHex))
                        .build());
            }
            if (hasManufacturerData) {
                dataVariants.add(new AdvertiseData.Builder()
                        .setIncludeDeviceName(false)
                        .setIncludeTxPowerLevel(false)
                        .addManufacturerData(0x5556, fromHex(manufacturerDataHex))
                        .build());
            }
            startedAdvertisers = 0;
            failedAdvertisers = 0;
            int instancesPerVariant = Math.max(1,
                    PARALLEL_ADVERTISERS / dataVariants.size());
            requestedAdvertisers = instancesPerVariant * dataVariants.size();
            int instance = 0;
            for (AdvertiseData data : dataVariants) {
                for (int i = 0; i < instancesPerVariant; i++) {
                    AdvertiseCallback callback = createCallback(instance++);
                    callbacks.add(callback);
                    advertiser.startAdvertising(settings, data, callback);
                }
            }
            handler.postDelayed(new Runnable() {
                @Override
                public void run() {
                    stopSelf();
                }
            }, durationMs + 250L);
        } catch (IllegalArgumentException | IllegalStateException | SecurityException error) {
            logEvent("advertise_failed", "reason", error.toString());
            stopSelf();
        }
    }

    private AdvertiseCallback createCallback(final int instance) {
        return new AdvertiseCallback() {
            @Override
            public void onStartSuccess(AdvertiseSettings settingsInEffect) {
                startedAdvertisers++;
                advertising = true;
                logEvent("advertise_instance_started", "instance", instance);
                if (startedAdvertisers == 1) {
                    logEvent("advertise_started", dataKind(), activeDataHex());
                }
            }

            @Override
            public void onStartFailure(int errorCode) {
                failedAdvertisers++;
                logEvent("advertise_instance_failed", "error_code", errorCode);
                if (failedAdvertisers == requestedAdvertisers
                        && startedAdvertisers == 0) {
                    stopSelf();
                }
            }
        };
    }

    private static byte[] fromHex(String value) {
        byte[] output = new byte[value.length() / 2];
        for (int i = 0; i < output.length; i++) {
            int high = Character.digit(value.charAt(i * 2), 16);
            int low = Character.digit(value.charAt(i * 2 + 1), 16);
            if (high < 0 || low < 0) {
                throw new IllegalArgumentException("Invalid hexadecimal service data");
            }
            output[i] = (byte) ((high << 4) | low);
        }
        return output;
    }

    private void logEvent(String name, String key, Object value) {
        try {
            JSONObject event = new JSONObject();
            event.put("event", name);
            event.put("epoch_ms", System.currentTimeMillis());
            event.put(key, value);
            Log.i(TAG, event.toString());
        } catch (JSONException impossible) {
            Log.e(TAG, name + ": " + value);
        }
    }

    private void stopAdvertisement() {
        handler.removeCallbacksAndMessages(null);
        if (advertiser != null) {
            for (AdvertiseCallback callback : callbacks) {
                try {
                    advertiser.stopAdvertising(callback);
                } catch (SecurityException ignored) {
                    // Permission may have been revoked while transmitting.
                }
            }
        }
        if (advertising) {
            logEvent("advertise_stopped", dataKind(), activeDataHex());
        }
        advertising = false;
        callbacks.clear();
        startedAdvertisers = 0;
        failedAdvertisers = 0;
        requestedAdvertisers = 0;
    }

    @Override
    public void onDestroy() {
        stopAdvertisement();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    private String dataKind() {
        if (manufacturerDataHex != null && serviceDataHex != null) {
            return "data_mode";
        }
        return manufacturerDataHex == null ? "service_data" : "manufacturer_data";
    }

    private String activeDataHex() {
        if (manufacturerDataHex != null && serviceDataHex != null) {
            return "dual";
        }
        return manufacturerDataHex == null ? serviceDataHex : manufacturerDataHex;
    }
}
