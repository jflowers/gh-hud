import AppKit
import GHHudKit
import SwiftUI

/// The menu bar app: what needs you, from the hub, at a glance.
@main
struct GHHudApp: App {
    // Not @State: SwiftUI's @State is a macro that only Xcode ships, and
    // this app builds with the Command Line Tools. The App is created once.
    private let model: AppModel

    init() {
        SnapshotMode.runIfAsked() // never returns when asked
        SnapshotMode.windowIfAsked() // nor this
        model = AppModel()
    }

    var body: some Scene {
        MenuBarExtra {
            MenuView(conn: model.conn)
        } label: {
            MenuLabel(conn: model.conn)
        }
        .menuBarExtraStyle(.window)

        Window("gh-hud", id: DashboardView.windowID) {
            DashboardView(conn: model.conn)
        }
        .defaultSize(width: 1100, height: 640)
    }
}

/// `GHHud --snapshot out.png` renders the menu once the hub answers, then
/// exits: for screenshots and checking the app without clicking.
enum SnapshotMode {
    @MainActor
    static func runIfAsked() {
        let args = CommandLine.arguments
        guard let i = args.firstIndex(of: "--snapshot"), i + 1 < args.count else { return }
        let out = URL(fileURLWithPath: args[i + 1])
        let conn = HubConnection()
        conn.start()
        Task { @MainActor in
            for _ in 0..<150 where conn.status != .connected {
                try? await Task.sleep(for: .milliseconds(100))
            }
            let view = MenuView(conn: conn, rendering: true).background(Color(nsColor: .windowBackgroundColor))
            let r = ImageRenderer(content: view)
            r.scale = 2
            guard let img = r.nsImage, let tiff = img.tiffRepresentation,
                  let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:])
            else {
                FileHandle.standardError.write(Data("snapshot: render failed\n".utf8))
                exit(1)
            }
            do {
                try png.write(to: out)
            } catch {
                FileHandle.standardError.write(Data("snapshot: \(error)\n".utf8))
                exit(1)
            }
            print("wrote \(out.path) (\(conn.status))")
            exit(0)
        }
        RunLoop.main.run()
    }
}

extension SnapshotMode {
    /// `GHHud --snapshot-window out.png [--filter review] [--search text]
    /// [--select N [--agent | --queue]]` opens the dashboard window, waits
    /// for the hub, and draws the window (the real AppKit views, table and
    /// toolbar included) to a PNG. --select selects the row numbered N;
    /// --agent or --queue then opens its sheet, drawn to out-sheet.png.
    @MainActor
    static func windowIfAsked() {
        let args = CommandLine.arguments
        func arg(_ name: String) -> String? {
            guard let i = args.firstIndex(of: name), i + 1 < args.count else { return nil }
            return args[i + 1]
        }
        guard let path = arg("--snapshot-window") else { return }
        let out = URL(fileURLWithPath: path)
        let filter = arg("--filter").flatMap(ItemFilter.init(rawValue:)) ?? .all
        let conn = HubConnection()
        conn.start()
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
        let ui = DashboardState(pane: .items(filter), query: arg("--search") ?? "")
        let host = NSHostingController(rootView: DashboardView(conn: conn, state: ui))
        host.sceneBridgingOptions = [.toolbars, .title]
        let win = NSWindow(contentViewController: host)
        win.styleMask = [.titled, .closable, .resizable, .miniaturizable, .fullSizeContentView]
        win.setContentSize(NSSize(width: 1100, height: 520))
        win.makeKeyAndOrderFront(nil)
        Task { @MainActor in
            for _ in 0..<150 where conn.status != .connected {
                try? await Task.sleep(for: .milliseconds(100))
            }
            if let n = arg("--select").flatMap(Int.init),
               let row = ItemList.rows(conn.snapshot, filter: filter, query: ui.query).first(where: { $0.num == n }) {
                ui.selection = [row.id]
                if args.contains("--agent") || args.contains("--queue") {
                    ui.sheet = AgentRequest(row: row, queue: args.contains("--queue"))
                }
            }
            try? await Task.sleep(for: .milliseconds(1200)) // let the table (and sheet) lay out
            if let sheet = win.attachedSheet, let view = sheet.contentView?.superview,
               let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) {
                view.cacheDisplay(in: view.bounds, to: rep)
                let sheetOut = out.deletingPathExtension().appendingPathExtension("sheet.png")
                try? rep.representation(using: .png, properties: [:])?.write(to: sheetOut)
                print("wrote \(sheetOut.path)")
            }
            guard let frame = win.contentView?.superview,
                  let rep = frame.bitmapImageRepForCachingDisplay(in: frame.bounds)
            else {
                FileHandle.standardError.write(Data("snapshot: no window to draw\n".utf8))
                exit(1)
            }
            frame.cacheDisplay(in: frame.bounds, to: rep)
            do {
                try rep.representation(using: .png, properties: [:])?.write(to: out)
            } catch {
                FileHandle.standardError.write(Data("snapshot: \(error)\n".utf8))
                exit(1)
            }
            print("wrote \(out.path) (\(conn.status))")
            exit(0)
        }
        app.run()
        exit(0)
    }
}

/// Owns the hub connection and the notifications, for the app's lifetime.
@MainActor
final class AppModel {
    let conn = HubConnection()
    private let notifier = Notifier()

    init() {
        conn.onAlert = { [notifier] alert in notifier.post(alert) }
        conn.start()
        // Once you allow notifications, the app shows the hub's alerts and
        // the hub stops posting its own.
        notifier.authorize { [conn] granted in conn.setNativeNotifications(granted) }
    }
}

/// The menu bar icon: a count when something needs you.
struct MenuLabel: View {
    let conn: HubConnection

    var body: some View {
        switch conn.status {
        case .connected where conn.attention.count > 0:
            HStack(spacing: 2) {
                Image(systemName: "bell.badge")
                Text("\(conn.attention.count)")
            }
        case .connected:
            Image(systemName: "checkmark.circle")
        default:
            Image(systemName: "bolt.horizontal.circle")
        }
    }
}
