import AppKit
import CoreGraphics
import Foundation

private struct Counters {
    let cpuTotal: UInt64
    let cpuIdle: UInt64
    let rxBytes: UInt64
    let txBytes: UInt64
    let sampledAt: Date
}

private struct APISnapshot: Decodable {
    let version: Int
    let cpuTotal: UInt64
    let cpuIdle: UInt64
    let memTotalKb: Double
    let memAvailableKb: Double
    let rxBytes: UInt64
    let txBytes: UInt64
    let linkMbps: Int
    let uptimeSeconds: Double
    let temperatureC: Double?
}

private final class LightGlassCardView: NSVisualEffectView {
    private let roundedMask = CAShapeLayer()

    override init(frame frameRect: NSRect) {
        super.init(frame: frameRect)
        material = .underWindowBackground
        blendingMode = .behindWindow
        state = .active
        appearance = NSAppearance(named: .aqua)
        wantsLayer = true
        layer?.backgroundColor = NSColor.white.withAlphaComponent(0.05).cgColor
        layer?.cornerRadius = 26
        layer?.borderWidth = 0.75
        layer?.borderColor = NSColor.white.withAlphaComponent(0.46).cgColor
        layer?.masksToBounds = true
        roundedMask.fillColor = NSColor.white.cgColor
        layer?.mask = roundedMask
    }

    required init?(coder: NSCoder) { nil }

    override func layout() {
        super.layout()
        roundedMask.frame = bounds
        roundedMask.path = CGPath(
            roundedRect: bounds,
            cornerWidth: 26,
            cornerHeight: 26,
            transform: nil
        )
        maskImage = NSImage(size: bounds.size, flipped: false) { rect in
            NSColor.white.setFill()
            NSBezierPath(roundedRect: rect, xRadius: 26, yRadius: 26).fill()
            return true
        }
    }
}

private final class StatusCardController: NSWindowController {
    private let statusDot = NSView(frame: NSRect(x: 0, y: 0, width: 8, height: 8))
    private let statusLabel = NSTextField(labelWithString: "正在连接…")
    private let downloadValue = NSTextField(labelWithString: "--")
    private let uploadValue = NSTextField(labelWithString: "--")
    private let cpuValue = NSTextField(labelWithString: "--")
    private let memoryValue = NSTextField(labelWithString: "--")
    private let temperatureValue = NSTextField(labelWithString: "--")
    private let wanValue = NSTextField(labelWithString: "--")
    private let rebootButton = NSButton()
    var onRebootRequested: (() -> Void)?

    init() {
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 360, height: 192),
            styleMask: [.borderless],
            backing: .buffered,
            defer: false
        )
        let desktopLevel = CGWindowLevelForKey(.desktopIconWindow)
        panel.level = NSWindow.Level(rawValue: Int(desktopLevel) + 1)
        panel.isReleasedWhenClosed = false
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.isMovableByWindowBackground = true
        panel.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
        panel.hidesOnDeactivate = false
        super.init(window: panel)
        buildContent(in: panel)
    }

    required init?(coder: NSCoder) { nil }

    private func buildContent(in panel: NSPanel) {
        let cardView = LightGlassCardView()

        statusDot.wantsLayer = true
        statusDot.layer?.cornerRadius = 4
        statusDot.layer?.backgroundColor = NSColor.systemOrange.cgColor
        statusDot.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            statusDot.widthAnchor.constraint(equalToConstant: 8),
            statusDot.heightAnchor.constraint(equalToConstant: 8)
        ])

        let heading = NSTextField(labelWithString: "RAX3000M")
        heading.font = .systemFont(ofSize: 17, weight: .semibold)
        heading.textColor = .labelColor

        statusLabel.font = .systemFont(ofSize: 10.5, weight: .medium)
        statusLabel.textColor = .secondaryLabelColor

        let status = NSStackView(views: [statusDot, statusLabel])
        status.orientation = .horizontal
        status.alignment = .centerY
        status.spacing = 6

        rebootButton.image = NSImage(
            systemSymbolName: "arrow.clockwise",
            accessibilityDescription: "重启路由器"
        )
        rebootButton.symbolConfiguration = NSImage.SymbolConfiguration(pointSize: 12, weight: .medium)
        rebootButton.isBordered = false
        rebootButton.contentTintColor = .secondaryLabelColor
        rebootButton.toolTip = "重启路由器"
        rebootButton.target = self
        rebootButton.action = #selector(requestReboot)
        rebootButton.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            rebootButton.widthAnchor.constraint(equalToConstant: 22),
            rebootButton.heightAnchor.constraint(equalToConstant: 22)
        ])

        let headerSpacer = NSView()
        headerSpacer.setContentHuggingPriority(.defaultLow, for: .horizontal)
        let header = NSStackView(views: [heading, headerSpacer, status, rebootButton])
        header.orientation = .horizontal
        header.alignment = .centerY
        header.distribution = .fill
        header.spacing = 7
        heading.setContentHuggingPriority(.required, for: .horizontal)
        status.setContentHuggingPriority(.required, for: .horizontal)

        let rates = NSStackView(views: [
            makeRateTile(title: "实时下载", arrow: "↓", value: downloadValue),
            makeRateTile(title: "实时上传", arrow: "↑", value: uploadValue)
        ])
        rates.orientation = .horizontal
        rates.alignment = .height
        rates.distribution = .fillEqually
        rates.spacing = 10

        let metrics = NSStackView(views: [
            makeSmallTile(title: "CPU", value: cpuValue),
            makeSmallTile(title: "内存", value: memoryValue),
            makeSmallTile(title: "温度", value: temperatureValue),
            makeSmallTile(title: "WAN", value: wanValue)
        ])
        metrics.orientation = .horizontal
        metrics.alignment = .height
        metrics.distribution = .fillEqually
        metrics.spacing = 8

        let content = NSStackView(views: [header, rates, metrics])
        content.orientation = .vertical
        content.alignment = .width
        content.spacing = 9
        content.translatesAutoresizingMaskIntoConstraints = false

        cardView.addSubview(content)
        panel.contentView = cardView
        NSLayoutConstraint.activate([
            content.leadingAnchor.constraint(equalTo: cardView.leadingAnchor, constant: 16),
            content.trailingAnchor.constraint(equalTo: cardView.trailingAnchor, constant: -16),
            content.topAnchor.constraint(equalTo: cardView.topAnchor, constant: 14),
            content.bottomAnchor.constraint(equalTo: cardView.bottomAnchor, constant: -13)
        ])
    }

    private func makeRateTile(title: String, arrow: String, value: NSTextField) -> NSView {
        let tile = NSView()
        tile.wantsLayer = true
        tile.layer?.backgroundColor = NSColor.white.withAlphaComponent(0.10).cgColor
        tile.layer?.borderWidth = 0.5
        tile.layer?.borderColor = NSColor.white.withAlphaComponent(0.28).cgColor
        tile.layer?.cornerRadius = 18

        let caption = NSTextField(labelWithString: "\(arrow)  \(title)")
        caption.font = .systemFont(ofSize: 12, weight: .semibold)
        caption.textColor = NSColor(calibratedRed: 0.18, green: 0.36, blue: 0.50, alpha: 1)
        value.font = .monospacedDigitSystemFont(ofSize: 24, weight: .bold)
        value.textColor = .labelColor

        let stack = NSStackView(views: [caption, value])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 5
        stack.translatesAutoresizingMaskIntoConstraints = false
        tile.addSubview(stack)
        NSLayoutConstraint.activate([
            tile.heightAnchor.constraint(equalToConstant: 76),
            stack.leadingAnchor.constraint(equalTo: tile.leadingAnchor, constant: 14),
            stack.trailingAnchor.constraint(lessThanOrEqualTo: tile.trailingAnchor, constant: -10),
            stack.centerYAnchor.constraint(equalTo: tile.centerYAnchor)
        ])
        return tile
    }

    private func makeSmallTile(title: String, value: NSTextField) -> NSView {
        let tile = NSView()
        tile.wantsLayer = true
        tile.layer?.backgroundColor = NSColor.white.withAlphaComponent(0.09).cgColor
        tile.layer?.borderWidth = 0.5
        tile.layer?.borderColor = NSColor.white.withAlphaComponent(0.26).cgColor
        tile.layer?.cornerRadius = 14

        let caption = NSTextField(labelWithString: title)
        caption.font = .systemFont(ofSize: 10, weight: .medium)
        caption.textColor = .secondaryLabelColor
        value.font = .monospacedDigitSystemFont(ofSize: 14, weight: .semibold)
        value.textColor = .labelColor

        let stack = NSStackView(views: [caption, value])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 2
        stack.translatesAutoresizingMaskIntoConstraints = false
        tile.addSubview(stack)
        NSLayoutConstraint.activate([
            tile.heightAnchor.constraint(equalToConstant: 50),
            stack.leadingAnchor.constraint(equalTo: tile.leadingAnchor, constant: 11),
            stack.trailingAnchor.constraint(lessThanOrEqualTo: tile.trailingAnchor, constant: -8),
            stack.centerYAnchor.constraint(equalTo: tile.centerYAnchor)
        ])
        return tile
    }

    func placeAtTopRight() {
        guard let screen = NSScreen.main, let window else { return }
        let frame = window.frame
        let visible = screen.visibleFrame
        window.setFrameOrigin(NSPoint(
            x: visible.maxX - frame.width - 18,
            y: visible.maxY - frame.height - 18
        ))
    }

    func setConnected(_ connected: Bool, message: String) {
        statusDot.layer?.backgroundColor = (connected ? NSColor.systemGreen : NSColor.systemRed).cgColor
        statusLabel.stringValue = message
        if connected { rebootButton.isEnabled = true }
    }

    func setPaused() {
        statusDot.layer?.backgroundColor = NSColor.systemGray.cgColor
        statusLabel.stringValue = "后台暂停"
    }

    func setConnecting() {
        statusDot.layer?.backgroundColor = NSColor.systemOrange.cgColor
        statusLabel.stringValue = "正在连接…"
    }

    func setRebooting() {
        rebootButton.isEnabled = false
        statusDot.layer?.backgroundColor = NSColor.systemOrange.cgColor
        statusLabel.stringValue = "正在重启…"
    }

    func setRebootFailed(_ message: String) {
        rebootButton.isEnabled = true
        statusDot.layer?.backgroundColor = NSColor.systemRed.cgColor
        statusLabel.stringValue = message
    }

    @objc private func requestReboot() { onRebootRequested?() }

    func update(downloadRate: Double, uploadRate: Double, cpuPercent: Double,
                memoryPercent: Double, memoryUsed: Double, memoryTotal: Double,
                linkSpeed: Int, uptimeSeconds: Double, temperatureC: Double?) {
        downloadValue.stringValue = Self.rate(downloadRate)
        uploadValue.stringValue = Self.rate(uploadRate)
        cpuValue.stringValue = String(format: "%.1f%%", cpuPercent)
        memoryValue.stringValue = String(format: "%.1f%%", memoryPercent)
        memoryValue.toolTip = String(format: "已使用 %.0f MB，共 %.0f MB", memoryUsed, memoryTotal)
        temperatureValue.stringValue = temperatureC.map { String(format: "%.1f°C", $0) } ?? "--"
        wanValue.stringValue = Self.compactLink(linkSpeed)
        statusLabel.stringValue = "在线 · 运行 \(Self.uptime(uptimeSeconds))"
    }

    private static func rate(_ bytesPerSecond: Double) -> String {
        if bytesPerSecond >= 1_000_000 {
            return String(format: "%.1f MB/s", bytesPerSecond / 1_000_000)
        }
        if bytesPerSecond >= 1_000 {
            return String(format: "%.1f KB/s", bytesPerSecond / 1_000)
        }
        return String(format: "%.0f B/s", bytesPerSecond)
    }

    private static func link(_ speed: Int) -> String {
        if speed >= 1000 { return speed == 1000 ? "1 Gbps" : "\(speed / 1000) Gbps" }
        return speed > 0 ? "\(speed) Mbps" : "--"
    }

    private static func compactLink(_ speed: Int) -> String {
        if speed >= 1000 { return speed == 1000 ? "1G" : "\(speed / 1000)G" }
        return speed > 0 ? "\(speed)M" : "--"
    }

    private static func uptime(_ seconds: Double) -> String {
        let totalMinutes = Int(seconds) / 60
        let days = totalMinutes / 1440
        let hours = (totalMinutes % 1440) / 60
        let minutes = totalMinutes % 60
        if days > 0 { return "\(days)天 \(hours)小时" }
        if hours > 0 { return "\(hours)小时 \(minutes)分" }
        return "\(minutes)分钟"
    }
}

private final class RouterMonitor: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    private let endpoint: URL
    private let token: String?
    private let controlSession: URLSession
    private let card: StatusCardController
    private lazy var streamSession: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.timeoutIntervalForRequest = 20
        configuration.timeoutIntervalForResource = 3_700
        configuration.httpMaximumConnectionsPerHost = 1
        return URLSession(configuration: configuration, delegate: self, delegateQueue: .main)
    }()
    private var streamTask: URLSessionDataTask?
    private var streamBuffer = Data()
    private var shouldReceiveEvents = false
    private var reconnectWorkItem: DispatchWorkItem?
    private var workspaceObservers: [NSObjectProtocol] = []
    private var previous: Counters?
    private var rebooting = false
    var onRateUpdate: ((Double, Double) -> Void)?

    init(card: StatusCardController) {
        self.card = card
        let defaults = UserDefaults.standard
        let defaultURL = URL(string: "http://192.168.31.1:8099/cgi-bin/status")!
        if let configured = defaults.string(forKey: "APIURL"),
           let url = URL(string: configured),
           ["http", "https"].contains(url.scheme?.lowercased() ?? ""),
           url.host != nil {
            self.endpoint = url
        } else {
            self.endpoint = defaultURL
        }

        let supportDirectory = FileManager.default.urls(
            for: .applicationSupportDirectory,
            in: .userDomainMask
        ).first
        let tokenURL = supportDirectory?
            .appendingPathComponent("RouterStatusWidget", isDirectory: true)
            .appendingPathComponent("api-token", isDirectory: false)
        self.token = tokenURL
            .flatMap { try? String(contentsOf: $0, encoding: .utf8) }
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .flatMap { $0.isEmpty ? nil : $0 }

        let configuration = URLSessionConfiguration.ephemeral
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.timeoutIntervalForRequest = 4
        configuration.timeoutIntervalForResource = 5
        configuration.httpMaximumConnectionsPerHost = 2
        self.controlSession = URLSession(configuration: configuration)
        super.init()
    }

    deinit {
        reconnectWorkItem?.cancel()
        workspaceObservers.forEach(NSWorkspace.shared.notificationCenter.removeObserver)
        streamSession.invalidateAndCancel()
        controlSession.invalidateAndCancel()
    }

    func start() {
        let center = NSWorkspace.shared.notificationCenter
        let names: [Notification.Name] = [
            NSWorkspace.didActivateApplicationNotification,
            NSWorkspace.activeSpaceDidChangeNotification,
            NSWorkspace.didWakeNotification
        ]
        workspaceObservers = names.map { name in
            center.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
                self?.reevaluateWorkspaceSoon()
            }
        }
        workspaceObservers.append(
            center.addObserver(
                forName: NSWorkspace.willSleepNotification,
                object: nil,
                queue: .main
            ) { [weak self] _ in
                self?.suspendEventStream()
            }
        )
        updateStreamingState()
    }

    func refresh() {
        if isDesktopActive {
            restartEventStream()
        } else {
            fetchOneSnapshot()
        }
    }

    private var isDesktopActive: Bool {
        guard let identifier = NSWorkspace.shared.frontmostApplication?.bundleIdentifier else {
            return false
        }
        return identifier == "com.apple.finder" || identifier == Bundle.main.bundleIdentifier
    }

    private func reevaluateWorkspaceSoon() {
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) { [weak self] in
            self?.updateStreamingState()
        }
    }

    private func updateStreamingState() {
        if isDesktopActive {
            shouldReceiveEvents = true
            startEventStream()
        } else {
            suspendEventStream()
        }
    }

    private func startEventStream() {
        guard shouldReceiveEvents, streamTask == nil else { return }
        guard let token else {
            card.setConnected(false, message: "缺少API Token")
            return
        }

        reconnectWorkItem?.cancel()
        reconnectWorkItem = nil
        streamBuffer.removeAll(keepingCapacity: true)
        previous = nil

        let eventsEndpoint = endpoint
            .deletingLastPathComponent()
            .appendingPathComponent("events", isDirectory: false)
        var request = URLRequest(url: eventsEndpoint)
        request.httpMethod = "GET"
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 20
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")

        card.setConnecting()
        let task = streamSession.dataTask(with: request)
        streamTask = task
        task.resume()
    }

    private func suspendEventStream() {
        shouldReceiveEvents = false
        reconnectWorkItem?.cancel()
        reconnectWorkItem = nil
        let task = streamTask
        streamTask = nil
        task?.cancel()
        streamBuffer.removeAll(keepingCapacity: false)
        previous = nil
        card.setPaused()
    }

    private func restartEventStream() {
        shouldReceiveEvents = true
        let task = streamTask
        streamTask = nil
        task?.cancel()
        streamBuffer.removeAll(keepingCapacity: true)
        previous = nil
        startEventStream()
    }

    private func scheduleReconnect() {
        guard shouldReceiveEvents else { return }
        reconnectWorkItem?.cancel()
        let workItem = DispatchWorkItem { [weak self] in self?.startEventStream() }
        reconnectWorkItem = workItem
        DispatchQueue.main.asyncAfter(deadline: .now() + 2, execute: workItem)
    }

    func urlSession(
        _ session: URLSession,
        dataTask: URLSessionDataTask,
        didReceive response: URLResponse,
        completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
    ) {
        guard dataTask === streamTask else {
            completionHandler(.cancel)
            return
        }
        guard let http = response as? HTTPURLResponse else {
            streamTask = nil
            card.setConnected(false, message: "SSE响应异常")
            completionHandler(.cancel)
            scheduleReconnect()
            return
        }
        guard http.statusCode == 200 else {
            streamTask = nil
            card.setConnected(false, message: http.statusCode == 401 ? "Token无效" : "SSE错误 \(http.statusCode)")
            completionHandler(.cancel)
            scheduleReconnect()
            return
        }
        completionHandler(.allow)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        guard dataTask === streamTask else { return }
        streamBuffer.append(data)
        let separator = Data([0x0A, 0x0A])
        while let range = streamBuffer.range(of: separator) {
            let eventData = streamBuffer.subdata(in: streamBuffer.startIndex..<range.lowerBound)
            streamBuffer.removeSubrange(streamBuffer.startIndex..<range.upperBound)
            consumeEvent(eventData)
        }
    }

    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        didCompleteWithError error: Error?
    ) {
        guard task === streamTask else { return }
        streamTask = nil
        streamBuffer.removeAll(keepingCapacity: true)
        previous = nil
        if shouldReceiveEvents {
            card.setConnecting()
            scheduleReconnect()
        }
    }

    private func consumeEvent(_ eventData: Data) {
        guard let event = String(data: eventData, encoding: .utf8) else { return }
        let payload = event
            .split(separator: "\n", omittingEmptySubsequences: false)
            .map { String($0).trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { $0.hasPrefix("data:") }
            .map { String($0.dropFirst(5)).trimmingCharacters(in: .whitespaces) }
            .joined(separator: "\n")
        guard !payload.isEmpty,
              let data = payload.data(using: .utf8),
              let snapshot = decodeSnapshot(data) else { return }
        consume(snapshot)
    }

    private func fetchOneSnapshot() {
        guard let token else {
            card.setConnected(false, message: "缺少API Token")
            return
        }
        var request = URLRequest(url: endpoint)
        request.httpMethod = "GET"
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 4
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        controlSession.dataTask(with: request) { [weak self] data, response, error in
            DispatchQueue.main.async {
                guard let self else { return }
                guard error == nil,
                      let http = response as? HTTPURLResponse,
                      http.statusCode == 200,
                      let data,
                      let snapshot = self.decodeSnapshot(data) else {
                    self.card.setConnected(false, message: "刷新失败")
                    return
                }
                self.consume(snapshot)
            }
        }.resume()
    }

    private func decodeSnapshot(_ data: Data) -> APISnapshot? {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        guard let snapshot = try? decoder.decode(APISnapshot.self, from: data),
              snapshot.version == 1,
              snapshot.memTotalKb > 0 else { return nil }
        return snapshot
    }

    func reboot() {
        guard !rebooting else { return }
        guard let token else {
            card.setRebootFailed("缺少API Token")
            return
        }

        let rebootEndpoint = endpoint
            .deletingLastPathComponent()
            .appendingPathComponent("reboot", isDirectory: false)
        var request = URLRequest(url: rebootEndpoint)
        request.httpMethod = "POST"
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 4
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = Data(#"{"action":"reboot"}"#.utf8)

        rebooting = true
        card.setRebooting()
        controlSession.dataTask(with: request) { [weak self] _, response, error in
            DispatchQueue.main.async {
                guard let self else { return }
                if error != nil {
                    self.rebooting = false
                    self.card.setRebootFailed("重启请求失败")
                    return
                }
                guard let http = response as? HTTPURLResponse, http.statusCode == 202 else {
                    self.rebooting = false
                    self.card.setRebootFailed("重启接口拒绝")
                    return
                }
                self.previous = nil
                self.card.setRebooting()
            }
        }.resume()
    }

    private func consume(_ snapshot: APISnapshot) {
        rebooting = false
        let now = Date()
        let current = Counters(
            cpuTotal: snapshot.cpuTotal,
            cpuIdle: snapshot.cpuIdle,
            rxBytes: snapshot.rxBytes,
            txBytes: snapshot.txBytes,
            sampledAt: now
        )
        var cpuPercent = 0.0
        var downloadRate = 0.0
        var uploadRate = 0.0

        if let previous {
            let totalDelta = current.cpuTotal >= previous.cpuTotal ? current.cpuTotal - previous.cpuTotal : 0
            let idleDelta = current.cpuIdle >= previous.cpuIdle ? current.cpuIdle - previous.cpuIdle : 0
            if totalDelta > 0 {
                cpuPercent = Double(totalDelta - min(totalDelta, idleDelta)) / Double(totalDelta) * 100
            }
            let elapsed = max(now.timeIntervalSince(previous.sampledAt), 0.1)
            if current.rxBytes >= previous.rxBytes {
                downloadRate = Double(current.rxBytes - previous.rxBytes) / elapsed
            }
            if current.txBytes >= previous.txBytes {
                uploadRate = Double(current.txBytes - previous.txBytes) / elapsed
            }
        }
        self.previous = current

        let usedKB = snapshot.memTotalKb - snapshot.memAvailableKb
        let memoryPercent = usedKB / snapshot.memTotalKb * 100
        card.setConnected(true, message: "SSE已连接")
        card.update(
            downloadRate: downloadRate,
            uploadRate: uploadRate,
            cpuPercent: cpuPercent,
            memoryPercent: memoryPercent,
            memoryUsed: usedKB / 1024,
            memoryTotal: snapshot.memTotalKb / 1024,
            linkSpeed: snapshot.linkMbps,
            uptimeSeconds: snapshot.uptimeSeconds,
            temperatureC: snapshot.temperatureC
        )
        onRateUpdate?(downloadRate, uploadRate)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var card: StatusCardController!
    private var monitor: RouterMonitor!
    private var statusItem: NSStatusItem!

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        card = StatusCardController()
        card.placeAtTopRight()
        card.showWindow(nil)
        card.window?.orderFrontRegardless()

        buildStatusMenu()
        monitor = RouterMonitor(card: card)
        card.onRebootRequested = { [weak self] in self?.monitor.reboot() }
        monitor.onRateUpdate = { [weak self] down, up in
            self?.statusItem.button?.title = String(format: " ↓%.1f ↑%.1f", down / 1_000_000, up / 1_000_000)
        }
        monitor.start()
    }

    private func buildStatusMenu() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = statusItem.button {
            button.image = NSImage(systemSymbolName: "network", accessibilityDescription: "路由器状态")
            button.imagePosition = .imageLeading
            button.title = " 连接中"
        }

        let menu = NSMenu()
        menu.addItem(withTitle: "显示/隐藏状态卡", action: #selector(toggleCard), keyEquivalent: "")
        menu.addItem(withTitle: "立即刷新", action: #selector(refresh), keyEquivalent: "r")
        menu.addItem(.separator())
        menu.addItem(withTitle: "退出", action: #selector(quit), keyEquivalent: "q")
        menu.items.forEach { $0.target = self }
        statusItem.menu = menu
    }

    @objc private func toggleCard() {
        guard let window = card.window else { return }
        if window.isVisible { window.orderOut(nil) }
        else { window.orderFrontRegardless() }
    }

    @objc private func refresh() { monitor.refresh() }
    @objc private func quit() { NSApp.terminate(nil) }
}

@main
enum RouterStatusApplication {
    static func main() {
        let application = NSApplication.shared
        let delegate = AppDelegate()
        application.delegate = delegate
        application.run()
        withExtendedLifetime(delegate) {}
    }
}
