import Foundation

// The Agents and Workspaces panes, worked out from a hub snapshot: the
// TUI's tabs 6 and 9. Pure functions, so the checks cover them.

/// One agent session, with the dispatch run behind it when there is one.
public struct AgentRow: Identifiable, Equatable, Sendable {
    /// The run's ID for a dispatch, else the session's. Session IDs repeat:
    /// dispatching the same task on the same item and machine again reuses
    /// the name, so two rows (the new run and the old) share it.
    public let id: String
    public let session: Ghhud_V1_Session
    public let run: Ghhud_V1_Run?
    /// The task, or "" for a session gh-hud didn't start.
    public let task: String
    /// The ref and the item's title for a dispatch; the session title
    /// otherwise.
    public let name: String
    public let machine: String // "local" for the hub's machine
    public let workspace: String
    /// "running", "blocked", "failed", "left work"…; "" when idle.
    public let status: String
    public let tone: Tone
    /// How long the run took (or has taken so far).
    public let took: TimeInterval?
    public let updated: Date?
    /// Live or a dispatch: shown without "Show stale".
    public let recent: Bool

    public init(_ s: Ghhud_V1_Session, run: Ghhud_V1_Run?, items: [Ghhud_V1_Item], now: Date = Date()) {
        id = s.runID.isEmpty ? s.id : s.runID
        session = s
        self.run = run
        task = s.task
        if s.ref.isEmpty {
            name = s.title.isEmpty ? s.id : s.title
        } else if let it = items.first(where: { $0.ref == s.ref }) {
            name = "\(s.ref)  \(it.title)"
        } else {
            name = s.ref
        }
        machine = s.machine.isEmpty ? "local" : s.machine
        workspace = s.workspace
        updated = s.hasUpdatedAt ? s.updatedAt.date : nil
        if let r = run {
            status = r.status == "left-work" ? "left work" : r.status
            tone = RunTag.tone(r.status)
            let end = r.hasFinishedAt ? r.finishedAt.date : now
            took = r.hasStartedAt ? max(0, end.timeIntervalSince(r.startedAt.date)) : nil
        } else if s.blocked {
            (status, tone, took) = ("blocked", .warn, nil)
        } else if s.running {
            (status, tone, took) = (s.state.isEmpty ? "running" : s.state, .pending, nil)
        } else if s.failed {
            (status, tone, took) = ("failed", .bad, nil)
        } else if s.unknown {
            (status, tone, took) = ("unknown", .warn, nil)
        } else {
            (status, tone, took) = ("", .muted, nil)
        }
        let cutoff = now.addingTimeInterval(-AgentList.recentWindow)
        recent = !s.task.isEmpty || s.running || s.blocked || s.failed || (updated.map { $0 > cutoff } ?? false)
    }
}

extension AgentRow {
    /// Executing now: a running or blocked run, or a live session gh-hud
    /// didn't start, whatever state it reports. "Live Only" shows these.
    public var live: Bool {
        if let r = run { return r.status == "running" || r.status == "blocked" }
        return session.running || session.blocked
    }

    func withID(_ id: String) -> AgentRow {
        AgentRow(id: id, session: session, run: run, task: task, name: name, machine: machine, workspace: workspace,
                 status: status, tone: tone, took: took, updated: updated, recent: recent)
    }

    private init(id: String, session: Ghhud_V1_Session, run: Ghhud_V1_Run?, task: String, name: String,
                 machine: String, workspace: String, status: String, tone: Tone, took: TimeInterval?,
                 updated: Date?, recent: Bool) {
        self.id = id
        self.session = session
        self.run = run
        self.task = task
        self.name = name
        self.machine = machine
        self.workspace = workspace
        self.status = status
        self.tone = tone
        self.took = took
        self.updated = updated
        self.recent = recent
    }
}

public enum AgentList {
    /// Sessions idle longer than this, and not dispatches, are stale.
    public static let recentWindow: TimeInterval = 86400

    /// The sessions in the hub's order; stale ones only when asked, and
    /// with liveOnly only the ones executing now.
    public static func rows(_ s: Ghhud_V1_Snapshot, showStale: Bool, liveOnly: Bool = false,
                            now: Date = Date()) -> [AgentRow] {
        let runs = Dictionary(s.runs.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        var seen: [String: Int] = [:]
        return s.sessions
            .map { AgentRow($0, run: $0.runID.isEmpty ? nil : runs[$0.runID], items: s.result.items, now: now) }
            .filter { (showStale || $0.recent) && (!liveOnly || $0.live) }
            .map { r in
                // A table needs every row's ID unique; a repeat (sessions
                // without a run) gets a suffix.
                let n = seen[r.id, default: 0]
                seen[r.id] = n + 1
                return n == 0 ? r : r.withID("\(r.id)#\(n)")
            }
    }

    /// Live sessions: the sidebar's count.
    public static func running(_ s: Ghhud_V1_Snapshot) -> Int {
        s.sessions.filter(\.running).count
    }

    /// "45s", "12m", "1h05".
    public static func duration(_ t: TimeInterval) -> String {
        let secs = Int(t)
        switch secs {
        case ..<60: return "\(secs)s"
        case ..<3600: return "\(secs / 60)m"
        default: return String(format: "%dh%02d", secs / 3600, (secs % 3600) / 60)
        }
    }
}

extension RunTag {
    /// How a run status reads.
    public static func tone(_ status: String) -> Tone {
        switch status {
        case "running": return .pending
        case "blocked", "left-work": return .warn
        case "ok": return .good
        case "failed": return .bad
        default: return .muted
        }
    }
}

/// One checkout a dispatch could use.
public struct WorkspaceRow: Identifiable, Equatable, Sendable {
    public let id: String // machine and directory
    public let health: Ghhud_V1_CheckoutHealth
    /// "chai:widgets-2", or the workspace alone on the hub's machine.
    public let place: String
    public let machine: String // "local" for the hub's machine
    public let repo: String // the name, without the owner
    /// The default branch against upstream: "↓3 ↑1", "=", or "—" unknown.
    public let sync: String
    /// "ready", "running #12", "2 uncommitted"…
    public let state: String
    public let tone: Tone
    /// Not blocking but worth knowing: "2 local-only · 1 stash".
    public let notes: String
    /// What the state means for the next run, when there's more to say.
    public let explanation: String?
    /// The Dewey index column: "ok 2d 100%", "old format", "—"…
    public let index: String
    public let indexTone: Tone

    public init(_ h: Ghhud_V1_CheckoutHealth, now: Date = Date()) {
        id = h.machine + "\u{0}" + h.dir
        health = h
        place = h.machine.isEmpty ? h.workspace : "\(h.machine):\(h.workspace)"
        machine = h.machine.isEmpty ? "local" : h.machine
        repo = h.repo.split(separator: "/", maxSplits: 1).last.map(String.init) ?? h.repo
        if h.behind < 0 || h.ahead < 0 {
            sync = "—"
        } else {
            let parts = [h.behind > 0 ? "↓\(h.behind)" : nil, h.ahead > 0 ? "↑\(h.ahead)" : nil].compactMap { $0 }
            sync = parts.isEmpty ? "=" : parts.joined(separator: " ")
        }
        let problem = Self.problem(h)
        switch true {
        case !h.error.isEmpty:
            (state, tone) = (h.error, .bad)
        case !h.busy.isEmpty:
            let ref = h.busy.lastIndex(of: "#").map { String(h.busy[$0...]) } ?? h.busy
            (state, tone) = ("running \(ref)", .pending)
        case h.reindexing:
            (state, tone) = (problem, .pending)
        case h.noUpstream:
            (state, tone) = (problem, .warn)
        case problem == "upkeep only":
            (state, tone) = (problem, .muted)
        case !problem.isEmpty:
            (state, tone) = (problem, .bad)
        default:
            (state, tone) = ("ready", .good)
        }
        var n: [String] = []
        let local = Self.localOnlyCommits(h)
        if local > 0 { n.append("\(local) local-only") }
        if h.stashes > 0 { n.append("\(h.stashes) stash") }
        if h.learnings > 0 { n.append("\(h.learnings) learnings") }
        if h.hasLastReindex, DeweyIndex.failed(h.lastReindex), !h.reindexing { n.append("reindex failed") }
        notes = n.joined(separator: " · ")
        let ready = problem.isEmpty
        if ready, !h.defaultBranch.isEmpty, h.branch != h.defaultBranch {
            explanation = "A run here switches to \(h.defaultBranch) and fast-forwards it."
        } else if ready {
            explanation = "A run here fast-forwards \(h.defaultBranch) to upstream."
        } else if h.upkeepOnly, problem == "upkeep only" {
            explanation = "ghws clones new instances from it: upkeep keeps it current, and no run starts here."
        } else if !h.dirty.isEmpty, h.busy.isEmpty {
            explanation = "A run here is refused until these are committed or discarded."
        } else if h.ahead > 0, h.behind > 0 {
            explanation = "\(h.defaultBranch) can't fast-forward to upstream/\(h.defaultBranch)."
        } else {
            explanation = nil
        }
        (index, indexTone) = DeweyIndex.column(h, now: now)
    }

    /// Why the next run can't start here; "" when it can.
    static func problem(_ h: Ghhud_V1_CheckoutHealth) -> String {
        if !h.error.isEmpty { return h.error }
        if !h.busy.isEmpty { return "running " + h.busy }
        if h.reindexing { return "reindexing" }
        if h.noUpstream { return "no upstream remote" }
        if !h.dirty.isEmpty { return "\(h.dirty.count) uncommitted" }
        if h.ahead > 0, h.behind > 0 { return h.defaultBranch + " diverged" }
        if h.upkeepOnly { return "upkeep only" }
        return ""
    }

    /// Commits on no remote, across branches ("branch\tcount" lines).
    static func localOnlyCommits(_ h: Ghhud_V1_CheckoutHealth) -> Int {
        h.localOnly.reduce(0) { n, l in
            n + (l.split(separator: "\t", maxSplits: 1).last.flatMap { Int($0) } ?? 0)
        }
    }

    public var ready: Bool { state == "ready" }
}

public enum WorkspaceList {
    /// The checkouts; a worker's unmanaged-removal row isn't one.
    public static func rows(_ s: Ghhud_V1_Snapshot, now: Date = Date()) -> [WorkspaceRow] {
        s.workspaces.filter { !$0.unmanaged }.map { WorkspaceRow($0, now: now) }
    }

    /// Checkouts the next run could use: the sidebar's count.
    public static func ready(_ s: Ghhud_V1_Snapshot) -> Int {
        rows(s).filter(\.ready).count
    }

    /// Whether the workspaces are worth reading again on opening the pane
    /// (the TUI's 30 seconds).
    public static func stale(_ s: Ghhud_V1_Snapshot, now: Date = Date()) -> Bool {
        !s.workspacesLoading && (!s.hasWorkspacesAt || now.timeIntervalSince(s.workspacesAt.date) > 30)
    }
}
