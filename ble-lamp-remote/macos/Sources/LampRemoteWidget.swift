import AppKit
import CoreGraphics
import Foundation

private struct APISnapshot: Decodable {
    let version: Int
    let device: String
    let bluetoothReady: Bool
    let txCount: Int
    let lastAction: String
    let lastActionAt: Int64

    enum CodingKeys: String, CodingKey {
        case version
        case device
        case bluetoothReady = "bluetooth_ready"
        case txCount = "tx_count"
        case lastAction = "last_action"
        case lastActionAt = "last_action_at"
    }
}

private struct ActionResponse: Decodable {
    let accepted: Bool
    let action: String
    let label: String
}

private enum ClientFailure: LocalizedError {
    case missingToken
    case invalidConfiguration(String)
    case invalidResponse
    case rejected(Int)

    var errorDescription: String? {
        switch self {
        case .missingToken: return "缺少 API Token"
        case let .invalidConfiguration(message): return message
        case .invalidResponse: return "网关响应无效"
        case let .rejected(code): return "网关返回 HTTP \(code)"
        }
    }
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
        layer?.backgroundColor = NSColor(
            calibratedRed: 1.0,
            green: 0.94,
            blue: 0.76,
            alpha: 0.16
        ).cgColor
        layer?.cornerRadius = 24
        layer?.borderWidth = 0.75
        layer?.borderColor = NSColor.white.withAlphaComponent(0.48).cgColor
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
            cornerWidth: 24,
            cornerHeight: 24,
            transform: nil
        )
    }
}

private final class ActionButton: NSButton {
    private let handler: () -> Void

    init(
        title: String,
        symbol: String,
        fill: NSColor,
        tint: NSColor,
        handler: @escaping () -> Void
    ) {
        self.handler = handler
        super.init(frame: .zero)
        self.title = title
        target = self
        action = #selector(invoke)
        isBordered = false
        bezelStyle = .regularSquare
        image = NSImage(systemSymbolName: symbol, accessibilityDescription: title)
        imagePosition = .imageLeading
        contentTintColor = tint
        font = .systemFont(ofSize: 13, weight: .semibold)
        attributedTitle = NSAttributedString(
            string: title,
            attributes: [
                .font: NSFont.systemFont(ofSize: 13, weight: .semibold),
                .foregroundColor: tint
            ]
        )
        wantsLayer = true
        layer?.backgroundColor = fill.cgColor
        layer?.cornerRadius = 13
        translatesAutoresizingMaskIntoConstraints = false
        heightAnchor.constraint(equalToConstant: 44).isActive = true
    }

    required init?(coder: NSCoder) { nil }

    @objc private func invoke() { handler() }
}

private final class LampCardController: NSWindowController {
    private let statusDot = NSView(frame: NSRect(x: 0, y: 0, width: 8, height: 8))
    private let statusLabel = NSTextField(labelWithString: "正在连接…")
    private let lastActionLabel = NSTextField(labelWithString: "尚未发送命令")
    var onAction: ((String) -> Void)?
    var onRefreshRequested: (() -> Void)?
    var onImportTokenRequested: (() -> Void)?
    var onImportAddressRequested: (() -> Void)?

    init() {
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 320, height: 306),
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
        let card = LightGlassCardView()
        card.menu = makeCardMenu()
        panel.contentView = card

        let title = NSTextField(labelWithString: "佛山照明")
        title.font = .systemFont(ofSize: 18, weight: .semibold)
        title.textColor = .labelColor

        statusDot.wantsLayer = true
        statusDot.layer?.cornerRadius = 4
        statusDot.layer?.backgroundColor = NSColor.systemOrange.cgColor
        statusDot.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            statusDot.widthAnchor.constraint(equalToConstant: 8),
            statusDot.heightAnchor.constraint(equalToConstant: 8)
        ])
        statusLabel.font = .systemFont(ofSize: 10.5, weight: .medium)
        statusLabel.textColor = .secondaryLabelColor
        statusLabel.lineBreakMode = .byTruncatingTail
        let status = NSStackView(views: [statusDot, statusLabel])
        status.orientation = .horizontal
        status.alignment = .centerY
        status.spacing = 6

        let heading = NSStackView(views: [title, status])
        heading.orientation = .vertical
        heading.alignment = .leading
        heading.spacing = 3

        let bulb = NSImageView(image: NSImage(
            systemSymbolName: "lightbulb.max.fill",
            accessibilityDescription: "灯光"
        ) ?? NSImage())
        bulb.contentTintColor = .systemOrange
        bulb.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            bulb.widthAnchor.constraint(equalToConstant: 30),
            bulb.heightAnchor.constraint(equalToConstant: 30)
        ])
        let headerSpacer = NSView()
        let header = NSStackView(views: [heading, headerSpacer, bulb])
        header.orientation = .horizontal
        header.alignment = .centerY

        let power = makeRow([
            actionButton("开启", "power", .systemOrange, "on"),
            actionButton("关闭", "power", NSColor(calibratedWhite: 0.25, alpha: 1), "off")
        ])
        let brightness = makeRow([
            actionButton("调暗", "minus.circle.fill", NSColor(calibratedWhite: 0.48, alpha: 1), "brightness/down"),
            actionButton("调亮", "plus.circle.fill", .systemYellow, "brightness/up")
        ])
        let temperature = makeRow([
            actionButton("暖光", "sun.max.fill", .systemOrange, "temperature/warmer"),
            actionButton("冷光", "snowflake", .systemBlue, "temperature/cooler")
        ])
        let preset = makeRow([
            actionButton("全亮", "sun.max.circle.fill", .systemYellow, "preset/full"),
            actionButton(
                "半亮",
                "circle.lefthalf.filled",
                NSColor(calibratedRed: 0.34, green: 0.31, blue: 0.46, alpha: 1),
                "preset/half"
            )
        ])

        lastActionLabel.font = .systemFont(ofSize: 10.5, weight: .medium)
        lastActionLabel.textColor = .tertiaryLabelColor
        lastActionLabel.alignment = .center
        lastActionLabel.lineBreakMode = .byTruncatingTail

        let content = NSStackView(views: [header, power, brightness, temperature, preset, lastActionLabel])
        content.orientation = .vertical
        content.alignment = .width
        content.spacing = 9
        content.translatesAutoresizingMaskIntoConstraints = false
        card.addSubview(content)
        NSLayoutConstraint.activate([
            content.leadingAnchor.constraint(equalTo: card.leadingAnchor, constant: 15),
            content.trailingAnchor.constraint(equalTo: card.trailingAnchor, constant: -15),
            content.topAnchor.constraint(equalTo: card.topAnchor, constant: 14),
            content.bottomAnchor.constraint(equalTo: card.bottomAnchor, constant: -13)
        ])
    }

    private func makeRow(_ views: [NSView]) -> NSStackView {
        let row = NSStackView(views: views)
        row.orientation = .horizontal
        row.alignment = .height
        row.distribution = .fillEqually
        row.spacing = 8
        return row
    }

    private func actionButton(
        _ title: String,
        _ symbol: String,
        _ tint: NSColor,
        _ route: String
    ) -> ActionButton {
        ActionButton(
            title: title,
            symbol: symbol,
            fill: tint.withAlphaComponent(0.11),
            tint: tint
        ) { [weak self] in
            self?.onAction?(route)
        }
    }

    private func makeCardMenu() -> NSMenu {
        let menu = NSMenu()
        menu.addItem(withTitle: "立即检查连接", action: #selector(refresh), keyEquivalent: "")
        menu.addItem(.separator())
        menu.addItem(withTitle: "从剪贴板导入 API 地址", action: #selector(importAddress), keyEquivalent: "")
        menu.addItem(withTitle: "从剪贴板导入 Token", action: #selector(importToken), keyEquivalent: "")
        menu.addItem(.separator())
        menu.addItem(withTitle: "退出", action: #selector(quit), keyEquivalent: "")
        menu.items.forEach { $0.target = self }
        return menu
    }

    @objc private func refresh() { onRefreshRequested?() }
    @objc private func importAddress() { onImportAddressRequested?() }
    @objc private func importToken() { onImportTokenRequested?() }
    @objc private func quit() { NSApp.terminate(nil) }

    func placeAtTopRight() {
        guard let screen = NSScreen.main, let window else { return }
        let frame = window.frame
        let visible = screen.visibleFrame
        window.setFrameOrigin(NSPoint(
            x: visible.maxX - frame.width - 18,
            y: visible.maxY - frame.height - 18
        ))
    }

    func setConnection(ready: Bool, message: String) {
        statusDot.layer?.backgroundColor = (ready ? NSColor.systemGreen : NSColor.systemRed).cgColor
        statusLabel.stringValue = message
    }

    func setWorking(_ action: String) {
        statusDot.layer?.backgroundColor = NSColor.systemOrange.cgColor
        statusLabel.stringValue = "正在触发…"
        lastActionLabel.stringValue = action
    }

    func setAccepted(_ action: String) {
        statusDot.layer?.backgroundColor = NSColor.systemGreen.cgColor
        statusLabel.stringValue = "网关已接收"
        lastActionLabel.stringValue = "刚刚触发：\(action)"
    }

    func update(snapshot: APISnapshot) {
        setConnection(
            ready: snapshot.bluetoothReady,
            message: snapshot.bluetoothReady ? "Android 网关在线" : "蓝牙未就绪"
        )
        if !snapshot.lastAction.isEmpty {
            lastActionLabel.stringValue = "最近触发：\(snapshot.lastAction) · #\(snapshot.txCount)"
        }
    }
}

private final class LampAPIClient: @unchecked Sendable {
    private static let defaultBaseURL = URL(string: "http://192.168.31.129:8791")!
    private let session: URLSession
    private let supportDirectory: URL
    private var baseURL: URL
    private var token: String?

    init() {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.timeoutIntervalForRequest = 4
        configuration.timeoutIntervalForResource = 5
        configuration.httpMaximumConnectionsPerHost = 1
        session = URLSession(configuration: configuration)
        let support = FileManager.default.urls(
            for: .applicationSupportDirectory,
            in: .userDomainMask
        )[0].appendingPathComponent("LampRemoteWidget", isDirectory: true)
        supportDirectory = support
        baseURL = Self.validBaseURL(UserDefaults.standard.string(forKey: "APIURL"))
            ?? Self.defaultBaseURL
        token = Self.readToken(from: support)
    }

    func fetchStatus(completion: @escaping (Result<APISnapshot, Error>) -> Void) {
        request(method: "GET", path: "v1/status", response: APISnapshot.self, completion: completion)
    }

    func send(route: String, completion: @escaping (Result<ActionResponse, Error>) -> Void) {
        request(method: "POST", path: "v1/light/\(route)", response: ActionResponse.self, completion: completion)
    }

    func importTokenFromPasteboard() throws {
        guard let candidate = NSPasteboard.general.string(forType: .string)?
            .trimmingCharacters(in: .whitespacesAndNewlines),
              candidate.range(of: "^[0-9a-fA-F]{64}$", options: .regularExpression) != nil else {
            throw ClientFailure.invalidConfiguration("剪贴板不是 64 位十六进制 Token")
        }
        try FileManager.default.createDirectory(
            at: supportDirectory,
            withIntermediateDirectories: true
        )
        let tokenURL = supportDirectory.appendingPathComponent("api-token")
        try (candidate.lowercased() + "\n").write(to: tokenURL, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o600],
            ofItemAtPath: tokenURL.path
        )
        token = candidate.lowercased()
    }

    func importAddressFromPasteboard() throws {
        let candidate = NSPasteboard.general.string(forType: .string)?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard let imported = Self.validBaseURL(candidate) else {
            throw ClientFailure.invalidConfiguration("剪贴板不是有效的 HTTP(S) API 地址")
        }
        baseURL = imported
        UserDefaults.standard.set(imported.absoluteString, forKey: "APIURL")
    }

    private func request<T: Decodable>(
        method: String,
        path: String,
        response: T.Type,
        completion: @escaping (Result<T, Error>) -> Void
    ) {
        guard let token, !token.isEmpty else {
            DispatchQueue.main.async { completion(.failure(ClientFailure.missingToken)) }
            return
        }
        var endpoint = baseURL
        for component in path.split(separator: "/") {
            endpoint = endpoint.appendingPathComponent(String(component), isDirectory: false)
        }
        var request = URLRequest(url: endpoint)
        request.httpMethod = method
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if method == "POST" {
            request.httpBody = Data()
        }
        session.dataTask(with: request) { data, urlResponse, error in
            let result: Result<T, Error>
            if let error {
                result = .failure(error)
            } else if let http = urlResponse as? HTTPURLResponse,
                      !(200...299).contains(http.statusCode) {
                result = .failure(ClientFailure.rejected(http.statusCode))
            } else if let data,
                      let decoded = try? JSONDecoder().decode(T.self, from: data) {
                result = .success(decoded)
            } else {
                result = .failure(ClientFailure.invalidResponse)
            }
            DispatchQueue.main.async { completion(result) }
        }.resume()
    }

    private static func validBaseURL(_ value: String?) -> URL? {
        guard let value,
              var components = URLComponents(string: value),
              ["http", "https"].contains(components.scheme?.lowercased() ?? ""),
              components.host?.isEmpty == false,
              components.user == nil,
              components.password == nil,
              components.query == nil,
              components.fragment == nil,
              components.path.isEmpty || components.path == "/" else { return nil }
        components.path = ""
        return components.url
    }

    private static func readToken(from directory: URL) -> String? {
        let tokenURL = directory.appendingPathComponent("api-token")
        return try? String(contentsOf: tokenURL, encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var card: LampCardController!
    private var client: LampAPIClient!
    private var timer: Timer?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        card = LampCardController()
        client = LampAPIClient()
        card.placeAtTopRight()
        card.showWindow(nil)
        card.window?.orderFrontRegardless()

        card.onAction = { [weak self] route in self?.send(route: route) }
        card.onRefreshRequested = { [weak self] in self?.refresh() }
        card.onImportTokenRequested = { [weak self] in self?.importToken() }
        card.onImportAddressRequested = { [weak self] in self?.importAddress() }
        refresh()
        timer = Timer.scheduledTimer(withTimeInterval: 15, repeats: true) { [weak self] _ in
            self?.refresh()
        }
    }

    private func send(route: String) {
        let display = route
            .replacingOccurrences(of: "brightness/", with: "亮度 ")
            .replacingOccurrences(of: "temperature/", with: "色温 ")
            .replacingOccurrences(of: "preset/full", with: "全亮")
            .replacingOccurrences(of: "preset/half", with: "半亮")
            .replacingOccurrences(of: "preset/toggle", with: "全亮 / 半亮")
            .replacingOccurrences(of: "on", with: "开启")
            .replacingOccurrences(of: "off", with: "关闭")
            .replacingOccurrences(of: "up", with: "增加")
            .replacingOccurrences(of: "down", with: "降低")
            .replacingOccurrences(of: "warmer", with: "偏暖")
            .replacingOccurrences(of: "cooler", with: "偏冷")
        card.setWorking(display)
        client.send(route: route) { [weak self] result in
            switch result {
            case let .success(response): self?.card.setAccepted(response.label)
            case let .failure(error): self?.card.setConnection(ready: false, message: error.localizedDescription)
            }
        }
    }

    private func refresh() {
        client.fetchStatus { [weak self] result in
            switch result {
            case let .success(snapshot): self?.card.update(snapshot: snapshot)
            case let .failure(error): self?.card.setConnection(ready: false, message: error.localizedDescription)
            }
        }
    }

    private func importToken() {
        do {
            try client.importTokenFromPasteboard()
            card.setConnection(ready: true, message: "Token 已导入")
            refresh()
        } catch {
            card.setConnection(ready: false, message: error.localizedDescription)
        }
    }

    private func importAddress() {
        do {
            try client.importAddressFromPasteboard()
            card.setConnection(ready: true, message: "API 地址已导入")
            refresh()
        } catch {
            card.setConnection(ready: false, message: error.localizedDescription)
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        timer?.invalidate()
    }
}

@main
enum LampRemoteApplication {
    static func main() {
        let application = NSApplication.shared
        let delegate = AppDelegate()
        application.delegate = delegate
        application.run()
        withExtendedLifetime(delegate) {}
    }
}
