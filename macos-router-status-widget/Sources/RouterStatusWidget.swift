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
    let clientsSampledAt: Double?
    let clients: [APIClientCounter]?
}

private struct APIClientCounter: Decodable {
    let mac: String
    let name: String
    let ip: String
    let rxBytes: UInt64
    let txBytes: UInt64
}

private struct ClientRate {
    let mac: String
    let name: String
    let ip: String
    let downloadRate: Double
    let uploadRate: Double
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

private final class StatusCardController: NSWindowController, NSTableViewDataSource, NSTableViewDelegate {
    private let statusDot = NSView(frame: NSRect(x: 0, y: 0, width: 8, height: 8))
    private let statusLabel = NSTextField(labelWithString: "正在连接…")
    private let downloadValue = NSTextField(labelWithString: "--")
    private let uploadValue = NSTextField(labelWithString: "--")
    private let cpuValue = NSTextField(labelWithString: "--")
    private let memoryValue = NSTextField(labelWithString: "--")
    private let temperatureValue = NSTextField(labelWithString: "--")
    private let wanValue = NSTextField(labelWithString: "--")
    private let rebootButton = NSButton()
    private let trafficTable = NSTableView()
    private var latestClientRates: [ClientRate] = []
    private var clientOrder: [String] = []
    private var clientRatesByMAC: [String: ClientRate] = [:]
    var onRebootRequested: (() -> Void)?

    init() {
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 360, height: 348),
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

        let traffic = makeTrafficTable()

        let content = NSStackView(views: [header, rates, metrics, traffic])
        content.orientation = .vertical
        content.alignment = .width
        content.spacing = 9
        content.translatesAutoresizingMaskIntoConstraints = false

        cardView.addSubview(content)
        panel.contentView = cardView
        NSLayoutConstraint.activate([
            traffic.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            traffic.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            content.leadingAnchor.constraint(equalTo: cardView.leadingAnchor, constant: 16),
            content.trailingAnchor.constraint(equalTo: cardView.trailingAnchor, constant: -16),
            content.topAnchor.constraint(equalTo: cardView.topAnchor, constant: 14),
            content.bottomAnchor.constraint(equalTo: cardView.bottomAnchor, constant: -13)
        ])
    }

    private func makeTrafficTable() -> NSView {
        let container = NSView()
        container.wantsLayer = true
        container.layer?.backgroundColor = NSColor.white.withAlphaComponent(0.075).cgColor
        container.layer?.borderWidth = 0.5
        container.layer?.borderColor = NSColor.white.withAlphaComponent(0.25).cgColor
        container.layer?.cornerRadius = 16

        let deviceColumn = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("device"))
        deviceColumn.title = ""
        deviceColumn.width = 136
        deviceColumn.minWidth = 110
        let downloadColumn = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("download"))
        downloadColumn.title = ""
        downloadColumn.width = 72
        downloadColumn.minWidth = 64
        let uploadColumn = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("upload"))
        uploadColumn.title = ""
        uploadColumn.width = 72
        uploadColumn.minWidth = 64

        trafficTable.addTableColumn(deviceColumn)
        trafficTable.addTableColumn(downloadColumn)
        trafficTable.addTableColumn(uploadColumn)
        trafficTable.dataSource = self
        trafficTable.delegate = self
        trafficTable.headerView = nil
        trafficTable.style = .plain
        trafficTable.backgroundColor = .clear
        trafficTable.selectionHighlightStyle = .none
        trafficTable.rowHeight = 19
        trafficTable.intercellSpacing = NSSize(width: 6, height: 1)
        trafficTable.columnAutoresizingStyle = .firstColumnOnlyAutoresizingStyle
        trafficTable.autoresizingMask = [.width]
        trafficTable.allowsColumnReordering = false
        trafficTable.allowsColumnResizing = false
        trafficTable.usesAlternatingRowBackgroundColors = false

        let scrollView = NSScrollView()
        scrollView.drawsBackground = false
        scrollView.borderType = .noBorder
        scrollView.hasVerticalScroller = false
        scrollView.hasHorizontalScroller = false
        scrollView.horizontalScrollElasticity = .none
        scrollView.documentView = trafficTable
        scrollView.translatesAutoresizingMaskIntoConstraints = false

        let header = NSView()
        header.translatesAutoresizingMaskIntoConstraints = false
        let deviceHeader = makeTrafficHeaderLabel("设备名称", alignment: .left)
        let downloadHeader = makeTrafficHeaderLabel("下行速度", alignment: .right)
        let uploadHeader = makeTrafficHeaderLabel("上行速度", alignment: .right)
        header.addSubview(deviceHeader)
        header.addSubview(downloadHeader)
        header.addSubview(uploadHeader)

        let separator = NSView()
        separator.wantsLayer = true
        separator.layer?.backgroundColor = NSColor.white.withAlphaComponent(0.22).cgColor
        separator.translatesAutoresizingMaskIntoConstraints = false

        container.addSubview(scrollView)
        container.addSubview(header)
        container.addSubview(separator)

        NSLayoutConstraint.activate([
            container.heightAnchor.constraint(equalToConstant: 146),
            header.leadingAnchor.constraint(equalTo: container.leadingAnchor, constant: 10),
            header.trailingAnchor.constraint(equalTo: container.trailingAnchor, constant: -10),
            header.topAnchor.constraint(equalTo: container.topAnchor, constant: 7),
            header.heightAnchor.constraint(equalToConstant: 16),
            uploadHeader.trailingAnchor.constraint(equalTo: header.trailingAnchor),
            uploadHeader.widthAnchor.constraint(equalToConstant: 72),
            uploadHeader.centerYAnchor.constraint(equalTo: header.centerYAnchor),
            downloadHeader.trailingAnchor.constraint(equalTo: uploadHeader.leadingAnchor, constant: -6),
            downloadHeader.widthAnchor.constraint(equalToConstant: 72),
            downloadHeader.centerYAnchor.constraint(equalTo: header.centerYAnchor),
            deviceHeader.leadingAnchor.constraint(equalTo: header.leadingAnchor),
            deviceHeader.trailingAnchor.constraint(equalTo: downloadHeader.leadingAnchor, constant: -6),
            deviceHeader.centerYAnchor.constraint(equalTo: header.centerYAnchor),
            separator.leadingAnchor.constraint(equalTo: container.leadingAnchor, constant: 10),
            separator.trailingAnchor.constraint(equalTo: container.trailingAnchor, constant: -10),
            separator.topAnchor.constraint(equalTo: header.bottomAnchor, constant: 3),
            separator.heightAnchor.constraint(equalToConstant: 0.5),
            scrollView.leadingAnchor.constraint(equalTo: container.leadingAnchor, constant: 10),
            scrollView.trailingAnchor.constraint(equalTo: container.trailingAnchor, constant: -10),
            scrollView.topAnchor.constraint(equalTo: separator.bottomAnchor, constant: 3),
            scrollView.bottomAnchor.constraint(equalTo: container.bottomAnchor, constant: -7)
        ])
        return container
    }

    private func makeTrafficHeaderLabel(
        _ title: String,
        alignment: NSTextAlignment
    ) -> NSTextField {
        let label = NSTextField(labelWithString: title)
        label.font = .systemFont(ofSize: 9.5, weight: .medium)
        label.textColor = .tertiaryLabelColor
        label.alignment = alignment
        label.translatesAutoresizingMaskIntoConstraints = false
        return label
    }

    func numberOfRows(in tableView: NSTableView) -> Int {
        latestClientRates.count
    }

    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        guard row < latestClientRates.count, let tableColumn else { return nil }
        let identifier = NSUserInterfaceItemIdentifier("traffic-\(tableColumn.identifier.rawValue)")
        let field: NSTextField
        if let reusable = tableView.makeView(withIdentifier: identifier, owner: self) as? NSTextField {
            field = reusable
        } else {
            field = NSTextField(labelWithString: "")
            field.identifier = identifier
            field.font = tableColumn.identifier.rawValue == "device"
                ? .systemFont(ofSize: 10.5, weight: .medium)
                : .monospacedDigitSystemFont(ofSize: 10.5, weight: .medium)
            field.textColor = .labelColor
            field.lineBreakMode = .byTruncatingTail
            field.alignment = tableColumn.identifier.rawValue == "device" ? .left : .right
        }

        let client = latestClientRates[row]
        switch tableColumn.identifier.rawValue {
        case "device":
            field.stringValue = client.name
            field.toolTip = [client.ip, client.mac].filter { !$0.isEmpty }.joined(separator: " · ")
        case "download":
            field.stringValue = Self.compactRate(client.downloadRate)
        case "upload":
            field.stringValue = Self.compactRate(client.uploadRate)
        default:
            return nil
        }
        return field
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

    func updateClientRates(_ rates: [ClientRate]) {
        let previousCount = latestClientRates.count
        let visibleRates = rates.filter { !Self.isHiddenIoTDevice($0) }
        let currentByMAC = Dictionary(uniqueKeysWithValues: visibleRates.map { ($0.mac, $0) })

        for client in visibleRates where !clientOrder.contains(client.mac) {
            clientOrder.append(client.mac)
        }
        for mac in clientOrder {
            if let current = currentByMAC[mac] {
                clientRatesByMAC[mac] = current
            } else if let previous = clientRatesByMAC[mac] {
                clientRatesByMAC[mac] = ClientRate(
                    mac: previous.mac,
                    name: previous.name,
                    ip: previous.ip,
                    downloadRate: 0,
                    uploadRate: 0
                )
            }
        }
        latestClientRates = clientOrder.compactMap { clientRatesByMAC[$0] }
        if latestClientRates.count != previousCount {
            trafficTable.reloadData()
        } else if !latestClientRates.isEmpty {
            trafficTable.reloadData(
                forRowIndexes: IndexSet(integersIn: 0..<latestClientRates.count),
                columnIndexes: IndexSet(integersIn: 0..<trafficTable.numberOfColumns)
            )
        }
    }

    private static func isHiddenIoTDevice(_ client: ClientRate) -> Bool {
        let name = client.name.lowercased()
        return ["yeelink", "yeelight", "chuangmi-", "lumi-"].contains { name.contains($0) }
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

    private static func compactRate(_ bytesPerSecond: Double) -> String {
        if bytesPerSecond >= 1_000_000 {
            return String(format: "%.1f MB/s", bytesPerSecond / 1_000_000)
        }
        if bytesPerSecond >= 1_000 {
            return String(format: "%.0f KB/s", bytesPerSecond / 1_000)
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
        configuration.timeoutIntervalForRequest = 90
        configuration.timeoutIntervalForResource = 3_700
        configuration.httpMaximumConnectionsPerHost = 1
        return URLSession(configuration: configuration, delegate: self, delegateQueue: .main)
    }()
    private var streamTask: URLSessionDataTask?
    private var streamBuffer = Data()
    private var desiredStreamInterval: Int?
    private var activeStreamInterval: Int?
    private var fullyOccludedSince: Date?
    private var reconnectWorkItem: DispatchWorkItem?
    private var visibilityWorkItems: [DispatchWorkItem] = []
    private var workspaceObservers: [NSObjectProtocol] = []
    private var windowObservers: [NSObjectProtocol] = []
    private var previous: Counters?
    private var previousClientCounters: [String: APIClientCounter] = [:]
    private var previousClientSampledAt: Double?
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
        visibilityWorkItems.forEach { $0.cancel() }
        workspaceObservers.forEach(NSWorkspace.shared.notificationCenter.removeObserver)
        windowObservers.forEach(NotificationCenter.default.removeObserver)
        streamSession.invalidateAndCancel()
        controlSession.invalidateAndCancel()
    }

    func start() {
        let workspaceCenter = NSWorkspace.shared.notificationCenter
        let names: [Notification.Name] = [
            NSWorkspace.didActivateApplicationNotification,
            NSWorkspace.activeSpaceDidChangeNotification,
            NSWorkspace.didWakeNotification
        ]
        workspaceObservers = names.map { name in
            workspaceCenter.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
                self?.reevaluateVisibilitySoon()
            }
        }
        workspaceObservers.append(
            workspaceCenter.addObserver(
                forName: NSWorkspace.willSleepNotification,
                object: nil,
                queue: .main
            ) { [weak self] _ in
                self?.pauseForSleep()
            }
        )

        let notificationCenter = NotificationCenter.default
        if let window = card.window {
            let windowNotifications: [Notification.Name] = [
                NSWindow.didChangeOcclusionStateNotification,
                NSWindow.didMoveNotification,
                NSWindow.didMiniaturizeNotification,
                NSWindow.didDeminiaturizeNotification
            ]
            windowObservers = windowNotifications.map { name in
                notificationCenter.addObserver(forName: name, object: window, queue: .main) { [weak self] _ in
                    self?.reevaluateVisibilitySoon()
                }
            }
        }
        windowObservers.append(
            notificationCenter.addObserver(
                forName: NSApplication.didChangeScreenParametersNotification,
                object: nil,
                queue: .main
            ) { [weak self] _ in
                self?.reevaluateVisibilitySoon()
            }
        )
        reevaluateVisibilitySoon()
    }

    func refresh() {
        if desiredStreamInterval != nil {
            restartEventStream()
        } else {
            fetchOneSnapshot()
        }
    }

    func windowVisibilityChanged() {
        reevaluateVisibilitySoon()
    }

    private var isVisibleOnAnyDisplay: Bool {
        guard let window = card.window,
              window.isVisible,
              !window.isMiniaturized else { return false }
        return window.occlusionState.contains(.visible)
    }

    private func reevaluateVisibilitySoon() {
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) { [weak self] in
            self?.updateVisibilityPolicy()
        }
    }

    private func updateVisibilityPolicy() {
        if isVisibleOnAnyDisplay {
            fullyOccludedSince = nil
            cancelVisibilityTransitions()
            applyStreamInterval(1)
            return
        }

        if fullyOccludedSince == nil {
            fullyOccludedSince = Date()
            scheduleVisibilityTransitions()
        }

        let elapsed = Date().timeIntervalSince(fullyOccludedSince ?? Date())
        if elapsed >= 15 * 60 {
            applyStreamInterval(nil)
        } else if elapsed >= 5 * 60 {
            applyStreamInterval(60)
        } else if elapsed >= 60 {
            applyStreamInterval(30)
        } else {
            applyStreamInterval(15)
        }
    }

    private func scheduleVisibilityTransitions() {
        cancelVisibilityTransitions()
        for delay in [60.0, 5 * 60.0, 15 * 60.0] {
            let workItem = DispatchWorkItem { [weak self] in
                self?.updateVisibilityPolicy()
            }
            visibilityWorkItems.append(workItem)
            DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: workItem)
        }
    }

    private func cancelVisibilityTransitions() {
        visibilityWorkItems.forEach { $0.cancel() }
        visibilityWorkItems.removeAll(keepingCapacity: true)
    }

    private func applyStreamInterval(_ interval: Int?) {
        if desiredStreamInterval == interval {
            if interval == nil || (streamTask != nil && activeStreamInterval == interval) {
                return
            }
        }

        desiredStreamInterval = interval
        reconnectWorkItem?.cancel()
        reconnectWorkItem = nil

        let task = streamTask
        streamTask = nil
        activeStreamInterval = nil
        task?.cancel()
        streamBuffer.removeAll(keepingCapacity: interval != nil)

        guard interval != nil else {
            previous = nil
            previousClientCounters.removeAll(keepingCapacity: true)
            previousClientSampledAt = nil
            card.setPaused()
            return
        }
        startEventStream()
    }

    private func startEventStream() {
        guard let interval = desiredStreamInterval, streamTask == nil else { return }
        guard let token else {
            card.setConnected(false, message: "缺少API Token")
            return
        }

        reconnectWorkItem?.cancel()
        reconnectWorkItem = nil
        streamBuffer.removeAll(keepingCapacity: true)

        let baseEventsEndpoint = endpoint
            .deletingLastPathComponent()
            .appendingPathComponent("events", isDirectory: false)
        var components = URLComponents(url: baseEventsEndpoint, resolvingAgainstBaseURL: false)
        components?.queryItems = [URLQueryItem(name: "interval", value: String(interval))]
        guard let eventsEndpoint = components?.url else {
            card.setConnected(false, message: "SSE地址无效")
            return
        }
        var request = URLRequest(url: eventsEndpoint)
        request.httpMethod = "GET"
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 90
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")

        card.setConnecting()
        let task = streamSession.dataTask(with: request)
        streamTask = task
        activeStreamInterval = interval
        task.resume()
    }

    private func pauseForSleep() {
        fullyOccludedSince = Date()
        cancelVisibilityTransitions()
        applyStreamInterval(nil)
    }

    private func restartEventStream() {
        guard desiredStreamInterval != nil else {
            fetchOneSnapshot()
            return
        }
        let task = streamTask
        streamTask = nil
        activeStreamInterval = nil
        task?.cancel()
        streamBuffer.removeAll(keepingCapacity: true)
        startEventStream()
    }

    private func scheduleReconnect() {
        guard desiredStreamInterval != nil else { return }
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
            activeStreamInterval = nil
            card.setConnected(false, message: "SSE响应异常")
            completionHandler(.cancel)
            scheduleReconnect()
            return
        }
        guard http.statusCode == 200 else {
            streamTask = nil
            activeStreamInterval = nil
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
        activeStreamInterval = nil
        streamBuffer.removeAll(keepingCapacity: true)
        if desiredStreamInterval != nil {
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
                self.previousClientCounters.removeAll(keepingCapacity: true)
                self.previousClientSampledAt = nil
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

        let clientSampledAt = snapshot.clientsSampledAt ?? snapshot.uptimeSeconds
        var newClientRates: [ClientRate]?
        if previousClientSampledAt != clientSampledAt {
            let clientElapsed = previousClientSampledAt.flatMap { previousSample in
                let delta = clientSampledAt - previousSample
                return delta > 0 ? delta : nil
            }
            var rates: [ClientRate] = []
            for client in snapshot.clients ?? [] {
                var clientDownloadRate = 0.0
                var clientUploadRate = 0.0
                if let elapsed = clientElapsed, let old = previousClientCounters[client.mac] {
                    let rxDelta = client.rxBytes >= old.rxBytes ? client.rxBytes - old.rxBytes : 0
                    let txDelta = client.txBytes >= old.txBytes ? client.txBytes - old.txBytes : 0
                    clientDownloadRate = Double(rxDelta) / elapsed
                    clientUploadRate = Double(txDelta) / elapsed
                }
                rates.append(ClientRate(
                    mac: client.mac,
                    name: client.name,
                    ip: client.ip,
                    downloadRate: clientDownloadRate,
                    uploadRate: clientUploadRate
                ))
            }
            previousClientCounters = Dictionary(
                uniqueKeysWithValues: (snapshot.clients ?? []).map { ($0.mac, $0) }
            )
            previousClientSampledAt = clientSampledAt
            newClientRates = rates
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
        if let newClientRates {
            card.updateClientRates(newClientRates)
        }
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
        monitor.windowVisibilityChanged()
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
