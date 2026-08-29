package com.ming.lightprobe;

import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageInfo;
import android.content.pm.PackageManager;
import android.content.pm.Signature;
import android.net.Uri;
import android.os.Build;
import android.provider.Settings;

import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URI;
import java.net.URL;
import java.security.MessageDigest;
import java.util.HashSet;
import java.util.Locale;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.regex.Pattern;

/** Downloads GitHub release updates and verifies the artifact before opening Android's installer. */
final class AppUpdateManager {
    interface Listener {
        void onStatusChanged(String message, boolean busy);
        void onUpdateAvailable(Release release);
    }

    static final class Release {
        final int versionCode;
        final String versionName;
        final URL downloadUrl;
        final String sha256;
        final String signerSha256;
        final long sizeBytes;
        final String releaseNotes;

        Release(int versionCode, String versionName, URL downloadUrl, String sha256,
                String signerSha256, long sizeBytes, String releaseNotes) {
            this.versionCode = versionCode;
            this.versionName = versionName;
            this.downloadUrl = downloadUrl;
            this.sha256 = sha256;
            this.signerSha256 = signerSha256;
            this.sizeBytes = sizeBytes;
            this.releaseNotes = releaseNotes;
        }
    }

    private static final String MANIFEST_URL =
            "https://github.com/gmch1/junkyard/releases/latest/download/"
                    + "ble-lamp-remote-android.json";
    private static final String APK_MIME_TYPE = "application/vnd.android.package-archive";
    private static final long MAX_APK_BYTES = 64L * 1024L * 1024L;
    private static final int MAX_MANIFEST_BYTES = 64 * 1024;
    private static final Pattern HEX_256 = Pattern.compile("^[a-f0-9]{64}$");
    private static final Pattern RELEASE_APK_PATH = Pattern.compile(
            "^/gmch1/junkyard/releases/download/[^/]+/ble-lamp-remote-android\\.apk$");

    private final Activity activity;
    private final Listener listener;
    private final ExecutorService executor = Executors.newSingleThreadExecutor();
    private final AtomicBoolean busy = new AtomicBoolean(false);
    private final File updateDirectory;
    private volatile boolean closed;
    private volatile String status = "尚未检查更新";
    private volatile File pendingInstall;

    AppUpdateManager(Activity activity, Listener listener) {
        this.activity = activity;
        this.listener = listener;
        updateDirectory = new File(activity.getFilesDir(), "app-updates");
        executor.execute(new Runnable() {
            @Override
            public void run() {
                cleanupOldArtifacts();
            }
        });
    }

    String currentStatus() {
        return status;
    }

    String currentVersionName() {
        try {
            return installedPackage().versionName;
        } catch (Exception ignored) {
            return "未知";
        }
    }

    boolean checkForUpdates(final boolean manual) {
        if (closed || !busy.compareAndSet(false, true)) {
            return false;
        }
        updateStatus("正在检查更新…", true);
        executor.execute(new Runnable() {
            @Override
            public void run() {
                try {
                    Release release = fetchRelease();
                    long installed = versionCodeOf(installedPackage());
                    if (release.versionCode <= installed) {
                        finish(manual ? "当前已是最新版本" : "已是最新版本");
                        return;
                    }
                    busy.set(false);
                    updateStatus("发现新版本 v" + release.versionName, false);
                    activity.runOnUiThread(new Runnable() {
                        @Override
                        public void run() {
                            if (!closed) {
                                listener.onUpdateAvailable(release);
                            }
                        }
                    });
                } catch (Exception error) {
                    finish(manual ? readableError(error) : "自动检查更新失败");
                }
            }
        });
        return true;
    }

    boolean downloadAndInstall(final Release release) {
        if (closed || release == null || !busy.compareAndSet(false, true)) {
            return false;
        }
        updateStatus("准备下载 v" + release.versionName + "…", true);
        executor.execute(new Runnable() {
            @Override
            public void run() {
                try {
                    File artifact = download(release);
                    verifyPackage(artifact, release);
                    pendingInstall = artifact;
                    activity.runOnUiThread(new Runnable() {
                        @Override
                        public void run() {
                            requestInstallPermissionOrLaunch();
                        }
                    });
                } catch (Exception error) {
                    finish(readableError(error));
                }
            }
        });
        return true;
    }

    void resumeAfterInstallPermission() {
        if (pendingInstall == null || closed) {
            return;
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                && !activity.getPackageManager().canRequestPackageInstalls()) {
            return;
        }
        launchInstaller();
    }

    void close() {
        closed = true;
        executor.shutdownNow();
    }

    private Release fetchRelease() throws Exception {
        HttpURLConnection connection = openHttps(new URL(MANIFEST_URL),
                "application/json, application/octet-stream", 5);
        try {
            if (connection.getResponseCode() != HttpURLConnection.HTTP_OK) {
                throw new IOException("检查更新失败（HTTP "
                        + connection.getResponseCode() + "）");
            }
            byte[] payload = readLimited(connection.getInputStream(), MAX_MANIFEST_BYTES);
            JSONObject json = new JSONObject(new String(payload, "UTF-8"));
            if (!activity.getPackageName().equals(json.optString("applicationId"))) {
                throw new IOException("更新清单的应用标识不匹配");
            }
            int versionCode = json.getInt("versionCode");
            String versionName = json.getString("versionName").trim();
            String sha256 = json.getString("sha256").trim().toLowerCase(Locale.US);
            String signer = json.getString("signerSha256").trim().toLowerCase(Locale.US);
            long sizeBytes = json.getLong("sizeBytes");
            String notes = json.optString("releaseNotes", "").trim();
            URL downloadUrl = validateDownloadUrl(json.getString("downloadUrl"));
            if (versionCode < 1 || versionName.length() < 1 || versionName.length() > 64
                    || !HEX_256.matcher(sha256).matches()
                    || !HEX_256.matcher(signer).matches()
                    || sizeBytes < 1 || sizeBytes > MAX_APK_BYTES
                    || notes.length() > 2000) {
                throw new IOException("更新清单格式不正确");
            }
            if (!signerDigests(installedPackage()).contains(signer)) {
                throw new IOException("更新清单的签名证书不匹配");
            }
            return new Release(versionCode, versionName, downloadUrl, sha256,
                    signer, sizeBytes, notes);
        } finally {
            connection.disconnect();
        }
    }

    private URL validateDownloadUrl(String value) throws Exception {
        URI uri = new URI(value);
        if (!"https".equalsIgnoreCase(uri.getScheme())
                || !"github.com".equalsIgnoreCase(uri.getHost())
                || uri.getPort() != -1 || uri.getRawUserInfo() != null
                || uri.getRawQuery() != null || uri.getRawFragment() != null
                || !RELEASE_APK_PATH.matcher(uri.getRawPath()).matches()) {
            throw new IOException("更新包地址不受信任");
        }
        return uri.toURL();
    }

    private File download(Release release) throws Exception {
        if (!updateDirectory.exists() && !updateDirectory.mkdirs()) {
            throw new IOException("无法创建更新目录");
        }
        File artifact = new File(updateDirectory, artifactName(release));
        if (artifact.isFile() && artifact.length() == release.sizeBytes
                && sha256(artifact).equals(release.sha256)) {
            return artifact;
        }
        File partial = new File(updateDirectory, artifact.getName() + ".part");
        if (partial.exists() && !partial.delete()) {
            throw new IOException("无法清理旧的更新文件");
        }
        HttpURLConnection connection = openHttps(release.downloadUrl,
                APK_MIME_TYPE + ", application/octet-stream", 5);
        try {
            if (connection.getResponseCode() != HttpURLConnection.HTTP_OK) {
                throw new IOException("更新包下载失败（HTTP "
                        + connection.getResponseCode() + "）");
            }
            long declared = connection.getContentLengthLong();
            if (declared >= 0 && declared != release.sizeBytes) {
                throw new IOException("更新包大小与清单不一致");
            }
            long written = 0;
            long nextProgress = 0;
            try (InputStream input = connection.getInputStream();
                    FileOutputStream output = new FileOutputStream(partial)) {
                byte[] buffer = new byte[256 * 1024];
                while (true) {
                    if (closed || Thread.currentThread().isInterrupted()) {
                        throw new IOException("更新已取消");
                    }
                    int count = input.read(buffer);
                    if (count < 0) break;
                    if (count == 0) continue;
                    written += count;
                    if (written > release.sizeBytes) {
                        throw new IOException("更新包超过清单大小");
                    }
                    output.write(buffer, 0, count);
                    if (written >= nextProgress) {
                        int percent = (int) (written * 100L / release.sizeBytes);
                        updateStatus("正在下载更新… " + percent + "%", true);
                        nextProgress = written + 512 * 1024L;
                    }
                }
            } catch (Exception error) {
                partial.delete();
                throw error;
            }
            if (written != release.sizeBytes || !sha256(partial).equals(release.sha256)) {
                partial.delete();
                throw new IOException("更新包 SHA-256 校验失败");
            }
            if (artifact.exists() && !artifact.delete()) {
                partial.delete();
                throw new IOException("无法替换旧的更新文件");
            }
            if (!partial.renameTo(artifact)) {
                partial.delete();
                throw new IOException("无法保存更新文件");
            }
            return artifact;
        } finally {
            connection.disconnect();
        }
    }

    private void verifyPackage(File artifact, Release release) throws Exception {
        PackageManager manager = activity.getPackageManager();
        PackageInfo archive = packageInfoForArchive(manager, artifact);
        if (archive == null || !activity.getPackageName().equals(archive.packageName)) {
            artifact.delete();
            throw new IOException("更新包与当前应用不匹配");
        }
        if (versionCodeOf(archive) != release.versionCode) {
            artifact.delete();
            throw new IOException("更新包版本与清单不一致");
        }
        Set<String> archiveSigners = signerDigests(archive);
        Set<String> installedSigners = signerDigests(installedPackage());
        if (!archiveSigners.contains(release.signerSha256)
                || intersectionEmpty(archiveSigners, installedSigners)) {
            artifact.delete();
            throw new IOException("更新包签名与当前应用不一致");
        }
    }

    private void requestInstallPermissionOrLaunch() {
        if (closed || pendingInstall == null) return;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                && !activity.getPackageManager().canRequestPackageInstalls()) {
            updateStatus("请允许此应用安装更新", true);
            try {
                activity.startActivity(new Intent(
                        Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                        Uri.parse("package:" + activity.getPackageName())));
            } catch (Exception error) {
                finish("无法打开安装权限设置");
            }
            return;
        }
        launchInstaller();
    }

    private void launchInstaller() {
        File artifact = pendingInstall;
        if (artifact == null || !artifact.isFile() || closed) return;
        try {
            Uri uri = new Uri.Builder()
                    .scheme("content")
                    .authority(activity.getPackageName() + ".updates")
                    .appendPath(artifact.getName())
                    .build();
            Intent intent = new Intent(Intent.ACTION_VIEW)
                    .setDataAndType(uri, APK_MIME_TYPE)
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
            activity.startActivity(intent);
            pendingInstall = null;
            finish("已打开系统安装器");
        } catch (Exception error) {
            finish("无法打开系统安装器");
        }
    }

    private HttpURLConnection openHttps(URL initial, String accept, int redirects)
            throws Exception {
        URL current = initial;
        for (int i = 0; i <= redirects; i++) {
            if (!"https".equalsIgnoreCase(current.getProtocol())) {
                throw new IOException("更新连接必须使用 HTTPS");
            }
            HttpURLConnection connection = (HttpURLConnection) current.openConnection();
            connection.setInstanceFollowRedirects(false);
            connection.setConnectTimeout(15000);
            connection.setReadTimeout(60000);
            connection.setRequestProperty("Accept", accept);
            connection.setRequestProperty("Cache-Control", "no-cache");
            connection.setRequestProperty("User-Agent", "BLE-Lamp-Remote-Android");
            int statusCode = connection.getResponseCode();
            if (statusCode == 301 || statusCode == 302 || statusCode == 303
                    || statusCode == 307 || statusCode == 308) {
                String location = connection.getHeaderField("Location");
                connection.disconnect();
                if (location == null) throw new IOException("更新地址重定向无效");
                current = new URL(current, location);
                continue;
            }
            return connection;
        }
        throw new IOException("更新地址重定向过多");
    }

    private byte[] readLimited(InputStream input, int limit) throws IOException {
        try (InputStream source = input; ByteArrayOutputStream output = new ByteArrayOutputStream()) {
            byte[] buffer = new byte[8192];
            int total = 0;
            while (true) {
                int count = source.read(buffer);
                if (count < 0) break;
                total += count;
                if (total > limit) throw new IOException("更新清单过大");
                output.write(buffer, 0, count);
            }
            return output.toByteArray();
        }
    }

    private String sha256(File file) throws Exception {
        MessageDigest digest = MessageDigest.getInstance("SHA-256");
        try (FileInputStream input = new FileInputStream(file)) {
            byte[] buffer = new byte[256 * 1024];
            while (true) {
                int count = input.read(buffer);
                if (count < 0) break;
                if (count > 0) digest.update(buffer, 0, count);
            }
        }
        return toHex(digest.digest());
    }

    private PackageInfo installedPackage() throws PackageManager.NameNotFoundException {
        int flags = Build.VERSION.SDK_INT >= Build.VERSION_CODES.P
                ? PackageManager.GET_SIGNING_CERTIFICATES : PackageManager.GET_SIGNATURES;
        return activity.getPackageManager().getPackageInfo(activity.getPackageName(), flags);
    }

    @SuppressWarnings("deprecation")
    private PackageInfo packageInfoForArchive(PackageManager manager, File artifact) {
        int flags = Build.VERSION.SDK_INT >= Build.VERSION_CODES.P
                ? PackageManager.GET_SIGNING_CERTIFICATES : PackageManager.GET_SIGNATURES;
        return manager.getPackageArchiveInfo(artifact.getAbsolutePath(), flags);
    }

    @SuppressWarnings("deprecation")
    private Set<String> signerDigests(PackageInfo info) throws Exception {
        Signature[] signatures;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            if (info.signingInfo == null) return new HashSet<>();
            signatures = info.signingInfo.hasMultipleSigners()
                    ? info.signingInfo.getApkContentsSigners()
                    : info.signingInfo.getSigningCertificateHistory();
        } else {
            signatures = info.signatures;
        }
        Set<String> output = new HashSet<>();
        if (signatures == null) return output;
        for (Signature signature : signatures) {
            output.add(toHex(MessageDigest.getInstance("SHA-256")
                    .digest(signature.toByteArray())));
        }
        return output;
    }

    @SuppressWarnings("deprecation")
    private long versionCodeOf(PackageInfo info) {
        return Build.VERSION.SDK_INT >= Build.VERSION_CODES.P
                ? info.getLongVersionCode() : info.versionCode;
    }

    private boolean intersectionEmpty(Set<String> left, Set<String> right) {
        for (String value : left) {
            if (right.contains(value)) return false;
        }
        return true;
    }

    private String toHex(byte[] bytes) {
        StringBuilder output = new StringBuilder(bytes.length * 2);
        for (byte value : bytes) {
            output.append(String.format(Locale.US, "%02x", value & 0xff));
        }
        return output.toString();
    }

    private String artifactName(Release release) {
        return "ble-lamp-remote-update-" + release.versionCode + "-"
                + release.sha256.substring(0, 16) + ".apk";
    }

    private void cleanupOldArtifacts() {
        File[] files = updateDirectory.listFiles();
        if (files == null) return;
        for (File file : files) {
            if (file.getName().endsWith(".part") || !file.getName().endsWith(".apk")) {
                file.delete();
            }
        }
    }

    private String readableError(Exception error) {
        String message = error.getMessage();
        return message == null || message.trim().isEmpty() ? "检查更新失败" : message;
    }

    private void finish(String message) {
        busy.set(false);
        updateStatus(message, false);
    }

    private void updateStatus(final String message, final boolean isBusy) {
        status = message;
        activity.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                if (!closed) listener.onStatusChanged(message, isBusy);
            }
        });
    }
}
