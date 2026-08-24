import AppKit
import Darwin
import Foundation

private let servicePort = 39281
private let modelAlias = "aliyun-translate-auto"
private let managementURL = URL(string: "http://127.0.0.1:\(servicePort)/")!

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
    func run(_ arguments: [String]) throws -> String {
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

        try process.run()
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

    func start() throws { _ = try run(["start"]) }
    func stop() throws { _ = try run(["stop"]) }
    func status() throws { _ = try run(["status"]) }
    func clientKey() throws -> String { try run(["key"]) }
}

private final class MenuBarController: NSObject, NSMenuDelegate {
    private let backend = BackendController()
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private let menu = NSMenu()
    private let statusMenuItem = NSMenuItem(title: "正在启动服务…", action: nil, keyEquivalent: "")
    private let toggleMenuItem = NSMenuItem(title: "停止服务", action: nil, keyEquivalent: "")
    private var serviceRunning = false
    private var refreshTimer: Timer?
    private var operationInProgress = false

    override init() {
        super.init()
        configureStatusItem()
        configureMenu()
    }

    deinit { refreshTimer?.invalidate() }

    private func configureStatusItem() {
        guard let button = statusItem.button else { return }
        let image = NSImage(
            systemSymbolName: "network",
            accessibilityDescription: "阿里云模型代理"
        )
        image?.isTemplate = true
        button.image = image
        if image == nil { button.title = "A" }
        button.toolTip = "阿里云模型代理"
    }

    private func configureMenu() {
        menu.delegate = self
        statusMenuItem.isEnabled = false
        menu.addItem(statusMenuItem)
        menu.addItem(.separator())

        let openItem = NSMenuItem(title: "打开管理页面", action: #selector(openManagementPage), keyEquivalent: "o")
        openItem.target = self
        menu.addItem(openItem)

        let copyBaseURL = NSMenuItem(title: "复制 Base URL", action: #selector(copyBaseURL), keyEquivalent: "")
        copyBaseURL.target = self
        menu.addItem(copyBaseURL)

        let copyAPIKey = NSMenuItem(title: "复制客户端 API Key", action: #selector(copyClientKey), keyEquivalent: "")
        copyAPIKey.target = self
        menu.addItem(copyAPIKey)

        let copyModel = NSMenuItem(title: "复制模型名称", action: #selector(copyModelAlias), keyEquivalent: "")
        copyModel.target = self
        menu.addItem(copyModel)

        menu.addItem(.separator())
        toggleMenuItem.target = self
        toggleMenuItem.action = #selector(toggleService)
        menu.addItem(toggleMenuItem)

        menu.addItem(.separator())
        let quitItem = NSMenuItem(title: "停止服务并退出", action: #selector(stopAndQuit), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
        statusItem.menu = menu
    }

    func launch() {
        setStatus(title: "正在启动服务…", running: false)
        performBackendOperation(resultRunning: true, openPageOnSuccess: true) { backend in
            try backend.start()
        }
        refreshTimer = Timer.scheduledTimer(withTimeInterval: 4, repeats: true) { [weak self] _ in
            self?.refreshStatus()
        }
    }

    func menuWillOpen(_ menu: NSMenu) {
        refreshStatus()
    }

    private func refreshStatus() {
        guard !operationInProgress else { return }
        DispatchQueue.global(qos: .utility).async { [weak self] in
            guard let self else { return }
            let running = (try? self.backend.status()) != nil
            DispatchQueue.main.async {
                self.setStatus(title: running ? "代理服务运行中" : "代理服务已停止", running: running)
            }
        }
    }

    private func setStatus(title: String, running: Bool) {
        serviceRunning = running
        statusMenuItem.title = title
        toggleMenuItem.title = running ? "停止服务" : "启动服务"
        toggleMenuItem.isEnabled = !operationInProgress
        statusItem.button?.toolTip = "阿里云模型代理 · \(running ? "运行中" : "已停止")"
    }

    private func performBackendOperation(
        resultRunning: Bool,
        openPageOnSuccess: Bool = false,
        operation: @escaping (BackendController) throws -> Void
    ) {
        guard !operationInProgress else { return }
        operationInProgress = true
        toggleMenuItem.isEnabled = false
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            do {
                try operation(self.backend)
                DispatchQueue.main.async {
                    self.operationInProgress = false
                    self.setStatus(
                        title: resultRunning ? "代理服务运行中" : "代理服务已停止",
                        running: resultRunning
                    )
                    if openPageOnSuccess { NSWorkspace.shared.open(managementURL) }
                }
            } catch {
                DispatchQueue.main.async {
                    self.operationInProgress = false
                    self.setStatus(title: "服务操作失败", running: false)
                    self.showError(error.localizedDescription)
                }
            }
        }
    }

    @objc func openManagementPage() {
        if serviceRunning {
            NSWorkspace.shared.open(managementURL)
            return
        }
        setStatus(title: "正在启动服务…", running: false)
        performBackendOperation(resultRunning: true, openPageOnSuccess: true) { backend in
            try backend.start()
        }
    }

    @objc private func toggleService() {
        let shouldStop = serviceRunning
        setStatus(title: shouldStop ? "正在停止服务…" : "正在启动服务…", running: serviceRunning)
        performBackendOperation(resultRunning: !shouldStop) { backend in
            if shouldStop { try backend.stop() } else { try backend.start() }
        }
    }

    @objc private func copyBaseURL() {
        copy("http://\(localIPv4Address()):\(servicePort)/v1")
    }

    @objc private func copyClientKey() {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            do {
                let key = try self.backend.clientKey()
                DispatchQueue.main.async { self.copy(key) }
            } catch {
                DispatchQueue.main.async { self.showError(error.localizedDescription) }
            }
        }
    }

    @objc private func copyModelAlias() { copy(modelAlias) }

    private func copy(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
    }

    @objc private func stopAndQuit() {
        operationInProgress = true
        toggleMenuItem.isEnabled = false
        statusMenuItem.title = "正在停止服务…"
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            do {
                try self.backend.stop()
                DispatchQueue.main.async { NSApp.terminate(nil) }
            } catch {
                DispatchQueue.main.async {
                    self.operationInProgress = false
                    self.showError(error.localizedDescription)
                    self.refreshStatus()
                }
            }
        }
    }

    private func showError(_ message: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "阿里云模型代理"
        alert.informativeText = message
        alert.addButton(withTitle: "好")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
    }
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
    private var menuBarController: MenuBarController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        let controller = MenuBarController()
        menuBarController = controller
        controller.launch()
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        menuBarController?.openManagementPage()
        return true
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
