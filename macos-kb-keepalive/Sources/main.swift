import AppKit

// 入口：菜单栏常驻应用（无 Dock 图标、无窗口）
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
app.delegate = delegate
app.run()
