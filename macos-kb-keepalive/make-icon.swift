// make-icon.swift —— 生成 AppIcon.icns
// 设计：深色 squircle 底 + 白色键盘 + 绿色盾牌（白色心跳线，寓意"保持活着"）
// 用法：swift make-icon.swift
import AppKit

let outputs: [(name: String, px: Int)] = [
    ("icon_16x16.png", 16), ("icon_16x16@2x.png", 32),
    ("icon_32x32.png", 32), ("icon_32x32@2x.png", 64),
    ("icon_128x128.png", 128), ("icon_128x128@2x.png", 256),
    ("icon_256x256.png", 256), ("icon_256x256@2x.png", 512),
    ("icon_512x512.png", 512), ("icon_512x512@2x.png", 1024),
]

func srgb(_ r: Double, _ g: Double, _ b: Double, _ a: Double = 1) -> CGColor {
    CGColor(srgbRed: r, green: g, blue: b, alpha: a)
}

func drawIcon(size s: CGFloat) -> NSImage {
    let img = NSImage(size: NSSize(width: s, height: s))
    img.lockFocus()
    let ctx = NSGraphicsContext.current!.cgContext

    // ── 背景 squircle（macOS 图标网格：824/1024，圆角 185/1024）──
    let inset = s * 0.0977
    let radius = s * 0.1807
    let bgRect = CGRect(x: inset, y: inset, width: s - 2 * inset, height: s - 2 * inset)
    let bgPath = CGPath(roundedRect: bgRect, cornerWidth: radius, cornerHeight: radius, transform: nil)
    ctx.saveGState()
    ctx.addPath(bgPath)
    ctx.clip()
    let bgGrad = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB)!,
                            colors: [srgb(0.24, 0.26, 0.38), srgb(0.12, 0.13, 0.21)] as CFArray,
                            locations: [0, 1])!
    ctx.drawLinearGradient(bgGrad, start: CGPoint(x: s / 2, y: s), end: CGPoint(x: s / 2, y: 0), options: [])
    ctx.restoreGState()

    // ── 键盘本体（整体左移 3%，为右下角盾牌留出视觉平衡）──
    let kbW = s * 0.58, kbH = s * 0.40
    let kbRect = CGRect(x: (s - kbW) / 2 - s * 0.03, y: s * 0.55 - kbH / 2, width: kbW, height: kbH)
    let kbPath = CGPath(roundedRect: kbRect, cornerWidth: kbH * 0.10, cornerHeight: kbH * 0.10, transform: nil)
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -s * 0.012), blur: s * 0.02,
                  color: srgb(0, 0, 0, 0.35))
    ctx.addPath(kbPath)
    ctx.setFillColor(srgb(0.91, 0.93, 0.96))
    ctx.fillPath()
    ctx.restoreGState()
    ctx.addPath(kbPath)
    ctx.setStrokeColor(srgb(0.72, 0.75, 0.82))
    ctx.setLineWidth(s * 0.006)
    ctx.strokePath()

    // 按键：两行 6 键 + 底行（功能键 / 空格 / 方向键）
    let keysRect = kbRect.insetBy(dx: kbW * 0.055, dy: kbH * 0.13)
    let gap = kbW * 0.022
    let cols = 6
    let keyW = (keysRect.width - gap * CGFloat(cols - 1)) / CGFloat(cols)
    let rowH = (keysRect.height - gap * 2) / 3
    let keyColor = srgb(0.20, 0.22, 0.29)
    for r in 0..<2 {
        for c in 0..<cols {
            let x = keysRect.minX + CGFloat(c) * (keyW + gap)
            let y = keysRect.maxY - rowH - CGFloat(r) * (rowH + gap)
            let kp = CGPath(roundedRect: CGRect(x: x, y: y, width: keyW, height: rowH),
                            cornerWidth: rowH * 0.28, cornerHeight: rowH * 0.28, transform: nil)
            ctx.addPath(kp)
            ctx.setFillColor(keyColor)
            ctx.fillPath()
        }
    }
    // 底行：一个居中的宽空格键（盾牌压角时视觉更干净）
    let spaceW = keysRect.width * 0.72
    let spaceX = keysRect.minX + (keysRect.width - spaceW) / 2
    let sp2 = CGPath(roundedRect: CGRect(x: spaceX, y: keysRect.minY, width: spaceW, height: rowH),
                     cornerWidth: rowH * 0.28, cornerHeight: rowH * 0.28, transform: nil)
    ctx.addPath(sp2)
    ctx.setFillColor(keyColor)
    ctx.fillPath()

    // ── 绿色盾牌（压在键盘右下角）──
    let sh = s * 0.32
    let shieldRect = CGRect(x: kbRect.maxX - sh * 0.70, y: kbRect.minY - sh * 0.24, width: sh, height: sh)
    let cx = shieldRect.midX, left = shieldRect.minX, right = shieldRect.maxX
    let top = shieldRect.maxY, bottom = shieldRect.minY
    let sp = CGMutablePath()
    sp.move(to: CGPoint(x: cx, y: top))
    sp.addLine(to: CGPoint(x: right, y: top - sh * 0.14))
    sp.addLine(to: CGPoint(x: right, y: top - sh * 0.52))
    sp.addLine(to: CGPoint(x: cx, y: bottom))
    sp.addLine(to: CGPoint(x: left, y: top - sh * 0.52))
    sp.addLine(to: CGPoint(x: left, y: top - sh * 0.14))
    sp.closeSubpath()
    let shGrad = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB)!,
                            colors: [srgb(0.30, 0.84, 0.45), srgb(0.12, 0.62, 0.30)] as CFArray,
                            locations: [0, 1])!
    ctx.saveGState()
    ctx.addPath(sp)
    ctx.clip()
    ctx.setShadow(offset: CGSize(width: 0, height: -s * 0.008), blur: s * 0.018,
                  color: srgb(0, 0, 0, 0.40))
    ctx.drawLinearGradient(shGrad, start: CGPoint(x: cx, y: top), end: CGPoint(x: cx, y: bottom), options: [])
    ctx.restoreGState()

    // ── 心跳线（"保持活着"）──
    let band = shieldRect.insetBy(dx: sh * 0.17, dy: sh * 0.30)
    let pts: [(Double, Double)] = [(0.02, 0.50), (0.30, 0.50), (0.40, 0.68), (0.53, 0.22), (0.66, 0.50), (0.98, 0.50)]
    let cgPts = pts.map { CGPoint(x: band.minX + $0.0 * band.width, y: band.maxY - $0.1 * band.height) }
    ctx.setStrokeColor(srgb(1, 1, 1))
    ctx.setLineWidth(sh * 0.085)
    ctx.setLineCap(.round)
    ctx.setLineJoin(.round)
    ctx.strokeLineSegments(between: cgPts)

    img.unlockFocus()
    return img
}

func savePNG(_ image: NSImage, px: Int, to url: URL) {
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px,
                               bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                               colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    rep.size = NSSize(width: px, height: px)
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    image.draw(in: NSRect(x: 0, y: 0, width: CGFloat(px), height: CGFloat(px)))
    NSGraphicsContext.restoreGraphicsState()
    try! rep.representation(using: .png, properties: [:])!.write(to: url)
}

let fm = FileManager.default
try? fm.removeItem(atPath: "AppIcon.iconset")
try! fm.createDirectory(atPath: "AppIcon.iconset", withIntermediateDirectories: true)
for (name, px) in outputs {
    savePNG(drawIcon(size: CGFloat(px)), px: px, to: URL(fileURLWithPath: "AppIcon.iconset/\(name)"))
}
print("iconset 生成完毕 (\(outputs.count) 个尺寸)")
