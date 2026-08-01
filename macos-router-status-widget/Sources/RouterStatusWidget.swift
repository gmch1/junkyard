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

private final class StatusCardController: NSWindowController {
    private let statusDot = NSView(frame: NSRect(x: 0, y: 0, width: 8, height: 8))
    private let statusLabel = NSTextField(labelWithString: "正在连接…")
    private let wanBadge = NSTextField(labelWithString: "WAN --")
    private let updatedLabel = NSTextField(labelWithString: "")
    private let downloadValue = NSTextField(labelWithString: "--")
    private let uploadValue = NSTextField(labelWithString: "--")
    private let cpuValue = NSTextField(labelWithString: "--")
    private let memoryValue = NSTextField(labelWithString: "--")
    private let uptimeValue = NSTextField(labelWithString: "--")

    init() {
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 360, height: 252),
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
        let cardView = NSView()
        cardView.wantsLayer = true
        cardView.layer?.backgroundColor = NSColor.controlBackgroundColor.withAlphaComponent(0.96).cgColor
        cardView.layer?.cornerRadius = 28
        cardView.layer?.borderWidth = 1
        cardView.layer?.borderColor = NSColor.separatorColor.cgColor
        cardView.layer?.masksToBounds = true

        statusDot.wantsLayer = true
        statusDot.layer?.cornerRadius = 4
        statusDot.layer?.backgroundColor = NSColor.systemOrange.cgColor
        statusDot.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            statusDot.widthAnchor.constraint(equalToConstant: 8),
            statusDot.heightAnchor.constraint(equalToConstant: 8)
        ])

        let heading = NSTextField(labelWithString: "RAX3000M")
        heading.font = .systemFont(ofSize: 17, weight: .bold)

        statusLabel.font = .systemFont(ofSize: 11, weight: .medium)
        statusLabel.textColor = .secondaryLabelColor

        let status = NSStackView(views: [statusDot, statusLabel])
        status.orientation = .horizontal
        status.alignment = .centerY
        status.spacing = 6

        let identity = NSStackView(views: [heading, status])
        identity.orientation = .vertical
        identity.alignment = .leading
        identity.spacing = 1

        wanBadge.font = .monospacedDigitSystemFont(ofSize: 11, weight: .semibold)
        wanBadge.alignment = .center
        wanBadge.textColor = .secondaryLabelColor
        wanBadge.wantsLayer = true
        wanBadge.layer?.backgroundColor = NSColor.secondaryLabelColor.withAlphaComponent(0.10).cgColor
        wanBadge.layer?.cornerRadius = 10
        wanBadge.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            wanBadge.widthAnchor.constraint(greaterThanOrEqualToConstant: 78),
            wanBadge.heightAnchor.constraint(equalToConstant: 24)
        ])

        let header = NSStackView(views: [identity, wanBadge])
        header.orientation = .horizontal
        header.alignment = .centerY
        header.distribution = .fill
        wanBadge.setContentHuggingPriority(.required, for: .horizontal)

        let rates = NSStackView(views: [
            makeRateTile(title: "实时下载", arrow: "↓", color: .systemBlue, value: downloadValue),
            makeRateTile(title: "实时上传", arrow: "↑", color: .systemGreen, value: uploadValue)
        ])
        rates.orientation = .horizontal
        rates.alignment = .height
        rates.distribution = .fillEqually
        rates.spacing = 10

        let metrics = NSStackView(views: [
            makeSmallTile(title: "CPU", value: cpuValue),
            makeSmallTile(title: "内存", value: memoryValue),
            makeSmallTile(title: "运行时间", value: uptimeValue)
        ])
        metrics.orientation = .horizontal
        metrics.alignment = .height
        metrics.distribution = .fillEqually
        metrics.spacing = 8

        updatedLabel.font = .systemFont(ofSize: 10)
        updatedLabel.textColor = .tertiaryLabelColor
        updatedLabel.alignment = .center
        updatedLabel.stringValue = "每5秒通过SSH刷新"

        let content = NSStackView(views: [header, rates, metrics, updatedLabel])
        content.orientation = .vertical
        content.alignment = .width
        content.spacing = 9
        content.translatesAutoresizingMaskIntoConstraints = false

        cardView.addSubview(content)
        panel.contentView = cardView
        NSLayoutConstraint.activate([
            content.leadingAnchor.constraint(equalTo: cardView.leadingAnchor, constant: 16),
            content.trailingAnchor.constraint(equalTo: cardView.trailingAnchor, constant: -16),
            content.topAnchor.constraint(equalTo: cardView.topAnchor, constant: 15),
            content.bottomAnchor.constraint(equalTo: cardView.bottomAnchor, constant: -11)
        ])
    }

    private func makeRateTile(title: String, arrow: String, color: NSColor,
                              value: NSTextField) -> NSView {
        let tile = NSView()
        tile.wantsLayer = true
        tile.layer?.backgroundColor = color.withAlphaComponent(0.11).cgColor
        tile.layer?.cornerRadius = 18

        let caption = NSTextField(labelWithString: "\(arrow)  \(title)")
        caption.font = .systemFont(ofSize: 12, weight: .semibold)
        caption.textColor = color
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
        tile.layer?.backgroundColor = NSColor.secondaryLabelColor.withAlphaComponent(0.07).cgColor
        tile.layer?.cornerRadius = 14

        let caption = NSTextField(labelWithString: title)
        caption.font = .systemFont(ofSize: 10, weight: .medium)
        caption.textColor = .secondaryLabelColor
        value.font = .monospacedDigitSystemFont(ofSize: 15, weight: .semibold)
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
    }

    func update(downloadRate: Double, uploadRate: Double, cpuPercent: Double,
                memoryPercent: Double, memoryUsed: Double, memoryTotal: Double,
                linkSpeed: Int, uptimeSeconds: Double) {
        downloadValue.stringValue = Self.rate(downloadRate)
        uploadValue.stringValue = Self.rate(uploadRate)
        cpuValue.stringValue = String(format: "%.1f%%", cpuPercent)
        memoryValue.stringValue = String(format: "%.1f%%", memoryPercent)
        memoryValue.toolTip = String(format: "已使用 %.0f MB，共 %.0f MB", memoryUsed, memoryTotal)
        wanBadge.stringValue = "WAN \(Self.link(linkSpeed))"
        uptimeValue.stringValue = Self.uptime(uptimeSeconds)

        updatedLabel.stringValue = "已更新 · 每5秒通过SSH刷新"
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

private final class RouterMonitor {
    private let host: String
    private let wanInterface: String
    private let card: StatusCardController
    private var timer: Timer?
    private var previous: Counters?
    private var polling = false
    var onRateUpdate: ((Double, Double) -> Void)?

    init(card: StatusCardController) {
        self.card = card
        let defaults = UserDefaults.standard
        let configuredHost = defaults.string(forKey: "RouterHost") ?? "root@192.168.31.1"
        let configuredInterface = defaults.string(forKey: "WANInterface") ?? "eth1"
        self.host = configuredHost.range(
            of: #"^[A-Za-z0-9._%+:-]+@[A-Za-z0-9.:[\]-]+$"#,
            options: .regularExpression
        ) != nil ? configuredHost : "root@192.168.31.1"
        self.wanInterface = configuredInterface.range(
            of: #"^[A-Za-z0-9_.:-]+$"#,
            options: .regularExpression
        ) != nil ? configuredInterface : "eth1"
    }

    func start() {
        poll()
        timer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            self?.poll()
        }
    }

    func poll() {
        guard !polling else { return }
        polling = true

        let command = "awk 'NR==1 {print \"cpu\",$2,$3,$4,$5,$6,$7,$8}' /proc/stat; "
            + "awk '/MemTotal:/ {t=$2} /MemAvailable:/ {a=$2} END {print \"mem\",t,a}' /proc/meminfo; "
            + "printf 'net '; tr -d '\\n' < /sys/class/net/\(wanInterface)/statistics/rx_bytes; "
            + "printf ' '; tr -d '\\n' < /sys/class/net/\(wanInterface)/statistics/tx_bytes; "
            + "printf ' '; cat /sys/class/net/\(wanInterface)/speed; "
            + "awk '{print \"uptime\",$1}' /proc/uptime"

        let process = Process()
        let stdout = Pipe()
        let stderr = Pipe()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/ssh")
        process.arguments = [
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=3",
            "-o", "StrictHostKeyChecking=accept-new",
            host, command
        ]
        process.standardOutput = stdout
        process.standardError = stderr

        process.terminationHandler = { [weak self] task in
            let output = String(data: stdout.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
            let error = String(data: stderr.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
            DispatchQueue.main.async {
                guard let self else { return }
                self.polling = false
                if task.terminationStatus == 0 {
                    self.consume(output)
                } else {
                    let detail = error.contains("Permission denied") ? "SSH免密失败" : "路由器离线"
                    self.card.setConnected(false, message: detail)
                }
            }
        }

        do {
            try process.run()
            DispatchQueue.global().asyncAfter(deadline: .now() + 4.5) {
                if process.isRunning { process.terminate() }
            }
        } catch {
            polling = false
            card.setConnected(false, message: "无法启动SSH")
        }
    }

    private func consume(_ text: String) {
        var cpuValues: [UInt64] = []
        var memoryTotalKB: Double = 0
        var memoryAvailableKB: Double = 0
        var rxBytes: UInt64 = 0
        var txBytes: UInt64 = 0
        var linkSpeed = 0
        var uptimeSeconds: Double = 0

        for line in text.split(separator: "\n") {
            let fields = line.split(whereSeparator: { $0 == " " || $0 == "\t" })
            guard let kind = fields.first else { continue }
            switch kind {
            case "cpu": cpuValues = fields.dropFirst().compactMap { UInt64($0) }
            case "mem" where fields.count >= 3:
                memoryTotalKB = Double(fields[1]) ?? 0
                memoryAvailableKB = Double(fields[2]) ?? 0
            case "net" where fields.count >= 4:
                rxBytes = UInt64(fields[1]) ?? 0
                txBytes = UInt64(fields[2]) ?? 0
                linkSpeed = Int(fields[3]) ?? 0
            case "uptime" where fields.count >= 2:
                uptimeSeconds = Double(fields[1]) ?? 0
            default: break
            }
        }

        guard cpuValues.count >= 7, memoryTotalKB > 0, rxBytes > 0 else {
            card.setConnected(false, message: "数据格式异常")
            return
        }

        let idle = cpuValues[3] + cpuValues[4]
        let total = cpuValues.reduce(0, +)
        let now = Date()
        let current = Counters(cpuTotal: total, cpuIdle: idle, rxBytes: rxBytes, txBytes: txBytes, sampledAt: now)
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

        let usedKB = memoryTotalKB - memoryAvailableKB
        let memoryPercent = usedKB / memoryTotalKB * 100
        card.setConnected(true, message: "已连接")
        card.update(
            downloadRate: downloadRate,
            uploadRate: uploadRate,
            cpuPercent: cpuPercent,
            memoryPercent: memoryPercent,
            memoryUsed: usedKB / 1024,
            memoryTotal: memoryTotalKB / 1024,
            linkSpeed: linkSpeed,
            uptimeSeconds: uptimeSeconds
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

    @objc private func refresh() { monitor.poll() }
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
