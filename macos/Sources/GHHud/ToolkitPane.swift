import AppKit
import GHHudKit
import SwiftUI

// The Toolkit pane: the TUI's T view. The tools the fleet pins above (◆
// when a release waits for your go-ahead), each repo's scaffold against
// the pins under them, the rollouts below, and the selected rollout's
// repos beside them.

struct ToolkitPane: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some View {
        let tools = ToolkitList.tools(conn.snapshot)
        let rollouts = ToolkitList.rollouts(conn.snapshot)
        let picked = rollouts.first { $0.id == ui.rolloutSelection } ?? rollouts.first
        let drift = ToolkitList.drift(conn.snapshot)
        VStack(alignment: .leading, spacing: 0) {
            header
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
            Divider()
            Table(tools, selection: $ui.toolSelection) {
                TableColumn("") { t in
                    Text(t.mark).foregroundStyle(t.tone.color).help(t.mark == "◆" ? "A release waits for your go-ahead" : "")
                }
                .width(14)
                TableColumn("Tool") { t in Text(t.name) }
                    .width(min: 60, ideal: 110, max: 180)
                TableColumn("Pin") { t in Text(t.pin).font(.body.monospaced()) }
                    .width(min: 50, ideal: 80, max: 120)
                TableColumn("Latest") { t in Text(t.latest).font(.body.monospaced()).foregroundStyle(t.tone.color) }
                    .width(min: 50, ideal: 80, max: 120)
                TableColumn("Repos") { t in Text(t.repos).foregroundStyle(.secondary) }
                    .width(min: 80, ideal: 150, max: 220)
                TableColumn("") { t in
                    if !t.error.isEmpty {
                        Text(t.error).foregroundStyle(.red).lineLimit(1).help(t.error)
                    } else if let r = t.rolling {
                        Text("rolling out \(r)").foregroundStyle(.secondary)
                    }
                }
            }
            .contextMenu(forSelectionType: ToolRow.ID.self) { ids in
                if let t = tools.first(where: { ids.contains($0.id) }) {
                    toolMenu(t)
                }
            } primaryAction: { ids in
                if let t = tools.first(where: { ids.contains($0.id) }), t.proposal != nil {
                    ui.release = t.name
                }
            }
            .frame(height: CGFloat(34 + 24 * max(tools.count, 1)))
            if !drift.isEmpty || !ToolkitList.driftState(conn.snapshot).isEmpty {
                Divider()
                driftTable(drift)
            }
            Divider()
            HSplitView {
                Table(rollouts, selection: $ui.rolloutSelection) {
                    TableColumn("") { r in StatusGlyph(tone: r.tone, active: r.stage != "cancelled") }
                        .width(12)
                    TableColumn("Rollout") { r in Text(r.id) }
                        .width(min: 80, ideal: 120, max: 200)
                    TableColumn("Stage") { r in Text(r.stage).foregroundStyle(r.tone.color) }
                        .width(min: 50, ideal: 70, max: 100)
                    TableColumn("Repos") { r in
                        Text(r.counts.isEmpty ? r.summary : r.counts).foregroundStyle(.secondary).lineLimit(1)
                            .help(r.error.isEmpty ? r.summary : r.error)
                    }
                }
                .contextMenu(forSelectionType: RolloutRow.ID.self) { ids in
                    if let r = rollouts.first(where: { ids.contains($0.id) }) {
                        rolloutMenu(r)
                    }
                }
                .overlay {
                    if rollouts.isEmpty { Text("No rollouts").foregroundStyle(.secondary) }
                }
                .frame(minWidth: 260, idealWidth: 360)
                targets(picked)
                    .frame(minWidth: 300)
            }
        }
        .sheet(item: Binding(get: { ui.release.map(ReleaseName.init) },
                             set: { ui.release = $0?.id; ui.releaseError = nil })) { name in
            ReleaseSheet(conn: conn, ui: ui, tool: name.id)
        }
        .confirmationDialog(ui.confirmRollOut.map { "Roll out \($0.name) \($0.latest)?" } ?? "",
                            isPresented: Binding(get: { ui.confirmRollOut != nil }, set: { if !$0 { ui.confirmRollOut = nil } }),
                            presenting: ui.confirmRollOut) { t in
            Button("Roll Out") { ToolkitTools.rollOut(conn: conn, ui: ui, t) }
        } message: { t in
            Text("Opens the dotfiles PR that pins \(t.name) at \(t.latest) (the fleet is on \(t.pin)), then re-scaffolds its repos, one PR each.")
        }
        .confirmationDialog(ui.confirmFix.map { "Fix \($0.target)?" } ?? "",
                            isPresented: Binding(get: { ui.confirmFix != nil }, set: { if !$0 { ui.confirmFix = nil } }),
                            presenting: ui.confirmFix) { d in
            Button("Open Scaffold PR") { ToolkitTools.fix(conn: conn, ui: ui, d) }
        } message: { d in
            Text("Re-scaffolds \(d.target) at the fleet's pins and opens a PR with what changes: \(d.files.count) file(s).")
        }
    }

    @ViewBuilder
    private func driftTable(_ rows: [DriftRow]) -> some View {
        HStack(spacing: 8) {
            Text("Scaffold drift").font(.headline)
            Text(ToolkitList.driftState(conn.snapshot))
                .foregroundStyle(conn.snapshot.toolkit.driftError.isEmpty ? Color.secondary : .red)
                .lineLimit(1)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 6)
        Table(rows, selection: $ui.driftSelection) {
            TableColumn("") { d in StatusGlyph(tone: d.tone, active: true) }
                .width(12)
            TableColumn("Repo") { d in Text(d.target) }
                .width(min: 100, ideal: 200, max: 280)
            TableColumn("State") { d in Text(d.state).foregroundStyle(d.tone.color) }
                .width(min: 60, ideal: 110, max: 160)
            TableColumn("") { d in
                if let url = d.fix {
                    Link(url.lastPathComponent.isEmpty ? "PR" : "PR #\(url.lastPathComponent)", destination: url)
                } else {
                    Text(d.note).foregroundStyle(.secondary).lineLimit(1)
                        .help(([d.note] + d.files + d.stale.map { "\($0) (the init skips it)" }).joined(separator: "\n"))
                }
            }
        }
        .contextMenu(forSelectionType: DriftRow.ID.self) { ids in
            if let d = rows.first(where: { ids.contains($0.id) }) {
                if d.fixable { Button("Fix…") { ui.confirmFix = d } }
                if let url = d.fix { Button("Open Fix PR") { NSWorkspace.shared.open(url) } }
                if !d.files.isEmpty || !d.stale.isEmpty {
                    Button("Copy Files") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString((d.files + d.stale).joined(separator: "\n"), forType: .string)
                    }
                }
            }
        } primaryAction: { ids in
            if let d = rows.first(where: { ids.contains($0.id) }) {
                if let url = d.fix { NSWorkspace.shared.open(url) } else if d.fixable { ui.confirmFix = d }
            }
        }
        .overlay {
            if rows.isEmpty { Text("Not checked yet").foregroundStyle(.secondary) }
        }
        .frame(height: CGFloat(34 + 24 * min(max(rows.count, 1), 8)))
    }

    @ViewBuilder
    private var header: some View {
        let t = conn.snapshot.toolkit
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text("Tools").font(.title3.weight(.semibold))
                Text(ToolkitList.state(conn.snapshot)).foregroundStyle(t.error.isEmpty ? Color.secondary : .red)
            }
        }
    }

    @ViewBuilder
    private func targets(_ r: RolloutRow?) -> some View {
        let rows = r?.targets ?? []
        VStack(alignment: .leading, spacing: 0) {
            if let r {
                HStack(spacing: 8) {
                    Text(r.id).font(.headline)
                    Text(r.summary).foregroundStyle(.secondary).lineLimit(1).help(r.summary)
                    Spacer()
                    if let pr = r.pinPR {
                        Link("Pin PR", destination: pr)
                    }
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                if !r.error.isEmpty {
                    Label(r.error, systemImage: "xmark.octagon.fill").foregroundStyle(.red)
                        .textSelection(.enabled).padding(.horizontal, 10).padding(.bottom, 6)
                }
            }
            Table(rows) {
                TableColumn("") { t in StatusGlyph(tone: t.tone, active: true) }
                    .width(12)
                TableColumn("Repo") { t in Text(t.target) }
                    .width(min: 100, ideal: 200)
                TableColumn("State") { t in Text(t.state).foregroundStyle(t.tone.color) }
                    .width(min: 50, ideal: 80, max: 110)
                TableColumn("") { t in
                    if let url = t.url {
                        Link(url.lastPathComponent.isEmpty ? "PR" : "PR #\(url.lastPathComponent)", destination: url)
                    } else {
                        Text(t.note).foregroundStyle(.secondary).lineLimit(1).help(t.note).textSelection(.enabled)
                    }
                }
            }
            .contextMenu(forSelectionType: TargetRow.ID.self) { ids in
                if let t = rows.first(where: { ids.contains($0.id) }), let url = t.url {
                    Button("Open PR") { NSWorkspace.shared.open(url) }
                }
            } primaryAction: { ids in
                if let url = rows.first(where: { ids.contains($0.id) })?.url {
                    NSWorkspace.shared.open(url)
                }
            }
            .overlay {
                if r == nil { Text("Select a rollout").foregroundStyle(.secondary) }
            }
        }
    }

    @ViewBuilder
    private func toolMenu(_ t: ToolRow) -> some View {
        if t.proposal != nil {
            Button("Release Notes…") { ui.release = t.name }
        }
        if t.canRollOut {
            Button("Roll Out \(t.latest)…") { ui.confirmRollOut = t }
        }
    }

    @ViewBuilder
    private func rolloutMenu(_ r: RolloutRow) -> some View {
        if r.canCancel { Button("Cancel Rollout") { ToolkitTools.control(conn: conn, ui: ui, .cancel, r) } }
        if r.canRetry { Button("Retry") { ToolkitTools.control(conn: conn, ui: ui, .retry, r) } }
        if r.canDismiss { Button("Dismiss") { ToolkitTools.control(conn: conn, ui: ui, .dismiss, r) } }
    }
}

/// A tool's name, as a sheet's item.
struct ReleaseName: Identifiable {
    let id: String
}

/// A proposed release: its notes, with Approve and Decline.
struct ReleaseSheet: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState
    let tool: String
    @Environment(\.dismiss) private var dismiss
    private var busy: Bool { ui.releaseBusy }

    var body: some View {
        let p = ToolkitList.proposal(conn.snapshot, tool: tool)
        VStack(alignment: .leading, spacing: 12) {
            if let p {
                Text("\(p.tool) \(p.version)").font(.title2.weight(.semibold))
                Text(p.from.isEmpty ? "A new release." : "A new release; the fleet runs \(p.from).")
                    .foregroundStyle(.secondary)
                ScrollView {
                    Text(Self.notes(p.notes))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(minHeight: 160, maxHeight: 360)
                .padding(8)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
                if let url = URL(string: p.notesURL), !p.notesURL.isEmpty {
                    Link("Release page", destination: url)
                }
            } else {
                Text("\(tool) has no release waiting.").foregroundStyle(.secondary)
            }
            if let error = ui.releaseError { Text(error).foregroundStyle(.red).font(.caption) }
            HStack {
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                Button("Close") { dismiss() }.keyboardShortcut(.cancelAction)
                if let p {
                    let a = Approval.release(id: p.id, tool: p.tool, version: p.version)
                    Button("Decline") { act(a, approve: false) }
                        .help("Don't propose \(p.version) again (Roll Out still can)")
                    Button("Approve") { act(a, approve: true) }
                        .keyboardShortcut(.defaultAction)
                        .help("Roll \(p.tool) \(p.version) out")
                }
            }
            .disabled(busy)
        }
        .padding(20)
        .frame(width: 520)
    }

    /// The notes as Markdown where they parse, else as they are.
    static func notes(_ s: String) -> AttributedString {
        if s.isEmpty { return AttributedString("No release notes.") }
        let o = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        return (try? AttributedString(markdown: s, options: o)) ?? AttributedString(s)
    }

    private func act(_ a: Approval, approve: Bool) {
        ui.releaseBusy = true
        ui.releaseError = nil
        Task {
            let why = approve ? await conn.approve(a) : await conn.reject(a)
            ui.releaseBusy = false
            if let why {
                ui.releaseError = why
            } else {
                ui.say(approve ? "Rolling out \(tool)" : "Declined \(tool)'s release")
                dismiss()
            }
        }
    }
}

/// The toolkit's toolbar: Check Now, Roll Out on the selected tool, and
/// Cancel, Retry or Dismiss on the selected rollout.
struct ToolkitTools: ToolbarContent {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some ToolbarContent {
        let off = conn.status != .connected || !conn.snapshot.hasToolkit
        let tool = ToolkitList.tools(conn.snapshot).first { $0.id == ui.toolSelection }
        let rollouts = ToolkitList.rollouts(conn.snapshot)
        let r = rollouts.first { $0.id == ui.rolloutSelection } ?? rollouts.first
        ToolbarItemGroup {
            Button {
                Self.check(conn: conn, ui: ui)
            } label: {
                Label("Check Now", systemImage: "arrow.triangle.2.circlepath")
            }
            .help("Check the tools' releases and versions now")
            .disabled(off || conn.snapshot.toolkit.checking)
            Button {
                Self.checkDrift(conn: conn, ui: ui)
            } label: {
                Label("Check Drift", systemImage: "doc.badge.gearshape")
            }
            .help("Dry-run every repo's scaffold at the pins now")
            .disabled(off || conn.snapshot.toolkit.driftChecking)
            Button {
                if let tool { ui.confirmRollOut = tool }
            } label: {
                Label("Roll Out", systemImage: "shippingbox.and.arrow.backward")
            }
            .help(tool.map { $0.canRollOut ? "Roll \($0.name) \($0.latest) out" : "\($0.name) has nothing newer to roll out" }
                ?? "Select a tool to roll out")
            .disabled(off || tool?.canRollOut != true)
            Button {
                if let r { Self.control(conn: conn, ui: ui, .cancel, r) }
            } label: {
                Label("Cancel Rollout", systemImage: "stop.circle")
            }
            .help(r.map { "Stop \($0.id); its PRs stay open" } ?? "Select a rollout")
            .disabled(off || r?.canCancel != true)
            Button {
                if let r { Self.control(conn: conn, ui: ui, .retry, r) }
            } label: {
                Label("Retry", systemImage: "arrow.counterclockwise")
            }
            .help(r.map { "Resume \($0.id), re-scaffolding the repos that failed or were refused" } ?? "Select a rollout")
            .disabled(off || r?.canRetry != true)
            Button {
                if let r { Self.control(conn: conn, ui: ui, .dismiss, r) }
            } label: {
                Label("Dismiss", systemImage: "xmark.circle")
            }
            .help(r.map { "Forget \($0.id)" } ?? "Select a rollout")
            .disabled(off || r?.canDismiss != true)
        }
    }

    @MainActor
    static func check(conn: HubConnection, ui: DashboardState) {
        Task {
            if let why = await conn.toolkit(.check) {
                ui.say("Toolkit: \(why)", bad: true)
            } else {
                ui.say("Checking the tools' releases")
            }
        }
    }

    @MainActor
    static func checkDrift(conn: HubConnection, ui: DashboardState) {
        Task {
            if let why = await conn.toolkit(.drift) {
                ui.say("Toolkit: \(why)", bad: true)
            } else {
                ui.say("Checking every repo's scaffold against the pins")
            }
        }
    }

    @MainActor
    static func fix(conn: HubConnection, ui: DashboardState, _ d: DriftRow) {
        Task {
            if let why = await conn.fixDrift(target: d.target) {
                ui.say("Toolkit: \(why)", bad: true)
            } else {
                ui.say("Fixing \(d.target): a scaffold PR at the pins")
            }
        }
    }

    @MainActor
    static func rollOut(conn: HubConnection, ui: DashboardState, _ t: ToolRow) {
        Task {
            if let why = await conn.rollOut(tool: t.name, version: t.latest) {
                ui.say("Toolkit: \(why)", bad: true)
            } else {
                ui.say("Rolling \(t.name) \(t.latest) out")
            }
        }
    }

    @MainActor
    static func control(conn: HubConnection, ui: DashboardState, _ a: Ghhud_V1_ToolkitAction, _ r: RolloutRow) {
        let said = switch a {
        case .cancel: "Cancelled \(r.id)"
        case .retry: "Retrying \(r.id)"
        default: "Dismissed \(r.id)"
        }
        Task {
            if let why = await conn.toolkit(a, id: r.id) {
                ui.say("Toolkit: \(why)", bad: true)
            } else {
                ui.say(said)
            }
        }
    }
}
