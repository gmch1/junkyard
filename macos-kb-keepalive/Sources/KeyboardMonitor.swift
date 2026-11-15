import Foundation
import AppKit
import ApplicationServices

/// 全局按键计数：CGEventTap 只监听 keyDown，只累加计数，不记录任何按键内容
/// 回调里 O(1) 操作（一次加法 + 字典计数），不分配大对象，不影响输入延迟
final class KeyboardMonitor {
    private let stats: StatsStore
    private var tap: CFMachPort?

    init(stats: StatsStore) {
        self.stats = stats
    }

    /// tap 存在且有效才算在计数（运行中被撤销授权时 tap 会失效，靠它自愈）
    var isCounting: Bool { tap.map { CFMachPortIsValid($0) } ?? false }

    var isAuthorized: Bool { AXIsProcessTrusted() }

    /// 弹出系统授权引导（辅助功能）
    func requestAuthorization() {
        let opts = [kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary
        _ = AXIsProcessTrustedWithOptions(opts)
    }

    func openAccessibilitySettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility") {
            NSWorkspace.shared.open(url)
        }
    }

    /// 创建事件监听；无权限时返回 false。tap 失效时会先清理再重建
    func start() -> Bool {
        if let t = tap {
            if CFMachPortIsValid(t) { return true }
            CFMachPortInvalidate(t)
            tap = nil
        }
        let mask = 1 << CGEventType.keyDown.rawValue
        guard let t = CGEvent.tapCreate(
            tap: .cgSessionEventTap,
            place: .headInsertEventTap,
            options: .defaultTap,
            eventsOfInterest: CGEventMask(mask),
            callback: kbTapCallback,
            userInfo: Unmanaged.passUnretained(self).toOpaque()
        ) else {
            return false
        }
        guard let src = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, t, 0) else { return false }
        // commonModes：菜单跟踪模式（NSEventTrackingRunLoopMode）下也要派发，
        // 否则菜单打开期间的按键全部漏计，mach port 积压还可能触发 tap 超时禁用
        CFRunLoopAddSource(CFRunLoopGetMain(), src, .commonModes)
        CGEvent.tapEnable(tap: t, enable: true)
        tap = t
        AppLogger.shared.info("按键计数已开启（监听所有键盘的 keyDown，只计数不记内容）")
        return true
    }

    fileprivate func handleKeyDown() {
        stats.increment()
    }

    fileprivate func reenable() {
        if let t = tap, CFMachPortIsValid(t) { CGEvent.tapEnable(tap: t, enable: true) }
    }
}

private func kbTapCallback(proxy: CGEventTapProxy, type: CGEventType, event: CGEvent,
                           refcon: UnsafeMutableRawPointer?) -> Unmanaged<CGEvent>? {
    guard let refcon else { return Unmanaged.passUnretained(event) }
    let monitor = Unmanaged<KeyboardMonitor>.fromOpaque(refcon).takeUnretainedValue()
    if type == .tapDisabledByTimeout || type == .tapDisabledByUserInput {
        monitor.reenable()
    } else if type == .keyDown {
        monitor.handleKeyDown()
    }
    return Unmanaged.passUnretained(event)
}
