import AppKit

/// 菜单栏入口：小图标 + 下拉菜单
/// 结构：状态 → 防休眠模式（父项显示当前值，悬停展开）→ 按键统计 → 操作
/// 菜单打开期间每秒刷新动态指标（下次保活/倒计时/计数）
final class MenuBarController: NSObject, NSMenuDelegate {
    private let statusItem: NSStatusItem
    private let engine: KeepAliveEngine
    private let monitor: KeyboardMonitor
    private let stats: StatsStore
    private let menu = NSMenu()

    private let batteryLine = NSMenuItem(title: "…", action: nil, keyEquivalent: "")
    private let statsLine = NSMenuItem(title: "…", action: nil, keyEquivalent: "")
    private let axHint = NSMenuItem(title: "⚠️ 按键计数未授权——点此开启", action: #selector(axAction), keyEquivalent: "")
    private let btHint = NSMenuItem(title: "⚠️ 无蓝牙权限——点此开启", action: #selector(btAction), keyEquivalent: "")
    private let modeRoot = NSMenuItem(title: "防休眠", action: nil, keyEquivalent: "")
    private var modeItems: [NSMenuItem] = []
    private let loginItem = NSMenuItem(title: "登录时启动", action: #selector(loginAction), keyEquivalent: "")
    private var openTimer: DispatchSourceTimer?
    private var titleTimer: DispatchSourceTimer?

    private static let modeOptions: [(title: String, mode: KeepAliveEngine.KeepAwakeMode)] = [
        ("持续防休眠", .continuous),
        ("限时 30 分钟", .timed(1800)),
        ("限时 1 小时", .timed(3600)),
        ("限时 2 小时", .timed(7200)),
        ("限时 4 小时", .timed(14400)),
        ("关闭（键盘自然休眠）", .off),
    ]

    init(engine: KeepAliveEngine, monitor: KeyboardMonitor, stats: StatsStore) {
        self.engine = engine
        self.monitor = monitor
        self.stats = stats

        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)

        super.init()

        if let button = statusItem.button {
            button.image = NSImage(systemSymbolName: "keyboard", accessibilityDescription: "键盘保活")
            button.image?.size = NSSize(width: 16, height: 16)
        }

        menu.autoenablesItems = false
        menu.delegate = self

        // ── 信息区（两行描述，置顶）──
        batteryLine.isEnabled = false
        menu.addItem(batteryLine)
        statsLine.isEnabled = false
        menu.addItem(statsLine)
        axHint.target = self
        menu.addItem(axHint)
        btHint.target = self
        menu.addItem(btHint)
        menu.addItem(.separator())

        // ── 操作区 ──
        let sub = NSMenu()
        for (i, opt) in Self.modeOptions.enumerated() {
            let item = NSMenuItem(title: opt.title, action: #selector(modeAction(_:)), keyEquivalent: "")
            item.target = self
            item.tag = i
            modeItems.append(item)
            sub.addItem(item)
        }
        modeRoot.submenu = sub
        menu.addItem(modeRoot)

        loginItem.target = self
        menu.addItem(loginItem)

        let logItem = NSMenuItem(title: "打开日志文件夹", action: #selector(logsAction), keyEquivalent: "")
        logItem.target = self
        menu.addItem(logItem)
        menu.addItem(.separator())

        let quitItem = NSMenuItem(title: "退出 KbKeepAlive", action: #selector(quitAction), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)

        statusItem.menu = menu

        engine.onStateChange = { [weak self] in self?.refreshTitle() }
        engine.onBattery = { [weak self] _ in self?.refreshTitle() }
        refreshTitle()

        // 低频兜底刷新标题：限时倒计时分钟数在菜单关闭期间也能走动
        // （每 10 秒一次，只改标题文本，开销可忽略）
        let tt = DispatchSource.makeTimerSource(queue: .main)
        tt.schedule(deadline: .now() + 10, repeating: 10)
        tt.setEventHandler { [weak self] in self?.refreshTitle() }
        tt.resume()
        titleTimer = tt
    }

    // MARK: - 菜单栏标题（常驻状态）

    private func refreshTitle() {
        guard let button = statusItem.button else { return }
        switch engine.mode {
        case .off:
            button.title = "⏹"
        case .continuous:
            button.title = engine.linkState == .ready ? (engine.battery >= 0 ? "\(engine.battery)%" : "…") : "⚠︎"
        case .timed:
            let m = Int(engine.timedRemaining ?? 0) / 60
            button.title = engine.linkState == .ready ? "\(m)分" : "⚠︎"
        }
    }

    // MARK: - 刷新（打开时全量；打开期间每秒刷新动态行）

    func menuNeedsUpdate(_ menu: NSMenu) {
        // 计数授权可能在运行中被授予/撤销：打开菜单时顺手重建 tap（自愈入口）
        if !monitor.isCounting { _ = monitor.start() }
        refreshDynamic()
        modeRoot.title = "防休眠：\(currentModeName)"
        for item in modeItems where item.tag >= 0 && item.tag < Self.modeOptions.count {
            item.state = Self.modeOptions[item.tag].mode == engine.mode ? .on : .off
        }
        axHint.isHidden = monitor.isCounting
        btHint.isHidden = engine.linkState != .unauthorized
        loginItem.state = LoginItem.isEnabled ? .on : .off
        refreshTitle()
        startOpenTimer()
    }

    func menuDidClose(_ menu: NSMenu) {
        openTimer?.cancel()
        openTimer = nil
    }

    /// 随时间变化的行：状态（保活节奏）/ 按键统计
    private func refreshDynamic() {
        var prefix = ""
        switch engine.mode {
        case .off: prefix = "已关闭 · "
        case .timed:
            if let r = engine.timedRemaining { prefix = "剩余 \(fmtCountdown(r)) · " }
        case .continuous: break
        }
        if engine.linkState == .ready {
            let next = engine.mode == .off ? "已停发" : (engine.secondsUntilNextRead.map { "\($0) 秒后" } ?? "—")
            batteryLine.title = "\(prefix)下次保活 \(next) · 已保活 \(engine.readSuccessCount) 次"
        } else {
            batteryLine.title = "\(prefix)\(engine.linkState.rawValue)"
        }
        statsLine.title = "今日输入 \(stats.todayCount.formatted()) 次 · 累计 \(stats.totalCount.formatted()) 次"
    }

    /// 父项标题里展示的当前模式名
    private var currentModeName: String {
        switch engine.mode {
        case .continuous:
            return "持续防休眠"
        case .off:
            return "已关闭"
        case .timed(let d):
            let min = Int(d / 60)
            if min >= 60 && min % 60 == 0 { return "限时 \(min / 60) 小时" }
            return "限时 \(min) 分钟"
        }
    }

    private func startOpenTimer() {
        guard openTimer == nil else { return }
        let t = DispatchSource.makeTimerSource(queue: .main)
        t.schedule(deadline: .now() + 1, repeating: 1)
        t.setEventHandler { [weak self] in self?.refreshDynamic() }
        t.resume()
        openTimer = t
    }

    private func fmtCountdown(_ ti: TimeInterval) -> String {
        let s = Int(ti)
        if s >= 3600 { return String(format: "%d:%02d:%02d", s / 3600, (s % 3600) / 60, s % 60) }
        return String(format: "%02d:%02d", s / 60, s % 60)
    }

    // MARK: - 动作

    @objc private func modeAction(_ sender: NSMenuItem) {
        guard sender.tag >= 0, sender.tag < Self.modeOptions.count else { return }
        engine.setMode(Self.modeOptions[sender.tag].mode)
    }

    @objc private func loginAction() {
        LoginItem.setEnabled(!LoginItem.isEnabled)
    }

    @objc private func logsAction() {
        NSWorkspace.shared.open(AppLogger.shared.dirURL)
    }

    @objc private func axAction() {
        monitor.requestAuthorization()
        DispatchQueue.main.asyncAfter(deadline: .now() + 1) { self.monitor.openAccessibilitySettings() }
    }

    @objc private func btAction() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Bluetooth") {
            NSWorkspace.shared.open(url)
        }
    }

    @objc private func quitAction() {
        NSApp.terminate(nil)
    }
}
