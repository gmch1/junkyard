import AppKit
import Darwin
import Foundation

private let servicePort = 39281
private let proxyModel = "aliyun-translate-auto"

private struct ProxyStatus: Decodable {
    struct Client: Decodable {
        let requests: UInt64
        let successes: UInt64
        let failures: UInt64
    }

    let uptime_seconds: Int
    let available_models: Int
    let client: Client
}

private struct BackendError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

private final class BackendController {
    private let stateDirectory: URL

    init() {
        let applicationSupport = FileManager.default.urls(
            for: .applicationSupportDirectory,
            in: .userDomainMask
        ).first!
        stateDirectory = applicationSupport.appendingPathComponent("AliyunLLMProxy", isDirectory: true)
    }

    private var executableURL: URL? {
        Bundle.main.executableURL?
            .deletingLastPathComponent()
            .appendingPathComponent("AliyunLLMProxyBackend")
    }

    private var environment: [String: String] {
        var values = ProcessInfo.processInfo.environment
        values["ALIYUN_PROXY_STATE_DIR"] = stateDirectory.path
        values["ALIYUN_PROXY_HOST"] = "0.0.0.0"
        values["ALIYUN_PROXY_PORT"] = String(servicePort)
        return values
    }

    @discardableResult
    func run(_ arguments: [String], input: String? = nil) throws -> String {
        guard let executableURL else {
            throw BackendError(message: "应用包内缺少代理程序，请重新安装应用。")
        }
        try FileManager.default.createDirectory(
            at: stateDirectory,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )

        let process = Process()
        let outputPipe = Pipe()
        let errorPipe = Pipe()
        process.executableURL = executableURL
        process.arguments = arguments
        process.environment = environment
        process.standardOutput = outputPipe
        process.standardError = errorPipe

        var inputPipe: Pipe?
        if input != nil {
            let pipe = Pipe()
            process.standardInput = pipe
            inputPipe = pipe
        }

        try process.run()
        if let input, let inputPipe {
            inputPipe.fileHandleForWriting.write(Data(input.utf8))
            try? inputPipe.fileHandleForWriting.close()
        }
        process.waitUntilExit()

        let output = String(
            data: outputPipe.fileHandleForReading.readDataToEndOfFile(),
            encoding: .utf8
        ) ?? ""
        let error = String(
            data: errorPipe.fileHandleForReading.readDataToEndOfFile(),
            encoding: .utf8
        ) ?? ""
        guard process.terminationStatus == 0 else {
            let detail = error.trimmingCharacters(in: .whitespacesAndNewlines)
            throw BackendError(message: detail.isEmpty ? "代理操作失败。" : detail)
        }
        return output.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func clientKey() throws -> String {
        try run(["key"])
    }

    func saveUpstreamKey(_ value: String) throws {
        _ = try run(["set-upstream-key"], input: value)
    }

    func start() throws {
        _ = try run(["start"])
    }

    func stop() throws {
        _ = try run(["stop"])
    }
}

private final class MainWindowController: NSWindowController {
    private let backend = BackendController()
    private let statusLabel = NSTextField(labelWithString: "正在检查服务…")
    private let statusDot = NSView()
    private let apiKeyField = NSSecureTextField(frame: .zero)
    private let saveButton = NSButton(title: "保存并启动", target: nil, action: nil)
    private let toggleButton = NSButton(title: "启动服务", target: nil, action: nil)
    private let baseURLValue = NSTextField(labelWithString: "—")
    private let clientKeyValue = NSTextField(labelWithString: "—")
    private let modelValue = NSTextField(labelWithString: proxyModel)
    private let modelsValue = NSTextField(labelWithString: "—")
    private let requestsValue = NSTextField(labelWithString: "—")
    private let uptimeValue = NSTextField(labelWithString: "—")
    private let messageLabel = NSTextField(wrappingLabelWithString: "输入阿里云百炼 DashScope API Key 后即可启动。")
    private var clientKey = ""
    private var serviceRunning = false
    private var timer: Timer?

    init() {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 720, height: 560),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered,
            defer: false
        )
        window.title = "阿里云模型代理"
        window.minSize = NSSize(width: 660, height: 520)
        window.isReleasedWhenClosed = false
        window.center()
        super.init(window: window)
        buildInterface(in: window)
    }

    required init?(coder: NSCoder) { nil }

    deinit { timer?.invalidate() }

    private func buildInterface(in window: NSWindow) {
        let root = NSStackView()
        root.orientation = .vertical
        root.alignment = .width
        root.spacing = 18
        root.translatesAutoresizingMaskIntoConstraints = false

        let title = NSTextField(labelWithString: "阿里云模型代理")
        title.font = .systemFont(ofSize: 26, weight: .bold)
        let subtitle = NSTextField(wrappingLabelWithString: "在这台 Mac 上提供局域网可用、OpenAI Chat Completions 兼容的阿里云百炼代理。")
        subtitle.textColor = .secondaryLabelColor
        subtitle.font = .systemFont(ofSize: 13)
        let titleStack = NSStackView(views: [title, subtitle])
        titleStack.orientation = .vertical
        titleStack.alignment = .leading
        titleStack.spacing = 5

        statusDot.wantsLayer = true
        statusDot.layer?.cornerRadius = 5
        statusDot.layer?.backgroundColor = NSColor.systemOrange.cgColor
        statusDot.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            statusDot.widthAnchor.constraint(equalToConstant: 10),
            statusDot.heightAnchor.constraint(equalToConstant: 10)
        ])
        statusLabel.font = .systemFont(ofSize: 13, weight: .medium)
        let statusStack = NSStackView(views: [statusDot, statusLabel])
        statusStack.orientation = .horizontal
        statusStack.alignment = .centerY
        statusStack.spacing = 8

        let spacer = NSView()
        spacer.setContentHuggingPriority(.defaultLow, for: .horizontal)
        let header = NSStackView(views: [titleStack, spacer, statusStack])
        header.orientation = .horizontal
        header.alignment = .centerY

        let connectionGrid = NSGridView(views: [
            connectionRow(title: "Base URL", value: baseURLValue, action: #selector(copyBaseURL)),
            connectionRow(title: "API Key", value: clientKeyValue, action: #selector(copyClientKey)),
            connectionRow(title: "Model", value: modelValue, action: #selector(copyModel))
        ])
        connectionGrid.rowSpacing = 10
        connectionGrid.columnSpacing = 12
        connectionGrid.column(at: 0).xPlacement = .trailing
        connectionGrid.column(at: 1).xPlacement = .fill

        let connectionCard = makeCard(title: "客户端连接信息", content: connectionGrid)

        apiKeyField.placeholderString = "输入 DashScope API Key（保存后不会再次显示）"
        apiKeyField.font = .systemFont(ofSize: 13)
        apiKeyField.translatesAutoresizingMaskIntoConstraints = false
        apiKeyField.heightAnchor.constraint(equalToConstant: 32).isActive = true

        saveButton.bezelStyle = .rounded
        saveButton.keyEquivalent = "\r"
        saveButton.target = self
        saveButton.action = #selector(saveAndStart)
        toggleButton.bezelStyle = .rounded
        toggleButton.target = self
        toggleButton.action = #selector(toggleService)
        let buttonRow = NSStackView(views: [saveButton, toggleButton])
        buttonRow.orientation = .horizontal
        buttonRow.alignment = .centerY
        buttonRow.spacing = 10

        messageLabel.textColor = .secondaryLabelColor
        messageLabel.font = .systemFont(ofSize: 12)
        let form = NSStackView(views: [apiKeyField, buttonRow, messageLabel])
        form.orientation = .vertical
        form.alignment = .width
        form.spacing = 10
        let configurationCard = makeCard(title: "阿里云配置", content: form)

        let metrics = NSStackView(views: [
            metricTile(title: "可用模型", value: modelsValue),
            metricTile(title: "累计请求", value: requestsValue),
            metricTile(title: "运行时长", value: uptimeValue)
        ])
        metrics.orientation = .horizontal
        metrics.alignment = .height
        metrics.distribution = .fillEqually
        metrics.spacing = 12

        root.addArrangedSubview(header)
        root.addArrangedSubview(connectionCard)
        root.addArrangedSubview(configurationCard)
        root.addArrangedSubview(metrics)
        window.contentView = NSView()
        window.contentView?.addSubview(root)
        NSLayoutConstraint.activate([
            root.leadingAnchor.constraint(equalTo: window.contentView!.leadingAnchor, constant: 28),
            root.trailingAnchor.constraint(equalTo: window.contentView!.trailingAnchor, constant: -28),
            root.topAnchor.constraint(equalTo: window.contentView!.topAnchor, constant: 26),
            root.bottomAnchor.constraint(lessThanOrEqualTo: window.contentView!.bottomAnchor, constant: -26)
        ])
    }

    private func makeCard(title: String, content: NSView) -> NSView {
        let heading = NSTextField(labelWithString: title)
        heading.font = .systemFont(ofSize: 16, weight: .semibold)
        let stack = NSStackView(views: [heading, content])
        stack.orientation = .vertical
        stack.alignment = .width
        stack.spacing = 14
        stack.translatesAutoresizingMaskIntoConstraints = false

        let box = NSBox()
        box.boxType = .custom
        box.borderType = .lineBorder
        box.borderColor = .separatorColor
        box.borderWidth = 1
        box.cornerRadius = 12
        box.fillColor = .controlBackgroundColor
        box.contentViewMargins = NSSize(width: 18, height: 16)
        box.contentView = stack
        return box
    }

    private func connectionRow(title: String, value: NSTextField, action: Selector) -> [NSView] {
        let label = NSTextField(labelWithString: title)
        label.textColor = .secondaryLabelColor
        label.font = .systemFont(ofSize: 12, weight: .medium)
        value.isSelectable = true
        value.lineBreakMode = .byTruncatingMiddle
        value.font = .monospacedSystemFont(ofSize: 12, weight: .regular)
        let button = NSButton(title: "复制", target: self, action: action)
        button.bezelStyle = .rounded
        return [label, value, button]
    }

    private func metricTile(title: String, value: NSTextField) -> NSView {
        let label = NSTextField(labelWithString: title)
        label.textColor = .secondaryLabelColor
        label.font = .systemFont(ofSize: 12)
        value.font = .systemFont(ofSize: 20, weight: .semibold)
        let stack = NSStackView(views: [label, value])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 5
        let box = NSBox()
        box.boxType = .custom
        box.borderType = .lineBorder
        box.borderColor = .separatorColor
        box.cornerRadius = 10
        box.contentViewMargins = NSSize(width: 14, height: 12)
        box.contentView = stack
        return box
    }

    func startMonitoring() {
        baseURLValue.stringValue = "http://\(localIPv4Address()):\(servicePort)/v1"
        modelValue.stringValue = proxyModel
        loadClientKeyAndRefresh()
        timer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in
            self?.refreshStatus()
        }
    }

    private func loadClientKeyAndRefresh() {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            do {
                let key = try backend.clientKey()
                DispatchQueue.main.async {
                    self.clientKey = key
                    self.clientKeyValue.stringValue = key
                    self.refreshStatus()
                }
            } catch {
                DispatchQueue.main.async { self.showError(error.localizedDescription) }
            }
        }
    }

    private func refreshStatus() {
        guard !clientKey.isEmpty else { return }
        var request = URLRequest(url: URL(string: "http://127.0.0.1:\(servicePort)/v1/proxy/status")!)
        request.timeoutInterval = 1.5
        request.setValue("Bearer \(clientKey)", forHTTPHeaderField: "Authorization")
        URLSession.shared.dataTask(with: request) { [weak self] data, response, _ in
            guard let self else { return }
            let status = data.flatMap { try? JSONDecoder().decode(ProxyStatus.self, from: $0) }
            let ok = (response as? HTTPURLResponse)?.statusCode == 200 && status != nil
            DispatchQueue.main.async {
                self.serviceRunning = ok
                self.statusDot.layer?.backgroundColor = (ok ? NSColor.systemGreen : NSColor.systemOrange).cgColor
                self.statusLabel.stringValue = ok ? "代理运行中" : "代理未运行"
                self.toggleButton.title = ok ? "停止服务" : "启动服务"
                if let status {
                    self.modelsValue.stringValue = String(status.available_models)
                    self.requestsValue.stringValue = String(status.client.requests)
                    self.uptimeValue.stringValue = self.formatUptime(status.uptime_seconds)
                } else {
                    self.modelsValue.stringValue = "—"
                    self.requestsValue.stringValue = "—"
                    self.uptimeValue.stringValue = "—"
                }
            }
        }.resume()
    }

    @objc private func saveAndStart() {
        let key = apiKeyField.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard key.count >= 8, key.count <= 4096, !key.contains("\n"), !key.contains("\r") else {
            showError("API Key 长度或格式不正确。")
            return
        }
        setBusy(true, message: "正在保存并启动…")
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            do {
                try backend.saveUpstreamKey(key)
                try backend.start()
                DispatchQueue.main.async {
                    self.apiKeyField.stringValue = ""
                    self.setBusy(false, message: "API Key 已保存，局域网代理已启动。")
                    self.refreshStatus()
                }
            } catch {
                DispatchQueue.main.async {
                    self.setBusy(false, message: error.localizedDescription, isError: true)
                    self.refreshStatus()
                }
            }
        }
    }

    @objc private func toggleService() {
        setBusy(true, message: serviceRunning ? "正在停止…" : "正在启动…")
        let shouldStop = serviceRunning
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            do {
                if shouldStop { try backend.stop() } else { try backend.start() }
                DispatchQueue.main.async {
                    self.setBusy(false, message: shouldStop ? "代理已停止。" : "代理已启动。")
                    self.refreshStatus()
                }
            } catch {
                DispatchQueue.main.async {
                    self.setBusy(false, message: error.localizedDescription, isError: true)
                    self.refreshStatus()
                }
            }
        }
    }

    private func setBusy(_ busy: Bool, message: String, isError: Bool = false) {
        saveButton.isEnabled = !busy
        toggleButton.isEnabled = !busy
        messageLabel.stringValue = message
        messageLabel.textColor = isError ? .systemRed : .secondaryLabelColor
    }

    private func showError(_ message: String) {
        setBusy(false, message: message, isError: true)
    }

    private func formatUptime(_ seconds: Int) -> String {
        if seconds < 3600 { return "\(seconds / 60) 分钟" }
        if seconds < 86_400 { return String(format: "%.1f 小时", Double(seconds) / 3600) }
        return String(format: "%.1f 天", Double(seconds) / 86_400)
    }

    private func copy(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
        messageLabel.stringValue = "已复制到剪贴板。"
        messageLabel.textColor = .secondaryLabelColor
    }

    @objc private func copyBaseURL() { copy(baseURLValue.stringValue) }
    @objc private func copyClientKey() { copy(clientKeyValue.stringValue) }
    @objc private func copyModel() { copy(modelValue.stringValue) }
}

private func localIPv4Address() -> String {
    var pointer: UnsafeMutablePointer<ifaddrs>?
    guard getifaddrs(&pointer) == 0, let first = pointer else { return "127.0.0.1" }
    defer { freeifaddrs(pointer) }
    var candidates: [(String, String)] = []
    for item in sequence(first: first, next: { $0.pointee.ifa_next }) {
        let interface = item.pointee
        guard let address = interface.ifa_addr, address.pointee.sa_family == UInt8(AF_INET) else { continue }
        let flags = Int32(interface.ifa_flags)
        guard flags & IFF_UP != 0, flags & IFF_RUNNING != 0, flags & IFF_LOOPBACK == 0 else { continue }
        var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
        let result = getnameinfo(
            address,
            socklen_t(MemoryLayout<sockaddr_in>.size),
            &host,
            socklen_t(host.count),
            nil,
            0,
            NI_NUMERICHOST
        )
        guard result == 0 else { continue }
        let value = String(cString: host)
        guard !value.hasPrefix("169.254.") else { continue }
        candidates.append((String(cString: interface.ifa_name), value))
    }
    for preferred in ["en0", "en1"] {
        if let match = candidates.first(where: { $0.0 == preferred }) { return match.1 }
    }
    return candidates.first?.1 ?? "127.0.0.1"
}

private final class AppDelegate: NSObject, NSApplicationDelegate {
    private var mainWindow: MainWindowController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        installMenu()
        let controller = MainWindowController()
        mainWindow = controller
        controller.showWindow(nil)
        controller.window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        controller.startMonitoring()
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    private func installMenu() {
        let mainMenu = NSMenu()
        let appItem = NSMenuItem()
        mainMenu.addItem(appItem)
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "关于阿里云模型代理", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "退出", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu

        let editItem = NSMenuItem()
        mainMenu.addItem(editItem)
        let editMenu = NSMenu(title: "编辑")
        editMenu.addItem(withTitle: "剪切", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "复制", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "粘贴", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "全选", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editItem.submenu = editMenu
        NSApp.mainMenu = mainMenu
    }
}

@main
private enum AliyunLLMProxyApplication {
    static func main() {
        let application = NSApplication.shared
        let delegate = AppDelegate()
        application.delegate = delegate
        application.run()
        withExtendedLifetime(delegate) {}
    }
}
