import AppKit
import GHHudKit
import SwiftUI

// The Agents and Workspaces panes: a table with the selected row's detail
// below it, like the TUI's tabs 6 and 9.

/// Agent sessions and the runs behind them. Double-click watches one.
struct AgentsPane: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some View {
        let rows = TableFilter.rows(AgentList.rows(conn.snapshot, showStale: ui.showStale, liveOnly: ui.liveOnly),
                                    query: ui.agentQuery, kept: ui.agentFilters).sorted(using: ui.agentSort)
        let picked = rows.first { ui.agentSelection == $0.id }
        VSplitView {
            Table(rows, selection: $ui.agentSelection, sortOrder: $ui.agentSort) {
                TableColumn("", value: \.statusRank) { r in StatusGlyph(tone: r.tone, active: !r.status.isEmpty) }
                    .width(12)
                TableColumn("Task", value: \.task) { r in Text(r.task.isEmpty ? "—" : r.task).foregroundStyle(r.task.isEmpty ? .secondary : .primary) }
                    .width(min: 44, ideal: 64, max: 100)
                TableColumn("Agent", value: \.name) { r in Text(r.name).lineLimit(1).foregroundStyle(r.recent ? .primary : .secondary) }
                    .width(min: 160, ideal: 290)
                TableColumn("Where", value: \.machine) { r in Text(r.machine).foregroundStyle(.secondary) }
                    .width(min: 40, ideal: 60, max: 100)
                TableColumn("Workspace", value: \.workspace) { r in Text(r.workspace.isEmpty ? "—" : r.workspace).foregroundStyle(.secondary).lineLimit(1) }
                    .width(min: 60, ideal: 130, max: 260)
                TableColumn("Status", value: \.statusRank) { r in Text(r.status).foregroundStyle(r.tone.color) }
                    .width(min: 50, ideal: 76, max: 120)
                TableColumn("Took", value: \.tookKey) { r in
                    Text(r.took.map(AgentList.duration) ?? "").foregroundStyle(.secondary).monospacedDigit()
                }
                .width(min: 36, ideal: 48, max: 64)
                TableColumn("Age", value: \.ageKey) { r in
                    Text(r.updated.map { ItemList.age($0) } ?? "").foregroundStyle(.secondary).monospacedDigit()
                }
                .width(min: 36, ideal: 44, max: 64)
            }
            .contextMenu(forSelectionType: AgentRow.ID.self) { ids in
                if let r = rows.first(where: { ids.contains($0.id) }) {
                    menu(r)
                    FilterMenu(row: r, filters: $ui.agentFilters, except: ["agent"])
                }
                HubOrderButton(sort: $ui.agentSort)
            } primaryAction: { ids in
                if let r = rows.first(where: { ids.contains($0.id) }) { watch(r) }
            }
            .safeAreaInset(edge: .top, spacing: 0) { FilterBar(filters: $ui.agentFilters) }
            .tableSearch(text: $ui.agentQuery, kept: $ui.agentFilters, columns: AgentRow.filterColumns,
                         prompt: "Search or status:blocked")
            .frame(minHeight: 140)
            .overlay {
                if rows.isEmpty {
                    if ui.liveOnly, ui.agentQuery.isEmpty, ui.agentFilters.isEmpty {
                        ContentUnavailableView("No agents running", systemImage: "bolt.slash",
                                               description: Text("Nothing is running or blocked right now."))
                    } else if !ui.agentQuery.isEmpty || !ui.agentFilters.isEmpty {
                        NoMatches(query: ui.agentQuery)
                    } else {
                        ContentUnavailableView("No agent sessions", systemImage: "person.2.slash",
                                               description: Text(ui.showStale ? "" : "Stale sessions are hidden."))
                    }
                }
            }

            ScrollView {
                Group {
                    if let picked {
                        AgentDetail(row: picked)
                    } else {
                        Text("Select a session to see its run.").foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(14)
            }
            .frame(minHeight: 120, idealHeight: 240)
        }
    }

    private var off: String? {
        conn.status == .connected ? ItemList.commandsOff(conn.snapshot) : "Not connected to the hub"
    }

    @ViewBuilder
    private func menu(_ r: AgentRow) -> some View {
        Button("Watch") { watch(r) }
            .disabled(off != nil || conn.isRemote)
        if let run = r.run {
            Button("Re-check Run") { recheck(r, run) }
                .disabled(off != nil || !run.hasFinishedAt)
            Button("Open Transcript") { transcript(r, run) }
                .disabled(off != nil || run.sessionID.isEmpty || conn.isRemote)
        }
        Divider()
        if let url = Links.forRef(r.session.ref) {
            Button("Open \(r.session.ref) on GitHub") { NSWorkspace.shared.open(url) }
        }
        Button("Copy Session ID") { copy(r.session.id) }
        if !r.session.directory.isEmpty {
            Button("Copy Directory") { copy(r.session.directory) }
        }
    }

    /// Watches a dispatch by its run (session IDs repeat across runs, and
    /// the hub watches the first session with the ID), anything else by
    /// its session.
    private func watch(_ r: AgentRow) {
        Task {
            let why = if let run = r.run {
                await conn.watchRun(id: run.id)
            } else {
                await conn.watch(sessionID: r.session.id)
            }
            if let why {
                ui.say("Can't watch \(r.name): \(why)", bad: true)
            } else {
                ui.say("Watching \(r.name) in a terminal")
            }
        }
    }

    private func recheck(_ r: AgentRow, _ run: Ghhud_V1_Run) {
        ui.say("Re-checking \(run.task) on \(run.ref)…")
        Task {
            switch await conn.reinspect(id: run.id) {
            case .success(let now):
                ui.say("Re-checked \(run.task) on \(run.ref): \(ItemCommands.outcome(now))")
            case .failure(let why):
                ui.say("Can't re-check \(run.task) on \(run.ref): \(why)", bad: true)
            }
        }
    }

    private func transcript(_ r: AgentRow, _ run: Ghhud_V1_Run) {
        Task {
            if let why = await conn.openTranscript(id: run.id) {
                ui.say("Can't open the transcript: \(why)", bad: true)
            } else {
                ui.say("Opened transcript \(run.sessionID) on its machine")
            }
        }
    }
}

func copy(_ text: String) {
    let pb = NSPasteboard.general
    pb.clearContents()
    pb.setString(text, forType: .string)
}

struct StatusGlyph: View {
    let tone: Tone
    let active: Bool

    var body: some View {
        Image(systemName: active ? "circle.fill" : "circle")
            .imageScale(.small)
            .foregroundStyle(active ? tone.color : .secondary)
    }
}

/// Labeled facts, the TUI's detail pane fields.
struct Facts: View {
    let rows: [(String, String)]

    var body: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 4) {
            ForEach(Array(rows.enumerated()), id: \.offset) { _, f in
                GridRow {
                    Text(f.0).foregroundStyle(.secondary).gridColumnAlignment(.trailing)
                    Text(f.1).textSelection(.enabled)
                }
            }
        }
    }
}

/// A session's detail: its run's outcome for a dispatch, the session's
/// identity otherwise.
struct AgentDetail: View {
    let row: AgentRow

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let r = row.run {
                run(r)
            } else {
                let s = row.session
                Text(s.title.isEmpty ? s.id : s.title).font(.headline).foregroundStyle(row.tone.color)
                Facts(rows: [
                    ("status", row.status.isEmpty ? "not running" : row.status),
                    ("dir", s.directory),
                    ("session", s.id),
                    ("updated", row.updated.map(Self.when) ?? ""),
                ] + (s.note.isEmpty ? [] : [("note", s.note)]))
            }
        }
    }

    @ViewBuilder
    private func run(_ r: Ghhud_V1_Run) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(RunTag.words(r.status)).font(.headline).foregroundStyle(RunTag.tone(r.status).color)
            Text("\(r.task) \(r.ref)").foregroundStyle(.secondary)
        }
        Facts(rows: runFacts(r))
        if !r.reason.isEmpty {
            Label(r.reason, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red).textSelection(.enabled)
        }
        if !r.left.isEmpty {
            section("Left behind", note: "commit, push or discard it, then Re-check", lines: r.left, color: .orange)
        }
        if !r.pushed.isEmpty {
            section("Pushed", note: nil, lines: r.pushed, color: .primary)
        }
        if let (lines, tone) = DeweyIndex.run(r.indexHealth) {
            section("Dewey index", note: tone == .good ? nil : "still dispatchable; Workspaces has the detail",
                    lines: lines, color: tone == .good ? .primary : tone.color)
        }
        if !r.summary.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Text("Final message").font(.subheadline.weight(.semibold))
                Text(r.summary).textSelection(.enabled)
            }
        } else if r.hasFinishedAt {
            Text("No final message recorded.").foregroundStyle(.secondary)
        }
    }

    private func runFacts(_ r: Ghhud_V1_Run) -> [(String, String)] {
        var facts = [
            ("where", "\(r.machine.isEmpty ? "local" : r.machine):\(r.workspace)  \(r.dir)"),
            ("started", Self.when(r.startedAt.date)),
        ]
        if let took = row.took { facts.append(("took", AgentList.duration(took))) }
        if r.auto { facts.append(("trigger", "auto-dispatch")) }
        if !r.sessionID.isEmpty { facts.append(("session", r.sessionID)) }
        if !r.branch.isEmpty {
            facts.append(("branch", r.branch + (r.cleaned ? "  (returned to the default branch)" : "")))
        }
        return facts
    }

    private func section(_ title: String, note: String?, lines: [String], color: Color) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(color)
                if let note { Text(note).font(.caption).foregroundStyle(.secondary) }
            }
            ForEach(lines, id: \.self) { l in
                Text(l).font(.callout.monospaced()).foregroundStyle(color).textSelection(.enabled)
            }
        }
    }

    static func when(_ d: Date) -> String {
        d.formatted(.dateTime.month(.abbreviated).day().hour().minute()) + "  (\(ItemList.age(d)) ago)"
    }
}

/// Every checkout a dispatch could use, and whether the next run could
/// start there.
struct WorkspacesPane: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState
    /// An action that pushes or opens a PR, waiting for its confirmation.
    @State private var confirming: (row: WorkspaceRow, action: WorkspaceActions.Action)?

    var body: some View {
        let rows = TableFilter.rows(WorkspaceList.rows(conn.snapshot), query: ui.workspaceQuery,
                                    kept: ui.workspaceFilters).sorted(using: ui.workspaceSort)
        let picked = rows.first { ui.workspaceSelection == $0.id }
        VSplitView {
            Table(rows, selection: $ui.workspaceSelection, sortOrder: $ui.workspaceSort) {
                TableColumn("", value: \.stateRank) { r in StatusGlyph(tone: r.tone, active: true) }
                    .width(12)
                TableColumn("Where", value: \.machine) { r in Text(r.machine).foregroundStyle(.secondary) }
                    .width(min: 40, ideal: 60, max: 100)
                TableColumn("Workspace", value: \.workspace) { r in Text(r.health.workspace).lineLimit(1) }
                    .width(min: 80, ideal: 130, max: 300)
                TableColumn("Repo", value: \.repo) { r in Text(r.repo).foregroundStyle(.secondary).lineLimit(1) }
                    .width(min: 60, ideal: 90, max: 200)
                TableColumn("Branch", value: \.branch) { r in
                    Text(r.health.branch)
                        .foregroundStyle(r.health.branch == r.health.defaultBranch ? .secondary : .primary)
                        .lineLimit(1)
                }
                .width(min: 60, ideal: 110, max: 260)
                TableColumn("Sync", value: \.syncKey) { r in Text(r.sync).foregroundStyle(.secondary).monospacedDigit() }
                    .width(min: 36, ideal: 56, max: 80)
                TableColumn("State", value: \.stateRank) { r in Text(r.state).foregroundStyle(r.tone.color).lineLimit(1) }
                    .width(min: 60, ideal: 140, max: 260)
                TableColumn("Index", value: \.indexRank) { r in
                    Text(r.index)
                        .foregroundStyle(r.indexTone == .good ? .secondary : r.indexTone.color)
                        .monospacedDigit()
                        .lineLimit(1)
                        .help(r.health.index.isEmpty ? "Dewey index not read" : r.health.index == DeweyIndex.none
                            ? "Not a Dewey vault" : "Dewey index: " + r.health.index)
                }
                .width(min: 50, ideal: 110, max: 180)
                TableColumn("Notes", value: \.notes) { r in Text(r.notes).foregroundStyle(.secondary).lineLimit(1) }
                    .width(min: 60, ideal: 160)
            }
            .contextMenu(forSelectionType: WorkspaceRow.ID.self) { ids in
                if let r = rows.first(where: { ids.contains($0.id) }) {
                    let actions = WorkspaceActions.actions(r.health)
                    ForEach(actions, id: \.kind) { a in
                        Button(a.title) {
                            if a.confirm { confirming = (r, a) } else { start(a.kind, on: r) }
                        }
                        .disabled(!a.enabled)
                    }
                    if !actions.isEmpty { Divider() }
                    Button("Copy Directory") { copy(r.health.dir) }
                    if r.machine == "local", !r.health.dir.isEmpty {
                        Button("Show in Finder") {
                            NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: r.health.dir)
                        }
                    }
                    FilterMenu(row: r, filters: $ui.workspaceFilters)
                }
                HubOrderButton(sort: $ui.workspaceSort)
            }
            .confirmationDialog(confirming.map { "\($0.action.title.replacingOccurrences(of: "…", with: "")) in \($0.row.place)?" } ?? "",
                                isPresented: Binding(get: { confirming != nil }, set: { if !$0 { confirming = nil } }),
                                titleVisibility: .visible, presenting: confirming) { c in
                Button(c.action.title.replacingOccurrences(of: "…", with: "")) { start(c.action.kind, on: c.row) }
                Button("Cancel", role: .cancel) {}
            } message: { c in
                Text(WorkspaceActions.confirmation(c.action.kind, c.row.health))
            }
            .safeAreaInset(edge: .top, spacing: 0) { FilterBar(filters: $ui.workspaceFilters) }
            .tableSearch(text: $ui.workspaceQuery, kept: $ui.workspaceFilters, columns: WorkspaceRow.filterColumns,
                         prompt: "Search or where:chai")
            .frame(minHeight: 140)
            .overlay {
                if rows.isEmpty {
                    if !ui.workspaceQuery.isEmpty || !ui.workspaceFilters.isEmpty {
                        NoMatches(query: ui.workspaceQuery)
                    } else if conn.snapshot.workspacesLoading {
                        ProgressView("Reading the checkouts…")
                    } else {
                        ContentUnavailableView("No checkouts", systemImage: "folder.badge.questionmark")
                    }
                }
            }

            ScrollView {
                Group {
                    if let picked {
                        WorkspaceDetail(row: picked)
                    } else {
                        Text("Select a checkout to see why it is or isn't ready.").foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(14)
            }
            .frame(minHeight: 120, idealHeight: 220)
        }
        .task(id: ui.pane) {
            // Opening the pane reads older checkouts again, as the TUI does.
            if WorkspaceList.stale(conn.snapshot) {
                _ = await conn.refresh(.workspaces)
            }
        }
    }
}

extension WorkspacesPane {
    /// Asks the hub to start an action on a checkout and says what started,
    /// or its refusal as it said it.
    fileprivate func start(_ kind: Ghhud_V1_CheckoutActionKind, on r: WorkspaceRow) {
        Task { @MainActor in
            switch await conn.checkoutAction(r.health, kind) {
            case .success(let started):
                ui.say(started.capitalizedFirst + ". The outcome shows in the checkout's detail.")
            case .failure(let why):
                ui.say("\(r.place): \(why)", bad: true)
            }
        }
    }
}

struct WorkspaceDetail: View {
    let row: WorkspaceRow

    var body: some View {
        let h = row.health
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(row.place).font(.headline).foregroundStyle(row.tone.color)
                Text(h.repo).foregroundStyle(.secondary)
            }
            if let why = row.explanation {
                Text("\(row.state.capitalizedFirst). \(why)")
            } else {
                Text(row.state.capitalizedFirst + ".")
            }
            Facts(rows: facts(h))
            if !h.dirty.isEmpty {
                list("Uncommitted (\(h.dirty.count))", h.dirty, color: .orange)
            }
            if !h.localOnly.isEmpty {
                list("Local-only commits: cleaning up loses them",
                     h.localOnly.map(Self.localOnly), color: .orange)
            }
            if let d = DeweyIndex.detail(h) {
                DeweyDetail(detail: d)
            }
        }
    }

    /// "branch\tn" as "branch: n commits".
    static func localOnly(_ line: String) -> String {
        let parts = line.split(separator: "\t", maxSplits: 1).map(String.init)
        guard parts.count == 2, let n = Int(parts[1]) else { return line }
        return "\(parts[0]): \(n) commit\(n == 1 ? "" : "s")"
    }

    private func facts(_ h: Ghhud_V1_CheckoutHealth) -> [(String, String)] {
        var facts = [("dir", h.dir)]
        if !h.branch.isEmpty {
            let other = !h.defaultBranch.isEmpty && h.defaultBranch != h.branch
            facts.append(("branch", h.branch + (other ? "   (default \(h.defaultBranch))" : "")))
        }
        if h.behind >= 0 {
            facts.append(("upstream", "\(h.defaultBranch) is \(h.behind) behind, \(h.ahead) ahead of "
                + "upstream/\(h.defaultBranch) (as of the last fetch)"))
        }
        if h.stashes > 0 { facts.append(("stashes", "\(h.stashes)")) }
        if h.learnings > 0 {
            facts.append(("learnings", "\(h.learnings) untracked; the next clean run opens the daily chore PR"))
        }
        if h.upkeeping {
            facts.append(("upkeep", "running now; no dispatch here until it ends"))
        }
        if h.hasLastUpkeep {
            facts.append(("last upkeep", WorkspaceActions.upkeepLine(h.lastUpkeep)))
        }
        return facts
    }

    private func list(_ title: String, _ lines: [String], color: Color) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(color)
            ForEach(lines, id: \.self) { l in
                Text(l).font(.callout.monospaced()).textSelection(.enabled)
            }
        }
    }
}

/// A checkout's Dewey index: the verdict, each reindex reason on its own
/// line, what built it, what it holds and the last reindex.
struct DeweyDetail: View {
    let detail: DeweyIndex.Detail

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text("Dewey index").font(.subheadline.weight(.semibold))
                Text(detail.verdict)
                    .foregroundStyle(detail.tone == .good || detail.tone == .muted ? .secondary : detail.tone.color)
            }
            ForEach(Array(detail.reasons.enumerated()), id: \.offset) { _, r in
                VStack(alignment: .leading, spacing: 1) {
                    Text(r.reason).font(.callout.monospaced()).foregroundStyle(Tone.pending.color).textSelection(.enabled)
                    Text(r.means).font(.caption).foregroundStyle(.secondary)
                }
            }
            ForEach(detail.problems, id: \.self) { p in
                Text(p).font(.callout.monospaced()).foregroundStyle(detail.tone.color).textSelection(.enabled)
            }
            if !detail.facts.isEmpty {
                Facts(rows: detail.facts.map { ($0.key, $0.value) }).padding(.top, 2)
            }
        }
    }
}

extension String {
    var capitalizedFirst: String { prefix(1).uppercased() + dropFirst() }
}
