import AppKit
import GHHudKit
import SwiftUI

/// The app: what needs you, from the hub, at a glance in the menu bar, and
/// the dashboard in a window. It's a regular app, in the Dock and ⌘-Tab.
@main
struct GHHudApp: App {
    // Not @State: SwiftUI's @State is a macro that only Xcode ships, and
    // this app builds with the Command Line Tools. The App is created once.
    private let model: AppModel
    @NSApplicationDelegateAdaptor private var delegate: AppDelegate

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

        Settings {
            HubSettingsView(conn: model.conn)
        }
    }
}

/// `GHHud --snapshot out.png` renders the menu once the hub answers, then
/// exits: for screenshots and checking the app without clicking.
enum SnapshotMode {
    /// This Mac's hub, or with --hub-url and --hub-token one on another
    /// Mac (the saved settings aren't used, so snapshots are repeatable).
    static var source: HubSource {
        let args = CommandLine.arguments
        func value(_ flag: String) -> String? {
            args.firstIndex(of: flag).flatMap { $0 + 1 < args.count ? args[$0 + 1] : nil }
        }
        if let url = value("--hub-url") {
            return .remote(url: url, token: value("--hub-token") ?? "")
        }
        return .thisMac
    }

    @MainActor
    static func runIfAsked() {
        let args = CommandLine.arguments
        guard let i = args.firstIndex(of: "--snapshot"), i + 1 < args.count else { return }
        let out = URL(fileURLWithPath: args[i + 1])
        let conn = HubConnection(source: SnapshotMode.source)
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
    /// `--pane plan|agents|workspaces|fleet|toolkit [--pick N]` shows that pane instead, with
    /// its Nth row (from 0) selected; --enroll opens the Enroll Epic sheet.
    /// --oldest-first sorts the items by their Age column, oldest first.
    /// --search goes to the shown table's search field; --keep then keeps
    /// its column terms as filter chips, as Return does. --live-only turns
    /// on the Agents pane's Live Only. --release TOOL opens the Toolkit pane's
    /// release notes for TOOL. --url REF opens Start Agent on URL
    /// with REF looked up ("" for the empty sheet).
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
        let conn = HubConnection(source: SnapshotMode.source)
        conn.start()
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
        let pane: Pane = switch arg("--pane") {
        case "plan": .plan
        case "fleet": .fleet
        case "toolkit": .toolkit
        case "agents": .agents
        case "workspaces": .workspaces
        default: .items(filter)
        }
        let search = arg("--search") ?? ""
        let ui = DashboardState(pane: pane, query: "")
        switch pane {
        case .agents: ui.agentQuery = search
        case .workspaces: ui.workspaceQuery = search
        case .fleet: ui.fleetQuery = search
        default: ui.query = search
        }
        if args.contains("--keep") {
            switch pane {
            case .agents: ui.agentQuery = ui.agentFilters.keepTerms(of: search, columns: AgentRow.filterColumns)
            case .workspaces:
                ui.workspaceQuery = ui.workspaceFilters.keepTerms(of: search, columns: WorkspaceRow.filterColumns)
            case .fleet: ui.fleetQuery = ui.fleetFilters.keepTerms(of: search, columns: FleetRow.filterColumns)
            default: ui.query = ui.itemFilters.keepTerms(of: search, columns: ItemRow.filterColumns)
            }
        }
        if args.contains("--oldest-first") { ui.itemSort = [KeyPathComparator(\.ageKey, order: .reverse)] }
        let host = NSHostingController(rootView: DashboardView(conn: conn, state: ui))
        host.sceneBridgingOptions = [.toolbars, .title]
        let win = NSWindow(contentViewController: host)
        win.styleMask = [.titled, .closable, .resizable, .miniaturizable, .fullSizeContentView]
        win.setContentSize(NSSize(width: 1100, height: pane == .items(filter) ? 520 : 680))
        win.makeKeyAndOrderFront(nil)
        Task { @MainActor in
            for _ in 0..<150 where conn.status != .connected {
                try? await Task.sleep(for: .milliseconds(100))
            }
            if args.contains("--enroll") { ui.enrolling = true }
            if args.contains("--live-only") { ui.liveOnly = true }
            if let tool = arg("--release") { ui.release = tool }
            if let ref = arg("--url") { ui.sheet = AgentRequest(named: ref) }
            if let n = arg("--pick").flatMap(Int.init) {
                if pane == .plan {
                    let rows = PlanList.groups(conn.snapshot).flatMap(\.rows)
                    if n < rows.count { ui.planSelection = rows[n].id }
                } else if pane == .agents {
                    let rows = AgentList.rows(conn.snapshot, showStale: false)
                    if n < rows.count { ui.agentSelection = rows[n].id }
                } else if pane == .toolkit {
                    let rows = ToolkitList.rollouts(conn.snapshot)
                    if n < rows.count { ui.rolloutSelection = rows[n].id }
                } else if pane == .workspaces {
                    let rows = WorkspaceList.rows(conn.snapshot)
                    if n < rows.count { ui.workspaceSelection = rows[n].id }
                } else if pane == .fleet {
                    let rows = FleetList.rows(conn.snapshot)
                    if n < rows.count { ui.fleetSelection = rows[n].id }
                }
            }
            if let n = arg("--select").flatMap(Int.init),
               let row = ItemList.rows(conn.snapshot, filter: filter, query: ui.query, kept: ui.itemFilters).first(where: { $0.num == n }) {
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
    let conn = HubConnection(source: HubSettings.source)
    private let notifier = Notifier()

    init() {
        conn.onAlert = { [notifier] alert in notifier.post(alert) }
        conn.start()
        // Once you allow notifications, the app shows the hub's alerts and
        // the hub stops posting its own.
        notifier.authorize { [conn] granted in conn.setNativeNotifications(granted) }
    }
}

/// Opens the dashboard window from outside SwiftUI's views (the app
/// delegate). SwiftUI only hands openWindow to views, so the menu bar label,
/// the one view that exists from launch, passes it on.
@MainActor
final class DashboardOpener: ObservableObject {
    static let shared = DashboardOpener()
    private var openWindow: OpenWindowAction?
    /// A pane to show, taken by the dashboard once it's open.
    @Published var pane: Pane?

    /// Takes openWindow from a view; the first time, at launch, it opens
    /// the dashboard.
    func take(_ action: OpenWindowAction) {
        let first = openWindow == nil
        openWindow = action
        if first { open() }
    }

    func open() {
        if let openWindow { DashboardView.open(openWindow) }
    }

    /// Opens the dashboard on a pane.
    func open(_ p: Pane) {
        pane = p
        open()
    }
}

/// Clicking the Dock icon with no window showing opens the dashboard.
final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows: Bool) -> Bool {
        if !hasVisibleWindows {
            MainActor.assumeIsolated { DashboardOpener.shared.open() }
        }
        return true
    }
}

/// The menu bar icon: a count when something needs you.
struct MenuLabel: View {
    let conn: HubConnection
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        icon.onAppear { DashboardOpener.shared.take(openWindow) }
    }

    @ViewBuilder
    private var icon: some View {
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
