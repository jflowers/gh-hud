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
        model = AppModel()
    }

    var body: some Scene {
        MenuBarExtra {
            MenuView(conn: model.conn)
        } label: {
            MenuLabel(conn: model.conn)
        }
        .menuBarExtraStyle(.window)
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
