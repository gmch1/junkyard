# Router Status Widget for macOS

一个轻量的 macOS 桌面路由器状态小组件。它每 5 秒通过 SSH 读取 OpenWrt 状态，不要求在路由器安装代理、插件或常驻脚本。

小组件位于桌面层：普通应用窗口会自然盖住它，点击桌面或使用“显示桌面”即可查看。卡片可拖动，菜单栏网络图标可以隐藏、显示、立即刷新或退出。

## 展示内容

- WAN 实时下载与上传速率
- WAN 物理链路协商速率，例如 `100 Mbps` 或 `1 Gbps`
- 路由器 CPU 使用率
- 内存使用率
- 路由器运行时间和 SSH 连接状态

在 RAX3000M/OpenWrt 25.12.5 上实测，应用物理内存约 17 MB，空闲 CPU 接近 0%。实际占用会随 macOS 版本变化。

## 要求

- macOS 13 或更高版本
- 本机具备 Apple Command Line Tools 或 Xcode
- 路由器运行 OpenWrt，并允许 SSH 登录
- macOS 当前用户已经配置到路由器的 SSH 公钥免密登录
- WAN 网卡提供 Linux sysfs 字节计数和链路速率文件

先确认下面的命令不会要求输入密码：

```bash
ssh -o BatchMode=yes root@192.168.31.1 'echo OK'
```

## 构建与运行

```bash
zsh build.sh
open build/RouterStatusWidget.app
```

生成的应用位于 `build/RouterStatusWidget.app`，构建脚本会执行本地 ad-hoc 签名。首次打开下载的 CI 构建产物时，macOS 可能要求在“隐私与安全性”中确认。

## 配置

默认配置适用于本项目最初使用的 RAX3000M：

| 配置 | 默认值 |
| --- | --- |
| SSH 目标 | `root@192.168.31.1` |
| WAN 接口 | `eth1` |
| 刷新周期 | 5 秒 |

修改 SSH 目标：

```bash
defaults write com.ming.RouterStatusWidget RouterHost 'root@192.168.31.1'
```

修改 WAN 接口：

```bash
defaults write com.ming.RouterStatusWidget WANInterface 'eth1'
```

修改后退出并重新启动应用。可在 OpenWrt 上使用下面的命令查找 WAN 对应设备：

```bash
ubus call network.interface.wan status
```

恢复默认配置：

```bash
defaults delete com.ming.RouterStatusWidget RouterHost
defaults delete com.ming.RouterStatusWidget WANInterface
```

## 数据与安全

- 应用仅启动系统自带的 `/usr/bin/ssh`，不保存 SSH 密码或私钥。
- 路由器读取命令仅访问 `/proc` 和 `/sys/class/net`，不会修改配置。
- 应用不连接互联网，不上传遥测数据。
- SSH 使用 `BatchMode=yes` 和 3 秒连接超时，无法免密登录时不会弹出密码输入框。

## 实现方式

项目使用纯 AppKit 和 Foundation 编写，没有 SwiftUI、WebView、Electron、Node.js 或第三方运行时。这样既能保持原生桌面小组件的外观，也能将实际物理内存控制在较低水平。

应用的数据流如下：

```text
macOS Timer（每 5 秒）
        │
        ▼
/usr/bin/ssh（BatchMode）
        │
        ▼
OpenWrt /proc 与 /sys/class/net
        │
        ▼
解析累计计数并计算 5 秒差值
        │
        ▼
AppKit 桌面卡片与菜单栏
```

核心实现分为四部分：

1. `RouterStatusApplication` 显式创建 `NSApplication` 并保持应用代理生命周期；
2. `StatusCardController` 创建无标题圆角卡片，并将窗口放在桌面图标层附近，因此普通应用窗口会盖住它；
3. `RouterMonitor` 每 5 秒启动一次系统 SSH，只读获取 CPU、内存、WAN 字节计数、物理链路速率和运行时间；
4. `Counters` 保存上一轮累计值，用相邻两轮差值计算 CPU 百分比和每秒上传、下载字节数。

### 指标来源

| 指标 | OpenWrt 数据源 | 计算方式 |
| --- | --- | --- |
| CPU | `/proc/stat` | 两次总计数和空闲计数的差值 |
| 内存 | `/proc/meminfo` | `(MemTotal - MemAvailable) / MemTotal` |
| 下载 | `/sys/class/net/<WAN>/statistics/rx_bytes` | 两次累计字节差值除以时间 |
| 上传 | `/sys/class/net/<WAN>/statistics/tx_bytes` | 两次累计字节差值除以时间 |
| WAN速率 | `/sys/class/net/<WAN>/speed` | 网卡PHY当前协商速率 |
| 运行时间 | `/proc/uptime` | 秒数转换为天、小时和分钟 |

### 为什么不在路由器安装服务

路由器端已经提供全部需要的内核统计数据。使用SSH按需读取可以避免安装额外软件包、开放新的监听端口或在路由器上增加常驻内存，同时复用用户现有的SSH密钥和主机指纹验证。

## 开发指南

### 开发原则

- 保持单一原生可执行文件，不引入非必要依赖；
- 路由器操作必须只读，禁止在轮询命令中修改UCI、网络或防火墙配置；
- 一轮刷新只建立一个SSH连接，一次取回全部指标；
- 所有可配置的接口名或SSH目标在拼接命令前必须经过格式校验；
- UI更新必须回到主线程；
- 构建缓存和`.app`产物不得提交Git；
- 修改后至少执行本地构建、Info.plist检查、签名检查和架构检查。

### 新增一个指标

1. 在 `RouterMonitor.poll()` 的远程只读命令中输出一个带固定前缀的数据行；
2. 在 `RouterMonitor.consume(_:)` 中解析该前缀，并处理缺失或格式错误；
3. 在 `StatusCardController` 中增加对应的文本控件；
4. 扩展 `StatusCardController.update(...)` 的参数并刷新控件；
5. 在上面的“指标来源”表中记录来源和算法；
6. 构建应用并确认SSH失败时仍能正常显示离线状态。

### 本地验证

```bash
zsh build.sh
plutil -lint build/RouterStatusWidget.app/Contents/Info.plist
codesign --verify --deep --strict build/RouterStatusWidget.app
file build/RouterStatusWidget.app/Contents/MacOS/RouterStatusWidget
```

在 Apple Silicon Mac 上，最后一条命令应包含：

```text
Mach-O 64-bit executable arm64
```

## CI/CD

仓库中的 `macos-router-status-widget.yml` 使用 GitHub Actions 的 ARM64 `macos-15` runner：

1. 编译应用；
2. 校验 app bundle 和代码签名；
3. 压缩并上传 `RouterStatusWidget-macos-arm64.zip` 构建产物；
4. 当推送 `router-status-v*` 标签时，自动创建 GitHub Release 并附加 ZIP。

发布示例：

```bash
git tag router-status-v1.0.0
git push origin router-status-v1.0.0
```

CI 构建采用 ad-hoc 签名，不等同于 Apple Developer ID 签名和公证。

## 目录结构

```text
junkyard/
├── .github/
│   └── workflows/
│       └── macos-router-status-widget.yml  # 本项目的构建与发布流水线
├── .gitignore                              # 忽略所有子项目的构建产物
├── LICENSE
├── README.md                               # 仓库项目索引和新增项目约定
└── macos-router-status-widget/
    ├── Info.plist                          # macOS App Bundle元数据
    ├── README.md                           # 使用、配置与开发文档
    ├── Sources/
    │   └── RouterStatusWidget.swift        # UI、SSH采集、解析和应用入口
    └── build.sh                            # 无Xcode工程的命令行构建脚本
```

本地构建后还会出现以下忽略目录：

```text
macos-router-status-widget/
├── .build/
│   └── module-cache/                       # Swift/Clang模块缓存
└── build/
    └── RouterStatusWidget.app/             # 可运行的应用Bundle
```

项目刻意不包含 `.xcodeproj`。`build.sh` 直接调用当前 Xcode/Command Line Tools 的 `swiftc`，因此源码、Bundle组装和CI使用同一个构建入口。
