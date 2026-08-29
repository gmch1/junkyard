# BLE Lamp Remote

可录制、可复用的本地 BLE 灯具遥控应用。Android 先从设备库选择已验证模板，或按引导录制一只实体遥控器；随后负责发送灯具实际响应的 BLE Manufacturer Advertising，并在可信局域网内提供固定动作 API。macOS 桌面卡片通过该 API 遥控灯具。

Android 与 macOS 的本地灯控链路不需要云服务、厂商账号或互联网连接；只有主动检查应用更新时会访问 GitHub。可选的巴法云桥接独立部署，用于把米家/小爱命令转发到同一套局域网 API：

```text
macOS 桌面卡片
        │  LAN HTTP + Bearer Token
        ▼
Android 常驻 API（仅绑定私有 IPv4）
        │  0x5556 BLE Advertising
        ▼
佛山照明吸顶灯
```

## 功能

- 开灯、关灯；
- 亮度增加、亮度降低；
- 色温偏暖、色温偏冷；
- 暖光/冷光与全亮/半亮各使用一个来回切换按钮，并排在同一行；
- 内置已实机验证的“佛山照明吸顶灯”配置；
- 新遥控器按固定流程录制，每个按键识别三次独立广播突发后自动进入下一项；
- 一次按压产生的几十份重复广播只计为一次，不需要用户点“下一步”；
- 已识别为 FanLamp V1 的录制结果可立即加入设备库，未知协议会保留原始样本等待后续适配；
- Android 本机触控和 macOS API 调用共享同一个持久化发送序号；
- 按键没有应用层队列或发送锁，新操作立即替换上一轮剩余广播；
- Android 页面可以复制 API 地址和 256 位随机 Token；
- macOS 卡片可从通用剪贴板导入地址和 Token。
- 已有可用遥控器时，Android 启动后直接进入控制页；
- Android 使用固定的“设备 / 遥控 / 设置”底部导航，控制页顶部只保留录制入口；
- LAN API 默认关闭，其地址、Token 和启停开关集中在设置页；
- Android 可从 GitHub Release 检查更新，安装前校验大小、SHA-256、包名、版本和签名证书。

灯具不会回传实际状态，因此两端都不会虚构当前开关、亮度百分比或色温数值。界面只显示网关是否在线以及最近被网关接受的动作。

## 目录

```text
ble-lamp-remote/
├── android/   # 无 Gradle 依赖的原生 Java APK、BLE 协议与 LAN API
├── macos/     # 纯 AppKit 桌面卡片
└── bemfa-bridge/ # 可选的巴法云 MQTT TLS → Android LAN API 桥接
```

巴法云桥接的配置、安全边界和 188 部署方式见 [`bemfa-bridge/README.md`](bemfa-bridge/README.md)。

协议抓包和逐字段验证记录见 [`android/CAPTURE_RESULTS.md`](android/CAPTURE_RESULTS.md)。

## Android 网关

### 要求

- Android 6.0 或更高版本；
- 支持多实例 BLE Advertising；
- Android SDK Build Tools 和 JDK 8+；
- 手机与 Mac 位于同一个可信 IPv4 局域网。

### 构建和安装

```bash
cd android
./ble-probe.sh install
```

多台 ADB 设备同时在线时：

```bash
BLE_PROBE_SERIAL=192.168.31.129:32931 ./ble-probe.sh install
```

APK 输出到 `android/build/ble-lamp-remote-debug.apk`。已有可用配置时，应用启动后直接进入遥控器；左上角返回设备列表，右上角可以进入设置或录制新遥控器。首次发送时需要授予“附近设备”权限。

局域网 API 默认关闭。需要 Mac 遥控时，在 Android 控制页右上角进入设置，手动启用“局域网遥控”；启用后前台服务才会启动并监听手机当前的私有 IPv4 地址。手机 Wi-Fi 地址变化后，重新打开应用即可让 API 绑定新地址。

### 自动录制

录制流程依次采集开启、关闭、亮度增加、亮度降低、色温偏暖、色温偏冷和全亮/半亮。进入页面后先保持遥控器静止约 2 秒，让应用排除附近持续广播的设备；随后每一项短按并松开三次，每次间隔约 1 秒。

应用会按 BLE 广播突发的起止时间判断独立按压，并对已知协议去掉发送序号、随机 seed、CRC 和密文差异后再比较动作。因此实体遥控器一次发出的多份重复包不会让进度虚增，第三次有效按压后页面会自动前进。没有对应按键时可以使用页面底部的跳过入口。

设置页启用局域网 API 后会显示当前地址，并提供：

- `复制地址`：复制类似 `http://192.168.31.129:8791` 的地址；
- `复制 Token`：复制首次启动随机生成的 64 位十六进制 Token。

Token 可以从诊断脚本轮换：

```bash
./ble-probe.sh rotate-api-token
```

轮换后 Mac 上的旧 Token 立即失效，需要重新复制和导入。

### 应用更新

设置页的“检查更新”会读取 GitHub 最新 Release 中的 `ble-lamp-remote-android.json`。下载 APK 后，应用会依次验证清单字段、文件大小、SHA-256、包名、版本号及签名证书；全部一致才会打开 Android 系统安装器。Android 8 及以上首次使用时还需要授权“安装未知应用”。

发布使用 `ble-lamp-remote-v<版本号>` 标签，例如：

```bash
git tag ble-lamp-remote-v1.3.0
git push origin ble-lamp-remote-v1.3.0
```

GitHub Actions 会从仓库 Secrets 恢复固定签名密钥，构建 Android APK 和 macOS App，并把 APK、更新清单、校验文件与 macOS 压缩包发布到同一个 GitHub Release。签名 Secret 名称为：

- `BLE_LAMP_ANDROID_KEYSTORE_BASE64`；
- `BLE_LAMP_ANDROID_KEYSTORE_PASSWORD`；
- `BLE_LAMP_ANDROID_KEY_ALIAS`；
- `BLE_LAMP_ANDROID_KEY_PASSWORD`。

## LAN API

所有接口均要求：

```text
Authorization: Bearer <64位十六进制Token>
```

| 方法 | 路径 | 动作 |
| --- | --- | --- |
| `GET` | `/v1/status` | 读取网关、蓝牙和发送序号状态 |
| `POST` | `/v1/light/on` | 开灯 |
| `POST` | `/v1/light/off` | 关灯 |
| `POST` | `/v1/light/brightness/up` | 亮度增加一级 |
| `POST` | `/v1/light/brightness/down` | 亮度降低一级 |
| `POST` | `/v1/light/temperature/warmer` | 色温偏暖一级 |
| `POST` | `/v1/light/temperature/cooler` | 色温偏冷一级 |
| `POST` | `/v1/light/preset/full` | 全亮 |
| `POST` | `/v1/light/preset/half` | 半亮 |
| `POST` | `/v1/light/preset/toggle` | 全亮/半亮交替（兼容旧客户端） |

状态验证示例：

```bash
curl \
  -H 'Authorization: Bearer YOUR_TOKEN' \
  http://192.168.31.129:8791/v1/status
```

成功的动作请求返回 `202 Accepted`。这表示 Android 网关已接受并开始广播，不代表灯具回传了执行结果。

## macOS 桌面卡片

### 要求

- macOS 13 或更高版本；
- Apple Command Line Tools 或 Xcode。

### 构建

```bash
cd macos
zsh build.sh
open build/LampRemoteWidget.app
```

卡片位于桌面图标层，不占 Dock 或菜单栏，也不会覆盖普通应用窗口。可以拖动位置；右键卡片可刷新、导入配置或退出。

### 首次配置

1. 在 Android 设置页启用“局域网遥控”，再点“复制地址”；
2. 等通用剪贴板同步到 Mac，右键卡片并选择“从剪贴板导入 API 地址”；
3. 在 Android 设置页点“复制 Token”；
4. 右键卡片并选择“从剪贴板导入 Token”。

如果不使用通用剪贴板，也可以手动配置：

```bash
defaults write io.github.gmch1.LampRemoteWidget APIURL \
  'http://192.168.31.129:8791'

mkdir -p "$HOME/Library/Application Support/LampRemoteWidget"
install -m 600 /path/to/api-token \
  "$HOME/Library/Application Support/LampRemoteWidget/api-token"
```

修改后重新启动卡片。

## 安全边界

- API 只绑定手机当前的 RFC1918 私有 IPv4，拒绝监听 `0.0.0.0`、公网 IPv4 和 IPv6；
- API 首次安装默认关闭，只有用户在设置页明确启用后才启动服务和端口监听；
- 所有端点都要求随机 256 位 Bearer Token，并使用常量时间比较；
- API 只接受上表中的固定动作，不提供原始 BLE 载荷、文件访问或任意命令执行；
- HTTP 请求行、Header 和 Body 均有严格大小限制，客户端连接有短超时；
- macOS Token 保存在权限为 `0600` 的文件中，不写入源码、URL、UserDefaults 或日志；
- 当前使用局域网明文 HTTP，Token 会在 LAN 内传输，只适合可信家庭网络；
- 不要做路由器端口转发，不要把端口 `8791` 暴露到互联网或访客网络。
- 更新只接受本仓库的 HTTPS GitHub Release 地址，并在安装前校验 APK 的哈希、身份、版本和签名；
- Android 签名私钥只保存在本机和 GitHub Actions Secrets，不进入仓库或构建日志。

如局域网中存在不可信客户端，应在前面增加带证书固定的 HTTPS 反向代理或改用组网 VPN。

## 已验证

- Android APK 本地构建和签名校验；
- Xiaomi M2104K10AC / Android 13 安装与前台常驻；
- Android 1.3.0 启动直达控制页，开关、调光与两个模式切换使用三行紧凑按钮；
- 设备、遥控与设置三个底部导航项可在真机上切换，当前页使用高亮指示；
- LAN API 默认关闭，设置页启停时 `8791` 与前台服务同步出现或消失；
- 无 Token 请求返回 `401`；
- 正确 Token 的 `/v1/status` 返回 `200`；
- 独立的全亮、半亮端点返回 `202` 并成功启动 BLE 广播；
- 未列入白名单的动作返回 `404`；
- API 仅绑定手机 Wi-Fi 地址 `192.168.31.129:8791`。

macOS 构建由 GitHub Actions 在 macOS runner 上执行；本地 Linux 开发机没有 AppKit SDK。
