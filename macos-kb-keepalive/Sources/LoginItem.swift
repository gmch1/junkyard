import Foundation
import AppKit
import ServiceManagement

/// 登录自启动：优先 SMAppService（原生机制），失败时回退 LaunchAgent plist
enum LoginItem {
    private static let plistURL = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/LaunchAgents/com.ming.kbkeepalive.plist")

    static var isEnabled: Bool {
        if SMAppService.mainApp.status == .enabled { return true }
        return FileManager.default.fileExists(atPath: plistURL.path)
    }

    /// 开关登录启动。异步执行（launchctl 子进程不阻塞主线程），结果以日志为准
    static func setEnabled(_ on: Bool) {
        DispatchQueue.global(qos: .userInitiated).async {
            apply(on)
        }
    }

    private static func apply(_ on: Bool) {
        if on {
            if SMAppService.mainApp.status == .enabled {
                AppLogger.shared.info("登录启动 → 开（已注册）")
                return
            }
            do {
                try SMAppService.mainApp.register()
            } catch {
                AppLogger.shared.warn("SMAppService 注册失败: \(error.localizedDescription)，回退 LaunchAgent")
                _ = installAgent()
                return
            }
            // register 成功不等于生效：requiresApproval 需要用户在系统设置里批准
            switch SMAppService.mainApp.status {
            case .enabled:
                AppLogger.shared.info("登录启动 → 开（SMAppService）")
            case .requiresApproval:
                AppLogger.shared.warn("登录启动需要批准：请在 系统设置 → 通用 → 登录项 中允许 KbKeepAlive")
                if let url = URL(string: "x-apple.systempreferences:com.apple.LoginItems-Settings.extension") {
                    NSWorkspace.shared.open(url)
                }
            default:
                AppLogger.shared.warn("SMAppService 注册后状态异常（raw \(SMAppService.mainApp.status.rawValue)），回退 LaunchAgent")
                _ = installAgent()
            }
        } else {
            removeAgent()
            try? SMAppService.mainApp.unregister()
            AppLogger.shared.info("登录启动 → 关")
        }
    }

    private static func xmlEscape(_ s: String) -> String {
        s.replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
            .replacingOccurrences(of: "\"", with: "&quot;")
    }

    private static func installAgent() -> Bool {
        let appPath = xmlEscape(Bundle.main.bundlePath)
        let plist = """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
            <key>Label</key><string>com.ming.kbkeepalive</string>
            <key>ProgramArguments</key><array><string>/usr/bin/open</string><string>-a</string><string>\(appPath)</string></array>
            <key>RunAtLoad</key><true/>
        </dict>
        </plist>
        """
        do {
            try FileManager.default.createDirectory(at: plistURL.deletingLastPathComponent(), withIntermediateDirectories: true)
            try plist.write(to: plistURL, atomically: true, encoding: .utf8)
        } catch {
            AppLogger.shared.warn("LaunchAgent 写入失败: \(error.localizedDescription)")
            return false
        }
        _ = run(["launchctl", "bootout", "gui/\(getuid())", plistURL.path])
        let ok = run(["launchctl", "bootstrap", "gui/\(getuid())", plistURL.path]) == 0
        if ok {
            AppLogger.shared.info("登录启动 → 开（LaunchAgent）")
        } else {
            AppLogger.shared.warn("LaunchAgent bootstrap 失败，登录启动未生效")
        }
        return ok
    }

    @discardableResult
    private static func removeAgent() -> Bool {
        guard FileManager.default.fileExists(atPath: plistURL.path) else { return true }
        _ = run(["launchctl", "bootout", "gui/\(getuid())", plistURL.path])
        try? FileManager.default.removeItem(at: plistURL)
        AppLogger.shared.info("登录启动 → 关（LaunchAgent 移除）")
        return true
    }

    private static func run(_ args: [String]) -> Int32 {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        p.arguments = args
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        do { try p.run(); p.waitUntilExit(); return p.terminationStatus } catch { return -1 }
    }
}
