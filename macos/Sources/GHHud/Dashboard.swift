import AppKit
import GHHudKit
import SwiftUI

/// What the window shows: a filter over the items, or one of the hub's
/// panes.
enum Pane: Hashable {
    case items(ItemFilter)
    case plan
    case agents
    case workspaces
    case fleet
}

/// The window's own state. Not @State (a macro only Xcode ships): see
/// GHHudApp.
@MainActor
final class DashboardState: ObservableObject {
    @Published var pane: Pane?
    @Published var query: String
    @Published var selection: Set<ItemRow.ID> = []
    @Published var refreshError: String?
    @Published var sheet: AgentRequest?
    @Published var notice: Notice?
    @Published var agentSelection: AgentRow.ID?
    @Published var workspaceSelection: WorkspaceRow.ID?
    @Published var showStale = false
    @Published var planSelection: PlanItem.ID?
    @Published var dropping: EpicRow?
    @Published var enrolling = false
    @Published var confirmSkipWait = false

    init(pane: Pane?, query: String) {
        self.pane = pane
        self.query = query
    }
}

/// The dashboard window: the TUI's tabs as a sidebar, the items as a table.
struct DashboardView: View {
    static let windowID = "dashboard"

    /// Opens (or brings forward) the window, and brings the app to the
    /// front: opened from the menu bar, it may not be.
    @MainActor
    static func open(_ openWindow: OpenWindowAction) {
        openWindow(id: windowID)
        NSApp.activate()
    }

    let conn: HubConnection
    @StateObject private var ui: DashboardState

    init(conn: HubConnection, state: DashboardState? = nil) {
        self.conn = conn
        _ui = StateObject(wrappedValue: state ?? DashboardState(pane: .items(.all), query: ""))
    }

    var body: some View {
        NavigationSplitView {
            Sidebar(conn: conn, ui: ui)
                .navigationSplitViewColumnWidth(min: 180, ideal: 210)
        } detail: {
            detail
        }
        .navigationTitle(title)
        .navigationSubtitle(subtitle)
        .toolbar {
            if case .items = ui.pane { itemTools }
            if ui.pane == .plan { PlanTools(conn: conn, ui: ui) }
            if ui.pane == .fleet { FleetTools(conn: conn, ui: ui) }
            if ui.pane == .agents {
                ToolbarItem {
                    Toggle(isOn: $ui.showStale) {
                        Label("Show Stale", systemImage: "clock.arrow.circlepath")
                    }
                    .help(ui.showStale ? "Hide sessions idle for over a day" : "Show sessions idle for over a day")
                }
            }
            ToolbarItem {
                Button {
                    Task { ui.refreshError = await conn.refresh(refreshTarget) }
                } label: {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .help(ui.refreshError ?? refreshHelp)
                .keyboardShortcut("r", modifiers: .command)
                .disabled(conn.status != .connected || refreshing)
            }
        }
        .frame(minWidth: 760, minHeight: 360)
        .sheet(item: $ui.sheet) { req in
            AgentSheet(conn: conn, req: req, ui: ui)
        }
    }

    @ToolbarContentBuilder
    private var itemTools: some ToolbarContent {
        ToolbarItemGroup {
            let cmds = ItemCommands(conn: conn, ui: ui)
            let one = selectedRow
            Button {
                if let one { cmds.startAgent(one) }
            } label: {
                Label("Start Agent", systemImage: "play.circle")
            }
            .help(cmds.off ?? (one == nil ? "Select an item to start an agent on" : "Start an agent on \(one!.ref)"))
            .keyboardShortcut("d", modifiers: .command)
            .disabled(one == nil || cmds.off != nil)
            Button {
                if let one { cmds.queue(one) }
            } label: {
                Label("Add to Queue", systemImage: "text.badge.plus")
            }
            .help(cmds.off ?? (one == nil ? "Select an item to queue" : "Add \(one!.ref) to your queue"))
            .keyboardShortcut("d", modifiers: [.command, .shift])
            .disabled(one == nil || cmds.off != nil)
        }
    }

    private var refreshTarget: Ghhud_V1_RefreshTarget {
        switch ui.pane {
        case .agents: return .agents
        case .workspaces: return .workspaces
        default: return .github
        }
    }

    private var refreshHelp: String {
        switch ui.pane {
        case .agents: return "Read the agent sessions and runs now"
        case .workspaces: return "Read every checkout now"
        default: return "Fetch GitHub now"
        }
    }

    private var refreshing: Bool {
        switch ui.pane {
        case .workspaces: return conn.snapshot.workspacesLoading
        case .agents: return false
        default: return conn.snapshot.fetching
        }
    }

    private var rows: [ItemRow] {
        ItemList.rows(conn.snapshot, filter: filter, query: ui.query)
    }

    /// The selected row, when exactly one is selected and shown.
    private var selectedRow: ItemRow? {
        guard ui.selection.count == 1 else { return nil }
        return rows.first { ui.selection.contains($0.id) }
    }

    private var filter: ItemFilter {
        if case .items(let f) = ui.pane { return f }
        return .all
    }

    private var title: String {
        switch ui.pane {
        case .plan: return "Plan"
        case .fleet: return "Fleet"
        case .agents: return "Agents"
        case .workspaces: return "Workspaces"
        default: return filter.title
        }
    }

    private var subtitle: String {
        switch conn.status {
        case .connecting: return "connecting to the hub…"
        case .connected: return HubSummary.line(conn.snapshot)
        case .noHub(let why), .lost(let why): return why
        }
    }

    @ViewBuilder
    private var detail: some View {
        if conn.status != .connected {
            ContentUnavailableView(subtitle, systemImage: "bolt.horizontal.circle")
        } else {
            pane
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    if let n = ui.notice {
                        NoticeBar(notice: n) { ui.notice = nil }
                    }
                }
        }
    }

    @ViewBuilder
    private var pane: some View {
        switch ui.pane {
        case .plan:
            PlanPane(conn: conn, ui: ui)
        case .fleet:
            FleetPane(conn: conn, ui: ui)
        case .agents:
            AgentsPane(conn: conn, ui: ui)
        case .workspaces:
            WorkspacesPane(conn: conn, ui: ui)
        default:
            let rows = rows
            ItemTable(rows: rows, cmds: ItemCommands(conn: conn, ui: ui), ui: ui)
                .searchable(text: $ui.query, placement: .toolbar, prompt: "Repo, title or #N")
                .overlay {
                    if rows.isEmpty {
                        if ui.query.isEmpty {
                            ContentUnavailableView("Nothing here", systemImage: "tray")
                        } else {
                            ContentUnavailableView.search(text: ui.query)
                        }
                    }
                }
        }
    }
}

/// The filters, with how many items each holds, and the hub's panes.
struct Sidebar: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some View {
        let counts = ItemList.counts(conn.snapshot)
        List(selection: $ui.pane) {
            Section("Items") {
                ForEach(ItemFilter.allCases) { f in
                    Label(f.shortTitle, systemImage: Self.icon(f))
                        .help(f.title)
                        .badge(counts[f] ?? 0)
                        .tag(Pane.items(f))
                }
            }
            if conn.snapshot.dispatch {
                Section("Hub") {
                    Label("Plan", systemImage: "list.number")
                        .help("What agents start next, your queue, cases and epics")
                        .badge(PlanList.starting(conn.snapshot))
                        .tag(Pane.plan)
                    Label("Agents", systemImage: "person.2")
                        .help("Agent sessions and runs")
                        .badge(AgentList.running(conn.snapshot))
                        .tag(Pane.agents)
                    Label("Workspaces", systemImage: "folder")
                        .help("Checkouts a dispatch could use")
                        .badge(WorkspaceList.ready(conn.snapshot))
                        .tag(Pane.workspaces)
                }
            }
            if conn.snapshot.hasFleet {
                Section("Fleet") {
                    Label("Rollout", systemImage: "shippingbox")
                        .help("The GitOps rollout across your machines")
                        .badge(Text(FleetList.progress(conn.snapshot)))
                        .tag(Pane.fleet)
                }
            }
        }
    }

    static func icon(_ f: ItemFilter) -> String {
        switch f {
        case .all: return "tray.full"
        case .review: return "eye"
        case .mine: return "person"
        case .issues: return "smallcircle.filled.circle"
        case .ci: return "gearshape.2"
        case .inbox: return "bell"
        case .ready: return "checkmark.seal"
        case .queue: return "sparkles"
        }
    }
}

/// The items: double-click (or Return) opens one on GitHub.
struct ItemTable: View {
    let rows: [ItemRow]
    let cmds: ItemCommands
    @ObservedObject var ui: DashboardState

    var body: some View {
        Table(rows, selection: $ui.selection) {
            TableColumn("") { r in MarkerDot(marker: r.marker) }
                .width(10)
            TableColumn("Kind") { r in KindBadge(kind: r.kind) }
                .width(min: 44, ideal: 52, max: 64)
            TableColumn("Status") { r in StatusCluster(row: r) }
                .width(min: 46, ideal: 50, max: 60)
            TableColumn("#") { r in
                Text(r.num > 0 ? "#\(r.num)" : "")
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
            .width(min: 40, ideal: 56, max: 80)
            TableColumn("Repo") { r in
                Text(r.repo).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
            }
            .width(min: 80, ideal: 120, max: 280)
            TableColumn("Title") { r in TitleCell(row: r) }
                .width(min: 160, ideal: 250)
            TableColumn("Threads") { r in ThreadsCell(row: r) }
                .width(min: 52, ideal: 56, max: 70)
            TableColumn("Last") { r in
                Text(r.lastActor).foregroundStyle(r.lastActor == "you" ? .secondary : .primary).lineLimit(1)
            }
            .width(min: 50, ideal: 80, max: 160)
            TableColumn("Age") { r in
                Text(r.updated.map { ItemList.age($0) } ?? "")
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
            .width(min: 36, ideal: 50, max: 70)
        }
        .contextMenu(forSelectionType: ItemRow.ID.self) { ids in
            let picked = rows.filter { ids.contains($0.id) }
            if picked.count == 1, let r = picked.first {
                cmds.menu(r)
                Divider()
            }
            Button(picked.count > 1 ? "Open \(picked.count) on GitHub" : "Open on GitHub") { open(picked) }
                .disabled(picked.isEmpty)
            Button(picked.count > 1 ? "Copy \(picked.count) Links" : "Copy Link") { copy(picked) }
                .disabled(picked.isEmpty)
        } primaryAction: { ids in
            open(rows.filter { ids.contains($0.id) })
        }
    }

    private func open(_ picked: [ItemRow]) {
        for r in picked {
            if let u = r.url { NSWorkspace.shared.open(u) }
        }
    }

    private func copy(_ picked: [ItemRow]) {
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setString(picked.map(\.id).joined(separator: "\n"), forType: .string)
    }
}

extension Tone {
    var color: Color {
        switch self {
        case .good: return .green
        case .bad: return .red
        case .warn: return .orange
        case .pending: return .yellow
        case .muted: return .secondary
        }
    }
}

struct MarkerDot: View {
    let marker: ItemRow.Marker?

    var body: some View {
        switch marker {
        case .ready:
            Image(systemName: "diamond.fill").foregroundStyle(.green).help("Ready for a maintainer")
        case .yourMove:
            Image(systemName: "diamond.fill").foregroundStyle(.yellow).help("Your move")
        case .needsYou:
            Image(systemName: "diamond.fill").foregroundStyle(.orange).help("An agent's next step waits on you")
        case nil:
            Color.clear
        }
    }
}

struct KindBadge: View {
    let kind: String

    var body: some View {
        Text(kind)
            .font(.caption.weight(.medium))
            .foregroundStyle(color)
    }

    private var color: Color {
        switch kind {
        case "mine": return .blue
        case "review": return .yellow
        case "issue": return .green
        case "ci": return .red
        default: return .purple
        }
    }
}

/// CI, review and merge, as the TUI's state cluster.
struct StatusCluster: View {
    let row: ItemRow

    var body: some View {
        if row.isPR {
            HStack(spacing: 3) {
                glyph(ciSymbol, row.ci, help: "CI")
                glyph(reviewSymbol, row.review, help: "Review")
                if row.conflict {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(.orange)
                        .help("Merge conflict")
                }
            }
            .imageScale(.small)
        }
    }

    private var ciSymbol: String {
        switch row.ci {
        case .good: return "checkmark.circle.fill"
        case .bad: return "xmark.circle.fill"
        case .pending: return "circle.dotted.circle"
        default: return "circle"
        }
    }

    private var reviewSymbol: String {
        switch row.review {
        case .good: return "hand.thumbsup.fill"
        case .bad, .warn: return "exclamationmark.bubble.fill"
        case .pending: return "bubble.left.fill"
        default: return "bubble.left"
        }
    }

    @ViewBuilder
    private func glyph(_ symbol: String, _ tone: Tone?, help: String) -> some View {
        if let tone {
            Image(systemName: symbol).foregroundStyle(tone.color).help(help)
        } else {
            Image(systemName: symbol).hidden()
        }
    }
}

struct TitleCell: View {
    let row: ItemRow

    var body: some View {
        HStack(spacing: 4) {
            if let run = row.run {
                Text(run.text())
                    .font(.caption.weight(.medium))
                    .foregroundStyle(run.tone.color)
                    .help("\(run.task) \(run.words)")
            }
            if let st = row.stage {
                Text(st.text).font(.caption).foregroundStyle(st.tone.color)
            }
            if row.draft {
                Text("draft").font(.caption).foregroundStyle(.secondary)
            }
            Text(row.title).lineLimit(1)
            ForEach(row.labels.prefix(3), id: \.self) { l in
                Text(l)
                    .font(.caption2)
                    .padding(.horizontal, 4)
                    .background(.quaternary, in: Capsule())
                    .lineLimit(1)
            }
        }
        .help(row.author.isEmpty ? row.ref : "\(row.ref) by \(row.author)")
    }
}

/// Unresolved threads: people's in yellow, bots' dimmed.
struct ThreadsCell: View {
    let row: ItemRow

    var body: some View {
        if row.threads > 0 {
            Label("\(row.threads)", systemImage: "text.bubble")
                .foregroundStyle(.yellow)
                .labelStyle(.titleAndIcon)
                .help("Unresolved threads from people")
        } else if row.botThreads > 0 {
            Label("\(row.botThreads)", systemImage: "text.bubble")
                .foregroundStyle(.secondary)
                .help("Unresolved threads from bots")
        }
    }
}
