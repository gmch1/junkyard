package com.ming.lightprobe;

import android.Manifest;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.bluetooth.BluetoothAdapter;
import android.bluetooth.BluetoothManager;
import android.bluetooth.le.BluetoothLeScanner;
import android.bluetooth.le.ScanCallback;
import android.bluetooth.le.ScanRecord;
import android.bluetooth.le.ScanResult;
import android.bluetooth.le.ScanSettings;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Build;
import android.os.IBinder;
import android.os.ParcelUuid;
import android.os.SystemClock;
import android.util.Log;
import android.util.SparseArray;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.TimeZone;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicLong;

public final class BleScanService extends Service {
    public static final String ACTION_START = "com.ming.lightprobe.START";
    public static final String EXTRA_MIN_RSSI = "min_rssi";

    private static final String TAG = "BLE_PROBE";
    private static final String CHANNEL_ID = "ble_probe_scan";
    private static final int NOTIFICATION_ID = 1001;

    private final AtomicLong sequence = new AtomicLong();
    private BluetoothLeScanner scanner;
    private boolean scanning;
    private int minRssi = -75;
    private String sessionId;

    private final ScanCallback callback = new ScanCallback() {
        @Override
        public void onScanResult(int callbackType, ScanResult result) {
            logResult(result);
        }

        @Override
        public void onBatchScanResults(List<ScanResult> results) {
            for (ScanResult result : results) {
                logResult(result);
            }
        }

        @Override
        public void onScanFailed(int errorCode) {
            logEvent("scan_failed", "error_code", errorCode);
            scanning = false;
        }
    };

    @Override
    public void onCreate() {
        super.onCreate();
        createNotificationChannel();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        minRssi = intent == null ? -75 : intent.getIntExtra(EXTRA_MIN_RSSI, -75);
        startForeground(NOTIFICATION_ID, buildNotification());
        startScan();
        return START_STICKY;
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL_ID,
                    "BLE remote scanning",
                    NotificationManager.IMPORTANCE_LOW);
            channel.setDescription("在熄屏时保持蓝牙诊断扫描");
            NotificationManager manager = getSystemService(NotificationManager.class);
            manager.createNotificationChannel(channel);
        }
    }

    private Notification buildNotification() {
        Intent launch = new Intent(this, MainActivity.class);
        int pendingFlags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            pendingFlags |= PendingIntent.FLAG_IMMUTABLE;
        }
        PendingIntent pendingIntent = PendingIntent.getActivity(this, 0, launch, pendingFlags);

        Notification.Builder builder = Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        return builder
                .setContentTitle("正在扫描灯具遥控广播")
                .setContentText("Minimum RSSI: " + minRssi + " dBm")
                .setSmallIcon(android.R.drawable.stat_notify_sync)
                .setContentIntent(pendingIntent)
                .setOngoing(true)
                .build();
    }

    private boolean hasScanPermission() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            return checkSelfPermission(Manifest.permission.BLUETOOTH_SCAN)
                    == PackageManager.PERMISSION_GRANTED;
        }
        return Build.VERSION.SDK_INT < Build.VERSION_CODES.M
                || checkSelfPermission(Manifest.permission.ACCESS_FINE_LOCATION)
                == PackageManager.PERMISSION_GRANTED;
    }

    private void startScan() {
        stopScan();
        sessionId = UUID.randomUUID().toString();
        sequence.set(0);

        if (!hasScanPermission()) {
            logEvent("scan_failed", "reason", "missing_scan_permission");
            stopSelf();
            return;
        }

        BluetoothManager manager = (BluetoothManager) getSystemService(BLUETOOTH_SERVICE);
        BluetoothAdapter adapter = manager == null ? null : manager.getAdapter();
        try {
            if (adapter == null) {
                logEvent("scan_failed", "reason", "bluetooth_unavailable");
                stopSelf();
                return;
            }
            if (!adapter.isEnabled()) {
                logEvent("scan_failed", "reason", "bluetooth_disabled");
                stopSelf();
                return;
            }
            scanner = adapter.getBluetoothLeScanner();
            if (scanner == null) {
                logEvent("scan_failed", "reason", "scanner_unavailable");
                stopSelf();
                return;
            }

            ScanSettings settings = new ScanSettings.Builder()
                    .setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY)
                    .setCallbackType(ScanSettings.CALLBACK_TYPE_ALL_MATCHES)
                    .setReportDelay(0)
                    .build();
            scanner.startScan(null, settings, callback);
            scanning = true;

            JSONObject event = baseEvent("session_started");
            event.put("min_rssi", minRssi);
            event.put("android_api", Build.VERSION.SDK_INT);
            event.put("device", Build.MANUFACTURER + " " + Build.MODEL);
            Log.i(TAG, event.toString());
        } catch (SecurityException | IllegalStateException | JSONException error) {
            logEvent("scan_failed", "reason", error.toString());
            stopSelf();
        }
    }

    private void stopScan() {
        if (!scanning || scanner == null) {
            return;
        }
        try {
            scanner.stopScan(callback);
        } catch (SecurityException ignored) {
            // Permission may have been revoked while scanning.
        }
        scanning = false;
        logEvent("session_stopped", "reason", "service_stopped");
    }

    private void logResult(ScanResult result) {
        if (result == null || result.getRssi() < minRssi) {
            return;
        }

        try {
            ScanRecord record = result.getScanRecord();
            JSONObject event = baseEvent("advertisement");
            event.put("seq", sequence.incrementAndGet());
            event.put("elapsed_ms", SystemClock.elapsedRealtime());
            event.put("rssi", result.getRssi());
            event.put("address", result.getDevice() == null
                    ? JSONObject.NULL : result.getDevice().getAddress());

            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                event.put("connectable", result.isConnectable());
                event.put("primary_phy", result.getPrimaryPhy());
                event.put("secondary_phy", result.getSecondaryPhy());
                event.put("sid", result.getAdvertisingSid());
            }

            if (record != null) {
                event.put("name", nullable(record.getDeviceName()));
                event.put("raw", hex(record.getBytes()));
                event.put("tx_power", record.getTxPowerLevel());
                event.put("advertise_flags", record.getAdvertiseFlags());
                event.put("manufacturer_data", manufacturerData(record));
                event.put("service_data", serviceData(record));
                event.put("service_uuids", serviceUuids(record));
            }
            Log.i(TAG, event.toString());
        } catch (SecurityException | JSONException error) {
            logEvent("result_error", "reason", error.toString());
        }
    }

    private JSONObject manufacturerData(ScanRecord record) throws JSONException {
        JSONObject output = new JSONObject();
        SparseArray<byte[]> values = record.getManufacturerSpecificData();
        for (int i = 0; values != null && i < values.size(); i++) {
            output.put(String.format(Locale.US, "0x%04X", values.keyAt(i)),
                    hex(values.valueAt(i)));
        }
        return output;
    }

    private JSONObject serviceData(ScanRecord record) throws JSONException {
        JSONObject output = new JSONObject();
        Map<ParcelUuid, byte[]> values = record.getServiceData();
        if (values != null) {
            for (Map.Entry<ParcelUuid, byte[]> entry : values.entrySet()) {
                output.put(entry.getKey().toString(), hex(entry.getValue()));
            }
        }
        return output;
    }

    private JSONArray serviceUuids(ScanRecord record) {
        JSONArray output = new JSONArray();
        List<ParcelUuid> values = record.getServiceUuids();
        if (values != null) {
            for (ParcelUuid value : values) {
                output.put(value.toString());
            }
        }
        return output;
    }

    private JSONObject baseEvent(String name) throws JSONException {
        JSONObject event = new JSONObject();
        event.put("event", name);
        event.put("time", timestamp());
        event.put("session", nullable(sessionId));
        return event;
    }

    private void logEvent(String name, String key, Object value) {
        try {
            JSONObject event = baseEvent(name);
            event.put(key, value);
            Log.i(TAG, event.toString());
        } catch (JSONException impossible) {
            Log.e(TAG, name + ": " + value);
        }
    }

    private static Object nullable(Object value) {
        return value == null ? JSONObject.NULL : value;
    }

    private static String timestamp() {
        SimpleDateFormat format = new SimpleDateFormat(
                "yyyy-MM-dd'T'HH:mm:ss.SSSXXX", Locale.US);
        format.setTimeZone(TimeZone.getDefault());
        return format.format(new Date());
    }

    private static String hex(byte[] bytes) {
        if (bytes == null) {
            return "";
        }
        char[] digits = "0123456789ABCDEF".toCharArray();
        char[] output = new char[bytes.length * 2];
        for (int i = 0; i < bytes.length; i++) {
            int value = bytes[i] & 0xFF;
            output[i * 2] = digits[value >>> 4];
            output[i * 2 + 1] = digits[value & 0x0F];
        }
        return new String(output);
    }

    @Override
    public void onDestroy() {
        stopScan();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
