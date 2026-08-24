import AppKit
import Foundation

private let servicePort = 39281
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
    private func run(_ arguments: [String]) throws -> String {
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
            throw BackendError(message: detail.isEmpty ? "代理服务操作失败。" : detail)
        }
        return output.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func ensureRunning() throws { _ = try run(["start"]) }
    func stop() throws { _ = try run(["stop"]) }
}

private final class MenuBarController: NSObject {
    private let backend = BackendController()
    private let backendQueue = DispatchQueue(label: "io.github.gmch1.AliyunLLMProxy.backend")
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private let menu = NSMenu()
    private var monitorTimer: Timer?
    private var ensureInProgress = false
    private var managementOpenPending = false
    private var shuttingDown = false

    override init() {
        super.init()
        configureStatusItem()
        configureMenu()
    }

    deinit { monitorTimer?.invalidate() }

    private func configureStatusItem() {
        guard let button = statusItem.button else { return }
        let image = NSImage(systemSymbolName: "network", accessibilityDescription: "阿里云模型代理")
        image?.isTemplate = true
        button.image = image
        if image == nil { button.title = "A" }
        button.toolTip = "阿里云模型代理"
    }

    private func configureMenu() {
        let openItem = NSMenuItem(title: "打开管理页面", action: #selector(openManagementPage), keyEquivalent: "o")
        openItem.target = self
        menu.addItem(openItem)
        menu.addItem(.separator())

        let quitItem = NSMenuItem(title: "退出", action: #selector(quitApplication), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
        statusItem.menu = menu
    }

    func launch() {
        managementOpenPending = true
        ensureProxyRunning(presentErrors: true)
        monitorTimer = Timer.scheduledTimer(withTimeInterval: 8, repeats: true) { [weak self] _ in
            self?.ensureProxyRunning(presentErrors: false)
        }
    }

    func shutdown() {
        guard !shuttingDown else { return }
        shuttingDown = true
        monitorTimer?.invalidate()
        monitorTimer = nil
        managementOpenPending = false

        backendQueue.sync {
            try? self.backend.stop()
        }
    }

    @objc func openManagementPage() {
        managementOpenPending = true
        ensureProxyRunning(presentErrors: true)
    }

    private func ensureProxyRunning(presentErrors: Bool) {
        guard !shuttingDown, !ensureInProgress else { return }
        ensureInProgress = true
        backendQueue.async { [weak self] in
            guard let self else { return }
            do {
                try self.backend.ensureRunning()
                DispatchQueue.main.async {
                    guard !self.shuttingDown else { return }
                    self.ensureInProgress = false
                    self.openManagementPageIfNeeded()
                }
            } catch {
                DispatchQueue.main.async {
                    guard !self.shuttingDown else { return }
                    self.ensureInProgress = false
                    let shouldPresent = presentErrors || self.managementOpenPending
                    self.managementOpenPending = false
                    if shouldPresent { self.showError(error.localizedDescription) }
                }
            }
        }
    }

    private func openManagementPageIfNeeded() {
        guard managementOpenPending else { return }
        managementOpenPending = false
        DispatchQueue.main.async { [weak self] in
            self?.openBrowserUsingWorkspace()
        }
    }

    private func openBrowserUsingWorkspace() {
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.activates = true
        configuration.addsToRecentItems = false
        NSWorkspace.shared.open(managementURL, configuration: configuration) { [weak self] _, error in
            guard let error else { return }
            DispatchQueue.main.async {
                self?.openBrowserUsingSystemTool(workspaceError: error)
            }
        }
    }

    private func openBrowserUsingSystemTool(workspaceError: Error) {
        let process = Process()
        let errorPipe = Pipe()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        process.arguments = [managementURL.absoluteString]
        process.standardError = errorPipe
        do {
            try process.run()
            process.waitUntilExit()
            guard process.terminationStatus != 0 else { return }
            let detail = String(
                data: errorPipe.fileHandleForReading.readDataToEndOfFile(),
                encoding: .utf8
            )?.trimmingCharacters(in: .whitespacesAndNewlines)
            let message = detail?.isEmpty == false ? detail : nil
            showError(message ?? workspaceError.localizedDescription)
        } catch {
            showError("无法打开管理页面：\(error.localizedDescription)")
        }
    }

    @objc private func quitApplication() {
        NSApp.terminate(nil)
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

    func applicationWillTerminate(_ notification: Notification) {
        menuBarController?.shutdown()
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
