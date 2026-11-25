import AppKit
import GHHudKit
import SwiftUI

// The Plan pane: the TUI's tab p. Open cases and enrolled epics, then what
// agents work on next in start order and what's running, waiting, stopped
// and merging.

struct PlanPane: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some View {
        let s = conn.snapshot
        let cases = PlanList.cases(s)
        let epics = PlanList.epics(s)
        let groups = PlanList.groups(s)
        let rows = groups.flatMap(\.rows)
        let cmds = PlanCommands(conn: conn, ui: ui)
        List(selection: $ui.planSelection) {
            Text(PlanList.summary(s)).font(.callout).foregroundStyle(.secondary)
            if !cases.isEmpty {
                Section("Cases") {
                    ForEach(cases) { c in CaseView(row: c, cmds: cmds) }
                }
            }
            if !epics.isEmpty {
                Section("Epics") {
                    ForEach(epics) { e in EpicView(row: e, cmds: cmds) }
                }
            }
            ForEach(groups) { g in
                Section(g.title) {
                    ForEach(g.rows) { r in
                        PlanRowView(row: r).tag(r.id)
                    }
                }
            }
            if rows.isEmpty {
                Text("Nothing queued, due, running or waiting.").foregroundStyle(.secondary)
            }
        }
        .contextMenu(forSelectionType: PlanItem.ID.self) { ids in
            if let r = rows.first(where: { ids.contains($0.id) }) {
                cmds.menu(r)
            }
        } primaryAction: { ids in
            if let u = rows.first(where: { ids.contains($0.id) })?.url { NSWorkspace.shared.open(u) }
        }
        .onDeleteCommand {
            if let r = selected(rows), r.queued != nil { cmds.unqueue(r) }
        }
        .confirmationDialog(
            "Stop following \(ui.dropping?.ref ?? "")?",
            isPresented: Binding(get: { ui.dropping != nil }, set: { if !$0 { ui.dropping = nil } }),
            presenting: ui.dropping
        ) { e in
            Button("Drop Epic", role: .destructive) { cmds.drop(e) }
        } message: { e in
            Text("gh-hud stops reading \(e.ref) and starts none of its pieces. Pieces already running carry on; you can enroll it again later.")
        }
        .confirmationDialog(
            "Finish \(ui.finishing?.ref ?? "")?",
            isPresented: Binding(get: { ui.finishing != nil }, set: { if !$0 { ui.finishing = nil } }),
            presenting: ui.finishing
        ) { e in
            Button("Post Summary and Close") { cmds.finish(e) }
        } message: { e in
            Text("gh-hud posts the summary shown under \(e.ref) as you, then closes the epic as completed.")
        }
        .sheet(isPresented: $ui.enrolling) {
            EnrollSheet(conn: conn, ui: ui)
        }
    }

    private func selected(_ rows: [PlanItem]) -> PlanItem? {
        rows.first { $0.id == ui.planSelection }
    }
}

/// The plan's toolbar: move and remove queued tasks, enroll an epic.
struct PlanTools: ToolbarContent {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some ToolbarContent {
        let cmds = PlanCommands(conn: conn, ui: ui)
        let rows = PlanList.groups(conn.snapshot).flatMap(\.rows)
        let picked = rows.first { $0.id == ui.planSelection }
        let queued = picked?.queued != nil && cmds.off == nil
        ToolbarItemGroup {
            Button {
                if let picked { cmds.move(picked, by: -1) }
            } label: {
                Label("Move Earlier", systemImage: "arrow.up")
            }
            .help("Start this queued task earlier")
            .keyboardShortcut(.upArrow, modifiers: [.command, .option])
            .disabled(!queued)
            Button {
                if let picked { cmds.move(picked, by: 1) }
            } label: {
                Label("Move Later", systemImage: "arrow.down")
            }
            .help("Start this queued task later")
            .keyboardShortcut(.downArrow, modifiers: [.command, .option])
            .disabled(!queued)
            Button {
                if let picked { cmds.unqueue(picked) }
            } label: {
                Label("Remove from Queue", systemImage: "minus.circle")
            }
            .help("Take this task off your queue (⌫)")
            .disabled(!queued)
        }
        ToolbarItem {
            Button {
                ui.enrolling = true
            } label: {
                Label("Enroll Epic", systemImage: "square.stack.3d.up.badge.automatic")
            }
            .help(cmds.off ?? "Have gh-hud read an epic issue and run its pieces once you approve the plan")
            .disabled(cmds.off != nil)
        }
    }
}

/// The plan's commands, shared by the toolbar, the menus and the buttons.
@MainActor
struct PlanCommands {
    let conn: HubConnection
    let ui: DashboardState

    var off: String? {
        conn.status == .connected ? ItemList.commandsOff(conn.snapshot) : "Not connected to the hub"
    }

    private func row(_ r: PlanItem) -> ItemRow { ItemRow(r.item, me: conn.snapshot.result.user) }

    @ViewBuilder
    func menu(_ r: PlanItem) -> some View {
        let off = off
        if let u = r.url {
            Button("Open on GitHub") { NSWorkspace.shared.open(u) }
            Button("Copy Link") { copy(u.absoluteString) }
            Divider()
        }
        Button("Start Agent…") { ui.sheet = AgentRequest(row: row(r), queue: false) }
            .disabled(off != nil || r.missing)
        if r.queued != nil {
            Button("Move Earlier") { move(r, by: -1) }.disabled(off != nil)
            Button("Move Later") { move(r, by: 1) }.disabled(off != nil)
            Button("Remove from Queue") { unqueue(r) }.disabled(off != nil)
        } else if !r.task.isEmpty {
            Button("Queue \(r.task)") { enqueue(r) }.disabled(off != nil)
        } else {
            Button("Add to Queue…") { ui.sheet = AgentRequest(row: row(r), queue: true) }.disabled(off != nil)
        }
        if r.gated {
            Divider()
            Button("Clear Gate") { clearGate(epic: r.epic, piece: r.ref) }.disabled(off != nil)
        }
    }

    func enqueue(_ r: PlanItem) {
        Task {
            switch await conn.enqueue(task: r.task, url: r.item.url) {
            case .success(let pos): ui.say("Queued \(r.task) for \(r.ref), number \(pos) in your queue")
            case .failure(let why): ui.say("Can't queue \(r.task) for \(r.ref): \(why)", bad: true)
            }
        }
    }

    func move(_ r: PlanItem, by delta: Int64) {
        Task {
            if case .failure(let why) = await conn.moveQueued(task: r.task, ref: r.ref, by: delta) {
                ui.say("Can't move \(r.task) \(r.ref): \(why)", bad: true)
            }
        }
    }

    func unqueue(_ r: PlanItem) {
        Task {
            if let why = await conn.unqueue(task: r.task, ref: r.ref) {
                ui.say("Can't unqueue \(r.task) \(r.ref): \(why)", bad: true)
            } else {
                ui.say("Took \(r.task) \(r.ref) off your queue")
            }
        }
    }

    func approve(_ c: CaseRow) {
        guard let id = c.proposal else { return }
        Task {
            if let why = await conn.approve(.proposal(id: id)) {
                ui.say("Can't approve the proposal for \(c.title): \(why)", bad: true)
            } else {
                ui.say("Approved: \(c.summary)")
            }
        }
    }

    func reject(_ c: CaseRow) {
        guard let id = c.proposal else { return }
        Task {
            if let why = await conn.reject(.proposal(id: id)) {
                ui.say("Can't reject the proposal for \(c.title): \(why)", bad: true)
            } else {
                ui.say("Rejected; \(c.title) stays with you")
            }
        }
    }

    func approve(_ e: EpicRow) {
        guard let plan = e.planToApprove else { return }
        Task {
            if let why = await conn.approve(.epic(ref: e.ref, planID: plan)) {
                ui.say("Can't approve the plan for \(e.ref): \(why)", bad: true)
            } else {
                ui.say("Approved the plan for \(e.ref)")
            }
        }
    }

    func finish(_ e: EpicRow) {
        guard let plan = e.planToFinish else { return }
        Task {
            if let why = await conn.finishEpic(e.ref, planID: plan) {
                ui.say("Can't finish \(e.ref): \(why)", bad: true)
            } else {
                ui.say("Posted the summary and closed \(e.ref)")
            }
        }
    }

    func reread(_ e: EpicRow) {
        Task {
            if let why = await conn.rereadEpic(e.ref) {
                ui.say("Can't reread \(e.ref): \(why)", bad: true)
            } else {
                ui.say("Reading \(e.ref) again")
            }
        }
    }

    func drop(_ e: EpicRow) {
        Task {
            if let why = await conn.dropEpic(e.ref) {
                ui.say("Can't drop \(e.ref): \(why)", bad: true)
            } else {
                ui.say("Dropped \(e.ref)")
            }
        }
    }

    func clearGate(epic: String, piece: String) {
        Task {
            if let why = await conn.approve(.gate(ref: epic, piece: piece)) {
                ui.say("Can't clear the gate on \(piece): \(why)", bad: true)
            } else {
                ui.say("Cleared the gate on \(piece)")
            }
        }
    }
}

struct PlanRowView: View {
    let row: PlanItem

    var body: some View {
        HStack(spacing: 10) {
            Text(row.position.map(String.init) ?? "")
                .monospacedDigit()
                .foregroundStyle(.secondary)
                .frame(width: 22, alignment: .trailing)
            Text(row.task.isEmpty ? "—" : row.task)
                .frame(width: 64, alignment: .leading)
            Text(row.ref)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.head)
                .frame(width: 190, alignment: .leading)
            Text(row.title).lineLimit(1)
            Spacer(minLength: 12)
            Text(row.status)
                .foregroundStyle(row.tone.color)
                .lineLimit(1)
        }
        .opacity(row.quiet ? 0.6 : 1)
        .help(row.missing ? "Queued, but not in the latest refresh" : row.ref)
    }
}

struct CaseView: View {
    let row: CaseRow
    let cmds: PlanCommands

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "flag.fill").foregroundStyle(.orange)
                Text(row.title).fontWeight(.medium)
                Text(row.scope).foregroundStyle(.secondary)
                Spacer()
                Text(row.status).foregroundStyle(row.tone.color).lineLimit(1)
            }
            if row.proposal != nil {
                if !row.summary.isEmpty {
                    Text(row.summary).foregroundStyle(.secondary).lineLimit(2)
                }
                HStack {
                    Button("Approve") { cmds.approve(row) }
                    Button("Reject") { cmds.reject(row) }
                }
                .controlSize(.small)
                .disabled(cmds.off != nil)
            }
        }
        .padding(.vertical, 2)
    }
}

struct EpicView: View {
    let row: EpicRow
    let cmds: PlanCommands
    /// Open pieces listed before "and N more".
    private let shown = 8

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "square.stack.3d.up.fill").foregroundStyle(.purple)
                Text(row.ref).foregroundStyle(.secondary)
                Text(row.title).fontWeight(.medium).lineLimit(1)
                Spacer()
                Text(row.status).foregroundStyle(row.tone.color).lineLimit(1)
                Menu {
                    if let u = row.url {
                        Button("Open on GitHub") { NSWorkspace.shared.open(u) }
                    }
                    Button("Reread") { cmds.reread(row) }
                    Divider()
                    Button("Drop…", role: .destructive) { cmds.ui.dropping = row }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
                .disabled(cmds.off != nil)
            }
            if !row.changes.isEmpty {
                VStack(alignment: .leading, spacing: 1) {
                    Text("Changed since you approved it:").font(.caption).foregroundStyle(.secondary)
                    ForEach(Array(row.changes.enumerated()), id: \.offset) { _, c in
                        Text("± \(c)").font(.caption).foregroundStyle(Tone.warn.color)
                    }
                }
                .padding(.leading, 22)
            }
            if row.planToApprove != nil {
                Button("Approve Plan") { cmds.approve(row) }
                    .controlSize(.small)
                    .disabled(cmds.off != nil)
            }
            if row.planToFinish != nil {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Closes with this comment, posted as you:").font(.caption).foregroundStyle(.secondary)
                    Text(row.summary.trimmingCharacters(in: .whitespacesAndNewlines))
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                    Button("Finish Epic…") { cmds.ui.finishing = row }
                        .controlSize(.small)
                        .disabled(cmds.off != nil)
                }
                .padding(.leading, 22)
            }
            ForEach(row.open.prefix(shown)) { p in
                HStack(spacing: 6) {
                    Text(EpicRow.mark(p.state)).frame(width: 14)
                    Text(p.short).foregroundStyle(.secondary)
                    Text(p.title).lineLimit(1)
                    if !p.note.isEmpty {
                        Text("· \(p.note)").foregroundStyle(.secondary).lineLimit(1)
                    }
                    if p.clearable {
                        Button("Clear Gate") { cmds.clearGate(epic: row.ref, piece: p.ref) }
                            .controlSize(.small)
                            .disabled(cmds.off != nil)
                    }
                }
                .padding(.leading, 22)
                .opacity(p.active ? 1 : 0.65)
            }
            if row.open.count > shown {
                Text("and \(row.open.count - shown) more").font(.caption).foregroundStyle(.secondary).padding(.leading, 22)
            }
        }
        .padding(.vertical, 2)
    }
}

/// Asks for an epic to enroll.
@MainActor
final class EnrollModel: ObservableObject {
    @Published var target = ""
    @Published var busy = false
    @Published var error: String?
}

struct EnrollSheet: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState
    @StateObject private var model = EnrollModel()

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Enroll an epic").font(.headline)
            Text("gh-hud reads the issue as a plan of pieces. Nothing starts until you approve the plan.")
                .foregroundStyle(.secondary)
            TextField("Issue URL or owner/repo#N", text: $model.target)
                .textFieldStyle(.roundedBorder)
                .onSubmit(enroll)
            if let err = model.error {
                Label(err, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
            }
            HStack {
                if model.busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("Cancel") { ui.enrolling = false }
                    .keyboardShortcut(.cancelAction)
                Button("Enroll", action: enroll)
                    .keyboardShortcut(.defaultAction)
                    .disabled(model.target.trimmingCharacters(in: .whitespaces).isEmpty || model.busy)
            }
        }
        .padding(16)
        .frame(width: 440)
    }

    private func enroll() {
        let target = model.target.trimmingCharacters(in: .whitespaces)
        guard !target.isEmpty else { return }
        model.busy = true
        model.error = nil
        Task {
            switch await conn.enrollEpic(target) {
            case .success(let ref):
                ui.enrolling = false
                ui.say("Enrolled \(ref); its plan shows here once it's read")
            case .failure(let why):
                model.error = "\(why)"
            }
            model.busy = false
        }
    }
}
