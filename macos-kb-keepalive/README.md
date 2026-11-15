# macos-kb-keepalive

一个原生 macOS 菜单栏小工具，解决廉价 BLE 蓝牙键盘（无厂商配置工具）"闲置几分钟就休眠"的问题，顺带做按键统计。

## 背景

针对的键盘：**M150K-1**（YiChip 方案，BLE 连接，复用 Samsung VID 0x04E8 / PID 0x7021）。

排查结论（2026-11）：

- HID 报告描述符：标准 键盘/多媒体/鼠标 报告，**无 Feature Report 配置接口**
- GATT：仅 Device Information + Battery 服务，**无厂商配置服务**，HID 服务被系统占用
- 固件休眠阈值烧死在 YiChip 固件里，无 USB 数据口（仅充电），无法刷机

因此采用 **host 侧保活**：通过 CoreBluetooth 维持连接，每 60 秒读一次 Battery Level（0x2A19），用周期性的主机→键盘流量重置固件的休眠计时器。整夜实测 845+ 次保活无一断连。

## 功能

- **防休眠模式**：持续防休眠 / 限时防休眠（30 分钟 ~ 4 小时，到期自动放手）/ 关闭，菜单栏平铺切换
- **按键统计**：今日/累计按键数（CGEventTap 只计数，不记录按键内容），菜单栏实时显示
- **日志**：`~/Library/Logs/KbKeepAlive/app.log`（512KB 自动轮转），统计持久化在 `~/Library/Application Support/KbKeepAlive/stats.json`
- **其他**：菜单栏电量显示、断连自动重连、登录自启（SMAppService，回退 LaunchAgent）

## 构建与运行

```zsh
swift make-icon.swift            # 可选：重新生成图标（需 iconutil 打包）
iconutil -c icns AppIcon.iconset -o AppIcon.icns
./build.sh                       # 产出 build/KbKeepAlive.app
cp -R build/KbKeepAlive.app ~/Applications/
open ~/Applications/KbKeepAlive.app
```

图标（`make-icon.swift`，Swift/CoreGraphics 矢量绘制）：深色底 + 白色键盘 + 绿色盾牌心跳线，寓意"键盘保活守护"。

需要两个系统授权（仅首次）：

1. 蓝牙
2. 辅助功能（用于按键计数）

## 签名说明

build.sh 默认固定使用钥匙串中的 **Apple Development** 证书签名（证书 CN 写死在脚本里，可用环境变量 `IDENTITY` 覆盖）——系统授权（蓝牙/辅助功能）绑定证书 CN，因此每次重新构建、重新安装后授权**保持有效**。固定证书不可用时回退"第一张 Apple Development"并警告；都没有才 ad-hoc 签名（授权会在下次构建时失效）。

## 可靠性设计（v1.1，经全量代码 review 修订）

- **看门狗**：连接/服务发现 12 秒、保活读 8 秒超时——回调丢失、链路假死都会主动重置重连，不会"静默假保活"（日志里"读请求已发出"和"✓"永远成对出现）
- **重连**：可取消、可合并的重连任务 + 指数退避（2s→60s 封顶），蓝牙状态抖动有去抖，陈旧回调按 peripheral UUID 防串扰
- **三级吸附**：持久化 peripheral UUID 直连 → 系统已连 HID 列表（每秒零开销轮询，按键唤醒后 ~1 秒吸附）→ 主动扫描（20 秒窗口 + 退避控噪；注意这键盘广播不带服务 UUID，扫描不能按服务过滤）
- **`.off` 真正放手**：停扫、断连、清引用，键盘可自然入睡
- 细节见 [CODE_REVIEW.md](CODE_REVIEW.md)

## 内存设计

菜单栏常驻 **phys_footprint ~10 MB**（不随时间增长；`ps` 里的 ~40MB RSS 大部分是 AppKit/CoreBluetooth 共享只读页）：计数回调 O(1) 且日期 key 带缓存；日志 512KB×2 上限，写失败降级 os_log；统计每 30 秒按需落盘，写失败保留脏标记重试，损坏文件先备份再重置；UI 仅在菜单打开时每秒刷新，平时 10 秒低频刷标题。
