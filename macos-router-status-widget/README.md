# Router Status Widget for macOS

一个轻量的 macOS 桌面路由器状态小组件。它通过局域网 Token API 建立 SSE 连接，由 OpenWrt 按需推送状态。只要组件在任意显示器上仍然可见，就保持 1 秒更新；所有显示器上都被遮挡后，采样频率会逐级降低，持续遮挡 15 分钟后才完全停止。组件以浅色毛玻璃卡片展示 WAN 实时速率、物理协商速率、CPU、内存、温度、运行时间，以及局域网设备的双向实时流量，并提供受限的路由器重启按钮。

小组件位于桌面层，普通应用窗口会自然盖住它，不会一直悬浮在最前面。点击桌面或使用“显示桌面”即可查看；菜单栏网络图标可以隐藏、显示、立即刷新或退出。

![Router Status Widget](assets/router-status-widget.png)

在 RAX3000M/OpenWrt 25.12.5 与 macOS 26 上实测，应用物理内存约 20 MB，空闲 CPU 接近 0%。实际占用会随系统版本变化。

## 展示内容

- WAN 实时下载和上传速率
- 固定顺序的设备流量表格，包含设备名及双向实时速度，超出区域时可滚动查看
- 自动隐藏 `yeelink`、`yeelight`、`chuangmi`、`lumi` 等明显的物联网设备名称
- WAN 物理链路协商速率，例如 `100 Mbps` 或 `1 Gbps`
- 路由器 CPU、内存使用率和芯片温度
- 路由器运行时间和 API 连接状态
- 通过右上角图标请求路由器重启
- 根据窗口在所有显示器上的真实遮挡状态自适应 SSE 采样频率

## 架构选择

OpenWrt 已经提供 `rpcd + uhttpd-mod-ubus` JSON-RPC API，但官方接口需要先登录取得有时效的 `ubus_rpc_session`，并通过 rpcd ACL 授权。这个项目只需要少量状态数据和单一的重启动作，因此没有让桌面应用持有 OpenWrt root 密码或广泛的 ubus 权限。

项目安装一个独立的最小 uhttpd 实例：

- 仅绑定指定的 LAN IPv4 地址，默认 `192.168.31.1:8099`；
- 使用随机的 256 位固定 Bearer Token；
- 状态接口只接受 `GET /cgi-bin/status`；
- 事件接口只接受 `GET /cgi-bin/events`，并以 SSE 推送只读快照；
- 重启接口只接受 `POST /cgi-bin/reboot` 和精确的 `{"action":"reboot"}` 请求体；
- 只读取 `/proc`、`/sys/class/net`、`/sys/class/thermal`、DHCP/邻居表和 `nlbwmon` 计数；
- 除固定调用 `/sbin/reboot` 外，不提供 UCI、任意命令执行、文件写入、关机或其他系统控制能力；
- 不监听公网 IPv6，也不要求新增 WAN 防火墙规则。

## 要求

- macOS 13 或更高版本
- Apple Command Line Tools 或 Xcode
- OpenWrt 路由器已经安装 `uhttpd`
- OpenWrt 路由器已经安装并启用 `nlbwmon`
- 安装API时可以通过SSH管理路由器；小组件日常运行不使用SSH
- WAN接口提供Linux sysfs字节计数和链路速率文件

## 安装

以下示例使用本项目默认值：

| 配置 | 默认值 |
| --- | --- |
| 路由器LAN地址 | `192.168.31.1` |
| API端口 | `8099` |
| WAN接口 | `eth1` |
| 可见时SSE推送周期 | 1秒 |
| 可见时设备流量采样周期 | 3秒 |

### 1. 生成Token

在Mac执行：

```bash
openssl rand -hex -out /private/tmp/router-status-api.token 32
chmod 600 /private/tmp/router-status-api.token
```

Token只保存在Mac和路由器，不得提交Git。

### 2. 安装OpenWrt API

```bash
scp -O \
  router/openwrt/router-status-api.cgi \
  router/openwrt/router-events-api.cgi \
  router/openwrt/router-client-counters.sh \
  router/openwrt/router-reboot-api.cgi \
  router/openwrt/install-router-status-api.sh \
  /private/tmp/router-status-api.token \
  root@192.168.31.1:/tmp/

ssh root@192.168.31.1 '
ROUTER_STATUS_LAN_IP=192.168.31.1 \
ROUTER_STATUS_API_PORT=8099 \
ROUTER_STATUS_WAN_INTERFACE=eth1 \
sh /tmp/install-router-status-api.sh
'
```

安装脚本会：

1. 备份 `/etc/config/uhttpd`；
2. 安装状态、SSE、设备计数、重启 CGI 和600权限Token；
3. 创建独立 `uhttpd.router_status` 实例；
4. 仅监听 `192.168.31.1:8099`；
5. 重启uhttpd使配置生效；
6. 保存 `nlbwmon` 原刷新周期并将其改为3秒；卸载时自动恢复原值。

### 3. 安装Mac Token

```bash
mkdir -p "$HOME/Library/Application Support/RouterStatusWidget"
install -m 600 \
  /private/tmp/router-status-api.token \
  "$HOME/Library/Application Support/RouterStatusWidget/api-token"
```

### 4. 构建并运行

```bash
zsh build.sh
open build/RouterStatusWidget.app
```

生成的应用位于 `build/RouterStatusWidget.app`，构建脚本会执行本地 ad-hoc 签名。首次打开下载的CI构建产物时，macOS可能要求在“隐私与安全性”中确认。

## 验证API

无Token请求应返回 `401 Unauthorized`：

```bash
curl -i http://192.168.31.1:8099/cgi-bin/status
```

带正确Token应返回 `200 OK`：

```bash
curl -i \
  -H "Authorization: Bearer $(tr -d '\n' < "$HOME/Library/Application Support/RouterStatusWidget/api-token")" \
  http://192.168.31.1:8099/cgi-bin/status
```

响应格式：

```json
{
  "version": 1,
  "cpu_total": 6837377,
  "cpu_idle": 6701119,
  "mem_total_kb": 496700,
  "mem_available_kb": 378928,
  "rx_bytes": 7545671984,
  "tx_bytes": 1024043164,
  "link_mbps": 100,
  "uptime_seconds": 34299,
  "temperature_c": 42.8,
  "clients_sampled_at": 34299,
  "clients": [
    {
      "mac": "bc:24:11:a3:21:b6",
      "name": "nas2.nnnest.fun",
      "ip": "192.168.31.199",
      "rx_bytes": 16862395514,
      "tx_bytes": 3587443550
    }
  ]
}
```

SSE 是只读接口，可以用下面的命令短暂验证。`interval` 只允许 `1`、`5`、`15`、`30` 或 `60`；`--max-time 3` 会在收到约三帧后主动断开，路由器端 CGI 随连接一起退出：

```bash
curl -sN --max-time 3 \
  -H "Authorization: Bearer $(tr -d '\n' < "$HOME/Library/Application Support/RouterStatusWidget/api-token")" \
  -H 'Accept: text/event-stream' \
  'http://192.168.31.1:8099/cgi-bin/events?interval=1'
```

### 多显示器与自适应频率

应用不再用“当前聚焦的应用”推测组件是否可见，而是读取 `NSWindow.occlusionState`。只要组件在任意显示器上还有可见区域，就继续实时更新；只有所有显示器上都完全看不到组件时才开始降频：

| 状态 | SSE采样间隔 |
| --- | --- |
| 任意显示器可见 | 1秒 |
| 完全遮挡不足1分钟 | 15秒 |
| 完全遮挡1至5分钟 | 30秒 |
| 完全遮挡5至15分钟 | 60秒 |
| 完全遮挡超过15分钟 | 断开SSE并停止采样 |

组件重新在任意显示器上可见时，会立即恢复1秒连接。降频由路由器端执行，Mac不会接收后再丢弃多余数据。单次 SSE 最长保持10分钟，随后由Mac自动重连，避免异常断网留下永久占用的CGI进程。

重启接口需要有效 Token、`POST` 方法和精确请求体。应用收到 `202 Accepted` 后，路由器才会在短暂延时后调用 `/sbin/reboot`。不要为了验证接口而手动执行下面的有效请求；它会真实重启路由器：

```text
POST /cgi-bin/reboot
Authorization: Bearer <token>
Content-Type: application/json

{"action":"reboot"}
```

## Mac配置

修改API地址：

```bash
defaults write com.ming.RouterStatusWidget APIURL \
  'http://192.168.31.1:8099/cgi-bin/status'
```

恢复默认地址：

```bash
defaults delete com.ming.RouterStatusWidget APIURL
```

修改后退出并重新启动应用。

## 数据与安全边界

- Mac应用使用 `URLSession`，不启动SSH进程，也不保存路由器密码或私钥。
- SSE 根据窗口在全部显示器上的真实遮挡状态自适应降频；休眠时立即取消连接。
- Token文件必须保持600权限，仓库的 `.gitignore` 不替代凭证管理。
- 状态API只读取 `/proc`、`/sys/class/net`、`/sys/class/thermal`、DHCP/邻居表和 `nlbwmon` 计数，不会修改防火墙或网络配置。
- 重启API只能调用 `/sbin/reboot`；代码中不包含 `poweroff`、`halt` 或任意命令参数。
- 应用不连接互联网、不上传遥测数据。
- 当前API使用局域网HTTP，Token会在LAN内传输；这适用于可信家庭网络，但不适合访客网络、公共网络或跨互联网使用。
- 任何取得Token的客户端都可以请求重启，因此不要共享Token，也不要将API端口暴露到WAN。
- 若局域网中存在不可信客户端，应改用HTTPS并在Mac端固定证书，或继续使用官方rpcd ACL与短期会话。
- 不要把API实例绑定到 `0.0.0.0`、`[::]` 或WAN地址。

### NAND写入与寿命

运行时状态不得持续写入 NAND。OpenWrt 中 `/var` 指向 tmpfs `/tmp`，因此本项目保持以下边界：

- `nlbwmon` 数据库使用 `/var/lib/nlbwmon`，实际位于 `/tmp/lib/nlbwmon`；
- `collectd`/RRD（若启用）应使用 `/tmp/rrd`；
- 系统日志保留在 `logd` 内存环形缓冲区，不配置 `/overlay` 下的持久化日志文件；
- SSE、状态API和Mac组件不写运行日志，不执行周期性 `uci commit`；
- Token、API脚本和UCI配置只在安装、升级或卸载时写入一次；
- 安装脚本仅在 `nlbwmon` 刷新周期确实需要改变时才提交配置，重复安装不会为相同值再次写入。

不要把 `nlbwmon.database_directory`、RRD目录或系统日志路径改到 `/overlay`、`/root` 或 `/etc`。这样设备流量历史会在路由器重启后丢失，但可避免为非关键监控数据消耗 NAND 擦写寿命。

## 实现方式

项目使用纯AppKit和Foundation编写，没有SwiftUI、WebView、Electron、Node.js或第三方运行时。

```text
macOS 多显示器窗口遮挡状态
        │
        ├── 任意屏幕可见：1秒 SSE
        └── 全部屏幕遮挡：15秒 → 30秒 → 60秒 → 停止
                    │
                    ▼
URLSession + Authorization: Bearer（单一长连接）
        │
        ▼
仅绑定LAN的OpenWrt uhttpd CGI
        │
        ▼
/proc、/sys/class 与 nlbwmon 的自适应只读采样
        │
        ▼
计算相邻快照差值
        │
        ▼
AppKit浅色毛玻璃桌面卡片与菜单栏
```

核心实现分为七部分：

1. `RouterStatusApplication` 显式创建 `NSApplication` 并保持应用代理生命周期；
2. `StatusCardController` 创建轻量的浅色毛玻璃卡片并放在桌面图标层；
3. `RouterMonitor` 监听窗口遮挡、Space 和显示器变化，按需调整 SSE 间隔或取消连接；
4. `Counters` 保存上一轮累计值，用相邻快照计算 CPU 百分比和每秒速率。
5. 独立重启 CGI 只接受固定动作，并在返回 `202 Accepted` 后调用 `/sbin/reboot`。
6. SSE CGI 只接受 1、5、15、30、60 秒五档采样间隔，断开后立即退出；
7. 设备计数脚本每3秒按 MAC 聚合一次 `nlbwmon` 数据，中间 SSE 帧复用缓存；Mac 根据独立的设备采样时间计算实时速率，设备按首次发现顺序固定展示，新设备只追加到底部。

### 指标来源

| 指标 | OpenWrt数据源 | 计算方式 |
| --- | --- | --- |
| CPU | `/proc/stat` | 两次总计数和空闲计数的差值 |
| 内存 | `/proc/meminfo` | `(MemTotal - MemAvailable) / MemTotal` |
| 下载 | `/sys/class/net/<WAN>/statistics/rx_bytes` | 两次累计字节差值除以时间 |
| 上传 | `/sys/class/net/<WAN>/statistics/tx_bytes` | 两次累计字节差值除以时间 |
| WAN速率 | `/sys/class/net/<WAN>/speed` | 网卡PHY当前协商速率 |
| 运行时间 | `/proc/uptime` | 秒数转换为天、小时和分钟 |
| 温度 | `/sys/class/thermal/thermal_zone*/temp` | 优先读取 CPU thermal zone 并换算为摄氏度 |
| 设备上下行 | `nlbw -g mac`、DHCP租约与邻居表 | 按MAC聚合累计字节，取相邻快照差值；设备名优先使用DHCP/DNS名称 |

## 开发指南

### 开发原则

- 保持单一原生Mac可执行文件，不引入非必要依赖；
- 状态API保持只读；系统操作必须使用独立端点、固定动作和严格请求校验；
- 只保持一个 SSE 连接；不可见时必须逐级降频，深度后台必须断开，不得使用固定HTTP轮询；
- Token不得写入源码、Info.plist、UserDefaults、日志、URL或Git；
- API地址和WAN接口等输入在使用前必须校验；
- UI更新必须回到主线程；
- 构建缓存和`.app`产物不得提交Git；
- 修改后至少执行本地构建、CGI语法、Info.plist、签名和架构检查。

### 新增一个指标

1. 在 `router-status-api.cgi` 中读取新的只读数据并加入版本化JSON；
2. 在 `APISnapshot` 中添加对应字段；
3. 在 `RouterMonitor.consume(_:)` 中计算或转换指标；
4. 在 `StatusCardController` 中增加控件并更新显示；
5. 更新“指标来源”和API响应示例；
6. 验证无Token、错误Token、路由器离线和字段缺失场景。

### 本地验证

```bash
sh -n router/openwrt/router-status-api.cgi
sh -n router/openwrt/router-events-api.cgi
sh -n router/openwrt/router-client-counters.sh
sh -n router/openwrt/router-reboot-api.cgi
sh -n router/openwrt/install-router-status-api.sh
sh -n router/openwrt/uninstall-router-status-api.sh
zsh build.sh
plutil -lint build/RouterStatusWidget.app/Contents/Info.plist
codesign --verify --deep --strict build/RouterStatusWidget.app
file build/RouterStatusWidget.app/Contents/MacOS/RouterStatusWidget
```

Apple Silicon构建应包含：

```text
Mach-O 64-bit executable arm64
```

## 卸载

```bash
scp -O router/openwrt/uninstall-router-status-api.sh root@192.168.31.1:/tmp/
ssh root@192.168.31.1 'sh /tmp/uninstall-router-status-api.sh'
rm "$HOME/Library/Application Support/RouterStatusWidget/api-token"
```

卸载脚本只删除本项目的uhttpd实例、API文件和Token，不影响LuCI主实例。

## CI/CD

仓库中的 `macos-router-status-widget.yml` 使用GitHub Actions ARM64 `macos-15` runner：

1. 检查六个OpenWrt shell/CGI脚本语法；
2. 编译Mac应用；
3. 校验app bundle、Info.plist、代码签名和架构；
4. 压缩并上传 `RouterStatusWidget-macos-arm64.zip`；
5. 推送 `router-status-v*` 标签时自动创建GitHub Release。

```bash
git tag router-status-v1.0.0
git push origin router-status-v1.0.0
```

CI构建采用ad-hoc签名，不等同于Apple Developer ID签名和公证。

## 目录结构

```text
junkyard/
├── .github/
│   └── workflows/
│       └── macos-router-status-widget.yml
├── .gitignore
├── LICENSE
├── README.md
└── macos-router-status-widget/
    ├── assets/
    │   └── router-status-widget.png
    ├── router/
    │   └── openwrt/
    │       ├── install-router-status-api.sh
    │       ├── router-client-counters.sh
    │       ├── router-events-api.cgi
    │       ├── router-reboot-api.cgi
    │       ├── router-status-api.cgi
    │       └── uninstall-router-status-api.sh
    ├── Sources/
    │   └── RouterStatusWidget.swift
    ├── Info.plist
    ├── README.md
    └── build.sh
```

本地构建会额外生成被Git忽略的 `.build/module-cache/` 和 `build/RouterStatusWidget.app/`。项目刻意不包含 `.xcodeproj`，本地与CI统一通过 `build.sh` 调用当前Xcode/Command Line Tools中的 `swiftc`。
