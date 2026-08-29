package com.ming.lightprobe;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.bluetooth.BluetoothAdapter;
import android.bluetooth.BluetoothManager;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.os.Build;
import android.os.IBinder;
import android.util.Log;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.Inet4Address;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.NetworkInterface;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Enumeration;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/** Small authenticated HTTP server that exposes only the lamp's fixed actions. */
public final class LightApiService extends Service {
    public static final int PORT = 8791;
    public static final String PREF_API_ENABLED = "lan_api_enabled";
    public static final String PREF_API_TOKEN = "lan_api_token";
    public static final String PREF_LAST_ACTION = "last_action";
    public static final String PREF_LAST_ACTION_AT = "last_action_at";

    private static final String TAG = "LIGHT_API";
    private static final String CHANNEL_ID = "light_lan_api";
    private static final int NOTIFICATION_ID = 1003;
    private static final int MAX_LINE_BYTES = 4096;

    private final Object serverLock = new Object();
    private final ExecutorService clients = Executors.newFixedThreadPool(2);
    private volatile boolean stopped;
    private ServerSocket serverSocket;
    private Thread serverThread;
    private InetAddress boundAddress;

    @Override
    public void onCreate() {
        super.onCreate();
        if (!isEnabled(this)) {
            stopSelf();
            return;
        }
        createNotificationChannel();
        startForeground(NOTIFICATION_ID, buildNotification("正在启动局域网 API"));
        ensureServer();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (!isEnabled(this)) {
            stopSelf();
            return START_NOT_STICKY;
        }
        ensureServer();
        return START_STICKY;
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL_ID, "灯控局域网 API", NotificationManager.IMPORTANCE_LOW);
            channel.setDescription("让局域网内已授权的设备控制灯具");
            getSystemService(NotificationManager.class).createNotificationChannel(channel);
        }
    }

    private Notification buildNotification(String text) {
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
                .setContentTitle("佛山照明遥控器")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
                .setContentIntent(pendingIntent)
                .setOngoing(true)
                .build();
    }

    private void ensureServer() {
        synchronized (serverLock) {
            InetAddress address = findLanIPv4();
            if (address == null) {
                updateNotification("未连接到 IPv4 局域网");
                return;
            }
            if (serverSocket != null && !serverSocket.isClosed()
                    && address.equals(boundAddress)) {
                return;
            }
            closeServerSocket();
            stopped = false;
            try {
                ServerSocket socket = new ServerSocket();
                socket.setReuseAddress(true);
                socket.bind(new InetSocketAddress(address, PORT), 16);
                serverSocket = socket;
                boundAddress = address;
                getOrCreateToken(this);
                serverThread = new Thread(new Runnable() {
                    @Override
                    public void run() {
                        acceptLoop(socket);
                    }
                }, "light-api-accept");
                serverThread.start();
                String endpoint = endpointFor(address);
                Log.i(TAG, "Listening on " + endpoint);
                updateNotification(endpoint);
            } catch (IOException error) {
                Log.e(TAG, "Unable to bind LAN API", error);
                updateNotification("API 启动失败：端口 " + PORT);
                closeServerSocket();
            }
        }
    }

    private void acceptLoop(ServerSocket socket) {
        while (!stopped && !socket.isClosed()) {
            try {
                final Socket client = socket.accept();
                clients.execute(new Runnable() {
                    @Override
                    public void run() {
                        handleClient(client);
                    }
                });
            } catch (IOException error) {
                if (!stopped) {
                    Log.w(TAG, "Accept failed", error);
                }
                return;
            }
        }
    }

    private void handleClient(Socket socket) {
        try (Socket client = socket) {
            client.setSoTimeout(3000);
            InputStream input = client.getInputStream();
            OutputStream output = client.getOutputStream();
            String requestLine = readAsciiLine(input);
            if (requestLine == null) {
                return;
            }
            String[] parts = requestLine.split(" ");
            if (parts.length != 3 || !parts[2].startsWith("HTTP/1.")) {
                sendJson(output, 400, "Bad Request", errorJson("invalid request line"), null);
                return;
            }
            String method = parts[0];
            String path = parts[1];
            if (path.indexOf('?') >= 0 || path.indexOf('#') >= 0) {
                sendJson(output, 404, "Not Found", errorJson("unknown endpoint"), null);
                return;
            }

            Map<String, String> headers = new HashMap<>();
            int headerBytes = requestLine.length();
            while (true) {
                String line = readAsciiLine(input);
                if (line == null) {
                    sendJson(output, 400, "Bad Request", errorJson("incomplete headers"), null);
                    return;
                }
                headerBytes += line.length();
                if (headerBytes > 8192) {
                    sendJson(output, 431, "Request Header Fields Too Large",
                            errorJson("headers too large"), null);
                    return;
                }
                if (line.isEmpty()) {
                    break;
                }
                int colon = line.indexOf(':');
                if (colon <= 0) {
                    sendJson(output, 400, "Bad Request", errorJson("invalid header"), null);
                    return;
                }
                headers.put(line.substring(0, colon).trim().toLowerCase(),
                        line.substring(colon + 1).trim());
            }

            if (!authorized(headers.get("authorization"))) {
                sendJson(output, 401, "Unauthorized", errorJson("invalid bearer token"),
                        "WWW-Authenticate: Bearer\r\n");
                return;
            }

            int contentLength = parseContentLength(headers.get("content-length"));
            if (contentLength < 0 || contentLength > 256) {
                sendJson(output, 413, "Payload Too Large", errorJson("body too large"), null);
                return;
            }
            for (int remaining = contentLength; remaining > 0;) {
                int skipped = input.read();
                if (skipped < 0) {
                    sendJson(output, 400, "Bad Request", errorJson("incomplete body"), null);
                    return;
                }
                remaining--;
            }

            if ("GET".equals(method) && "/v1/status".equals(path)) {
                sendJson(output, 200, "OK", statusJson(), null);
                return;
            }
            if (!"POST".equals(method)) {
                sendJson(output, 405, "Method Not Allowed", errorJson("method not allowed"),
                        "Allow: GET, POST\r\n");
                return;
            }
            String action = actionForPath(path);
            if (action == null) {
                sendJson(output, 404, "Not Found", errorJson("unknown endpoint"), null);
                return;
            }
            String label = LightCommandDispatcher.dispatchNamed(this, action);
            JSONObject response = new JSONObject();
            response.put("accepted", true);
            response.put("action", action);
            response.put("label", label);
            sendJson(output, 202, "Accepted", response, null);
        } catch (IOException | JSONException error) {
            Log.w(TAG, "Request failed", error);
        }
    }

    private String actionForPath(String path) {
        if ("/v1/light/on".equals(path)) {
            return LightCommandDispatcher.ACTION_ON;
        }
        if ("/v1/light/off".equals(path)) {
            return LightCommandDispatcher.ACTION_OFF;
        }
        if ("/v1/light/brightness/up".equals(path)) {
            return LightCommandDispatcher.ACTION_BRIGHTER;
        }
        if ("/v1/light/brightness/down".equals(path)) {
            return LightCommandDispatcher.ACTION_DIMMER;
        }
        if ("/v1/light/temperature/warmer".equals(path)) {
            return LightCommandDispatcher.ACTION_WARMER;
        }
        if ("/v1/light/temperature/cooler".equals(path)) {
            return LightCommandDispatcher.ACTION_COOLER;
        }
        if ("/v1/light/preset/full".equals(path)) {
            return LightCommandDispatcher.ACTION_PRESET_FULL;
        }
        if ("/v1/light/preset/half".equals(path)) {
            return LightCommandDispatcher.ACTION_PRESET_HALF;
        }
        if ("/v1/light/preset/toggle".equals(path)) {
            return LightCommandDispatcher.ACTION_PRESET_TOGGLE;
        }
        return null;
    }

    private JSONObject statusJson() throws JSONException {
        SharedPreferences preferences = getSharedPreferences(
                FanLampRemoteProtocol.PREFS_NAME, MODE_PRIVATE);
        RemoteProfileStore.RemoteProfile activeProfile = RemoteProfileStore.active(this);
        JSONObject response = new JSONObject();
        response.put("version", 1);
        response.put("device", activeProfile.name);
        response.put("profile_id", activeProfile.id);
        response.put("protocol", activeProfile.protocol);
        response.put("api", boundAddress == null ? JSONObject.NULL
                : endpointFor(boundAddress));
        response.put("bluetooth_ready", bluetoothReady());
        response.put("tx_count", preferences.getInt(
                FanLampRemoteProtocol.PREF_TX_COUNT, 0));
        response.put("last_action", preferences.getString(PREF_LAST_ACTION, ""));
        response.put("last_action_at", preferences.getLong(PREF_LAST_ACTION_AT, 0L));
        return response;
    }

    private boolean bluetoothReady() {
        try {
            BluetoothManager manager = (BluetoothManager) getSystemService(BLUETOOTH_SERVICE);
            BluetoothAdapter adapter = manager == null ? null : manager.getAdapter();
            return adapter != null && adapter.isEnabled()
                    && adapter.isMultipleAdvertisementSupported();
        } catch (SecurityException ignored) {
            return false;
        }
    }

    private boolean authorized(String authorization) {
        if (authorization == null || !authorization.startsWith("Bearer ")) {
            return false;
        }
        byte[] supplied = authorization.substring(7).getBytes(StandardCharsets.UTF_8);
        byte[] expected = getOrCreateToken(this).getBytes(StandardCharsets.UTF_8);
        return MessageDigest.isEqual(supplied, expected);
    }

    private int parseContentLength(String value) {
        if (value == null || value.isEmpty()) {
            return 0;
        }
        try {
            return Integer.parseInt(value);
        } catch (NumberFormatException error) {
            return -1;
        }
    }

    private JSONObject errorJson(String message) throws JSONException {
        JSONObject response = new JSONObject();
        response.put("error", message);
        return response;
    }

    private void sendJson(OutputStream output, int status, String reason,
            JSONObject body, String extraHeaders) throws IOException {
        byte[] payload = (body.toString() + "\n").getBytes(StandardCharsets.UTF_8);
        StringBuilder headers = new StringBuilder();
        headers.append("HTTP/1.1 ").append(status).append(' ').append(reason).append("\r\n")
                .append("Content-Type: application/json; charset=utf-8\r\n")
                .append("Cache-Control: no-store\r\n")
                .append("Connection: close\r\n")
                .append("Content-Length: ").append(payload.length).append("\r\n");
        if (extraHeaders != null) {
            headers.append(extraHeaders);
        }
        headers.append("\r\n");
        output.write(headers.toString().getBytes(StandardCharsets.US_ASCII));
        output.write(payload);
        output.flush();
    }

    private String readAsciiLine(InputStream input) throws IOException {
        ByteArrayOutputStream buffer = new ByteArrayOutputStream();
        while (buffer.size() <= MAX_LINE_BYTES) {
            int value = input.read();
            if (value < 0) {
                return buffer.size() == 0 ? null
                        : new String(buffer.toByteArray(), StandardCharsets.US_ASCII);
            }
            if (value == '\n') {
                byte[] bytes = buffer.toByteArray();
                int length = bytes.length;
                if (length > 0 && bytes[length - 1] == '\r') {
                    length--;
                }
                return new String(bytes, 0, length, StandardCharsets.US_ASCII);
            }
            buffer.write(value);
        }
        throw new IOException("HTTP line too long");
    }

    public static synchronized String getOrCreateToken(Context context) {
        SharedPreferences preferences = context.getSharedPreferences(
                FanLampRemoteProtocol.PREFS_NAME, Context.MODE_PRIVATE);
        String existing = preferences.getString(PREF_API_TOKEN, null);
        if (existing != null && existing.length() == 64) {
            return existing;
        }
        return rotateToken(context);
    }

    public static boolean isEnabled(Context context) {
        return context.getSharedPreferences(FanLampRemoteProtocol.PREFS_NAME,
                Context.MODE_PRIVATE).getBoolean(PREF_API_ENABLED, false);
    }

    public static void setEnabled(Context context, boolean enabled) {
        context.getSharedPreferences(FanLampRemoteProtocol.PREFS_NAME,
                Context.MODE_PRIVATE).edit().putBoolean(PREF_API_ENABLED, enabled).apply();
    }

    public static synchronized String rotateToken(Context context) {
        SharedPreferences preferences = context.getSharedPreferences(
                FanLampRemoteProtocol.PREFS_NAME, Context.MODE_PRIVATE);
        byte[] random = new byte[32];
        new SecureRandom().nextBytes(random);
        char[] digits = "0123456789abcdef".toCharArray();
        char[] hex = new char[random.length * 2];
        for (int i = 0; i < random.length; i++) {
            int value = random[i] & 0xFF;
            hex[i * 2] = digits[value >>> 4];
            hex[i * 2 + 1] = digits[value & 0x0F];
        }
        String token = new String(hex);
        preferences.edit().putString(PREF_API_TOKEN, token).commit();
        return token;
    }

    public static String endpoint() {
        InetAddress address = findLanIPv4();
        return address == null ? "未连接局域网" : endpointFor(address);
    }

    private static String endpointFor(InetAddress address) {
        return "http://" + address.getHostAddress() + ":" + PORT;
    }

    private static InetAddress findLanIPv4() {
        InetAddress fallback = null;
        try {
            Enumeration<NetworkInterface> interfaces = NetworkInterface.getNetworkInterfaces();
            while (interfaces != null && interfaces.hasMoreElements()) {
                NetworkInterface network = interfaces.nextElement();
                if (!network.isUp() || network.isLoopback()) {
                    continue;
                }
                Enumeration<InetAddress> addresses = network.getInetAddresses();
                while (addresses.hasMoreElements()) {
                    InetAddress address = addresses.nextElement();
                    if (!(address instanceof Inet4Address) || address.isLoopbackAddress()
                            || !address.isSiteLocalAddress()) {
                        continue;
                    }
                    if (network.getName().startsWith("wlan")) {
                        return address;
                    }
                    if (fallback == null) {
                        fallback = address;
                    }
                }
            }
        } catch (IOException error) {
            Log.w(TAG, "Unable to enumerate LAN interfaces", error);
        }
        return fallback;
    }

    private void updateNotification(String text) {
        NotificationManager manager = (NotificationManager)
                getSystemService(NOTIFICATION_SERVICE);
        if (manager != null) {
            manager.notify(NOTIFICATION_ID, buildNotification(text));
        }
    }

    private void closeServerSocket() {
        if (serverSocket != null) {
            try {
                serverSocket.close();
            } catch (IOException ignored) {
                // Already closing.
            }
        }
        serverSocket = null;
        boundAddress = null;
    }

    @Override
    public void onDestroy() {
        stopped = true;
        synchronized (serverLock) {
            closeServerSocket();
        }
        clients.shutdownNow();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
