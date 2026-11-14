// Draws the app icon: the menu bar's bell (white, its badge orange) on a
// blue rounded square, at every size macOS asks for, into AppIcon.iconset.
// `make mac-icon` turns that into AppIcon.icns (committed), so this only
// runs when the design changes. Needs only the Command Line Tools.
//
//   swift macos/Resources/make-icon.swift <out.iconset>
import AppKit

let out = URL(fileURLWithPath: CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "AppIcon.iconset")
try? FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)

/// One square image, `px` pixels wide, drawn on macOS's icon grid: an
/// 824/1024 rounded square with a soft shadow.
func render(_ px: Int) -> Data {
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8,
                               samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                               colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    let s = CGFloat(px) / 1024
    let tile = NSRect(x: 100 * s, y: 100 * s, width: 824 * s, height: 824 * s)
    let shape = NSBezierPath(roundedRect: tile, xRadius: 185 * s, yRadius: 185 * s)

    let shadow = NSShadow()
    shadow.shadowColor = NSColor.black.withAlphaComponent(0.3)
    shadow.shadowOffset = NSSize(width: 0, height: -10 * s)
    shadow.shadowBlurRadius = 24 * s
    NSGraphicsContext.saveGraphicsState()
    shadow.set()
    NSColor.black.setFill()
    shape.fill()
    NSGraphicsContext.restoreGraphicsState()

    let gradient = NSGradient(colors: [
        NSColor(srgbRed: 0.36, green: 0.55, blue: 1.00, alpha: 1),
        NSColor(srgbRed: 0.20, green: 0.27, blue: 0.86, alpha: 1),
    ])!
    gradient.draw(in: shape, angle: -90)

    let config = NSImage.SymbolConfiguration(pointSize: 430 * s, weight: .semibold)
        .applying(NSImage.SymbolConfiguration(paletteColors: [NSColor(srgbRed: 1, green: 0.62, blue: 0.2, alpha: 1), .white]))
    if let bell = NSImage(systemSymbolName: "bell.badge.fill", accessibilityDescription: nil)?
        .withSymbolConfiguration(config) {
        let size = bell.size
        let at = NSRect(x: (CGFloat(px) - size.width) / 2, y: (CGFloat(px) - size.height) / 2 - 8 * s,
                        width: size.width, height: size.height)
        bell.draw(in: at)
    }
    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for pt in [16, 32, 128, 256, 512] {
    for scale in [1, 2] {
        let name = scale == 1 ? "icon_\(pt)x\(pt).png" : "icon_\(pt)x\(pt)@2x.png"
        try render(pt * scale).write(to: out.appendingPathComponent(name))
    }
}
print("wrote \(out.path)")
