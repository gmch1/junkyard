import AppKit

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var stats: StatsStore!
    private var engine: KeepAliveEngine!
    private var monitor: KeyboardMonitor!
    private var menuBar: MenuBarController!
    private var housekeepingTimer: DispatchSourceTimer?
    private var authPollTimer: DispatchSourceTimer?
    private var authPollCount = 0
    private var lastSelfCheck = Date.distantPast
    private var keepAwakeActivity: (any NSObjectProtocol)?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let ver = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
        let os = ProcessInfo.processInfo.operatingSystemVersion
        AppLogger.shared.info("=== KbKeepAlive v\(ver) 启动（macOS \(os.majorVersion).\(os.minorVersion)）===")

        // 防 App Nap：保证保活定时器不被系统合并/延后（菜单栏无窗口应用的理论风险）
        keepAwakeActivity = ProcessInfo.processInfo.beginActivity(options: [.userInitiated], reason: "BLE keep-alive polling")

        stats = StatsStore()

        engine = KeepAliveEngine()
        engine.start()

        monitor = KeyboardMonitor(stats: stats)
        if !monitor.start() {
            AppLogger.shared.warn("辅助功能权限未生效，按键计数暂停；已开始每 5 秒重试（上限 30 分钟，打开菜单时也会自动重试）")
            scheduleAuthPoll()
            // 弹一次系统授权引导（每次启动最多一次）
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { self.monitor.requestAuthorization() }
        }

        menuBar = MenuBarController(engine: engine, monitor: monitor, stats: stats)

        // 单一低频心跳：30 秒落盘统计 + 每小时自检日志（内存/保活/重连快照）
        let t = DispatchSource.makeTimerSource(queue: .main)
        t.schedule(deadline: .now() + 30, repeating: 30)
        t.setEventHandler { [weak self] in self?.housekeeping() }
        t.resume()
        housekeepingTimer = t

        AppLogger.shared.info("启动自检 | footprint \(currentFootprintBytes() / 1_048_576) MB | 累计按键 \(stats.totalCount.formatted()) 次")
    }

    func applicationWillTerminate(_ notification: Notification) {
        stats.flush(force: true)
        AppLogger.shared.info("=== 退出 ===")
        AppLogger.shared.flushAndWait()   // 等日志队列写完再返回，避免最后一条丢失
        if let a = keepAwakeActivity { ProcessInfo.processInfo.endActivity(a) }
    }

    /// 授权后自动开启计数，无需重启：反复尝试创建事件监听（不信 AXIsProcessTrusted 的滞后状态）。
    /// 10 分钟上限防永久轮询；之后靠打开菜单时的重试自愈
    private func scheduleAuthPoll() {
        let t = DispatchSource.makeTimerSource(queue: .main)
        t.schedule(deadline: .now() + 5, repeating: 5)
        t.setEventHandler { [weak self] in
            guard let self else { return }
            if self.monitor.start() {
                self.authPollTimer?.cancel()
                self.authPollTimer = nil
                return
            }
            self.authPollCount += 1
            if self.authPollCount >= 360 {
                self.authPollTimer?.cancel()
                self.authPollTimer = nil
                AppLogger.shared.warn("辅助功能授权 30 分钟未生效，停止轮询（打开菜单时会自动重试）")
            }
        }
        t.resume()
        authPollTimer = t
    }

    private func housekeeping() {
        stats.flush()
        guard Date().timeIntervalSince(lastSelfCheck) >= 3600 else { return }
        lastSelfCheck = Date()
        AppLogger.shared.info("自检 | footprint \(currentFootprintBytes() / 1_048_576) MB | 今日按键 \(stats.todayCount.formatted()) | 累计 \(stats.totalCount.formatted()) | 保活发出 \(engine.readAttemptCount) · 成功 \(engine.readSuccessCount) · 重连 \(engine.reconnectCount) | \(engine.statusText)")
    }
}
