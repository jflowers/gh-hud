import Foundation

// The Plan pane, worked out from a hub snapshot: the TUI's tab p. What
// agents work on next in start order (your queue, then the auto steps
// due), what's running, waiting, stopped and merging; above them the open
// cases and the enrolled epics. Pure functions, so the checks cover them.

/// One row of the plan.
public struct PlanItem: Identifiable, Equatable, Sendable {
    public let id: String // task and ref, unique in a plan
    public let group: String // "your queue", "auto, next", "running"…
    /// Start order (1 first) for rows the next pass tries to start.
    public let position: Int?
    public let task: String
    public let ref: String
    public let title: String
    public let url: URL?
    public let status: String
    public let tone: Tone
    /// Waiting, merging or missing: drawn dimmed.
    public let quiet: Bool
    /// Your queue's place (1 first); nil for an auto row.
    public let queued: Int?
    /// Queued, but not in the latest refresh.
    public let missing: Bool
    /// The epic a piece comes from.
    public let epic: String
    /// A piece held by a gate only you can clear.
    public let gated: Bool
    public let item: Ghhud_V1_Item

    init(_ r: Ghhud_V1_PlanRow, position: Int?) {
        let it = r.item
        item = it
        task = r.task
        ref = it.num > 0 ? "\(it.repo)#\(it.num)" : it.url
        id = r.task + "|" + ref
        group = Self.group(r)
        self.position = position
        title = it.title
        url = URL(string: it.url).flatMap { $0.scheme == nil ? nil : $0 } ?? Links.forRef(ref)
        (status, tone) = Self.status(r)
        quiet = r.stage.kind == "wait" || r.stage.kind == "merge" || r.missing
        queued = r.queued > 0 ? Int(r.queued) : nil
        missing = r.missing
        epic = r.epic
        gated = !r.epic.isEmpty && r.stage.reason.hasPrefix("gate: ")
    }

    /// Whether the next pass tries to start it.
    static func starts(_ r: Ghhud_V1_PlanRow) -> Bool {
        r.queued > 0 ? !r.missing && r.stage.kind != "running" : r.stage.kind == "next"
    }

    static func group(_ r: Ghhud_V1_PlanRow) -> String {
        if r.queued > 0 { return "Your queue" }
        switch r.stage.kind {
        case "next": return "Auto, next"
        case "running": return "Running"
        case "wait": return "Waiting"
        case "you": return "Needs you"
        case "merge": return "Merging"
        default: return "Other"
        }
    }

    static func status(_ r: Ghhud_V1_PlanRow) -> (String, Tone) {
        let kind = r.stage.kind
        if r.queued > 0 {
            if r.missing || kind == "running" { return (r.note, .muted) }
            if r.note == "next free slot" || r.note == "starting" { return (r.note, .good) }
            return (r.note, .bad)
        }
        if !r.epic.isEmpty, kind == "next" { return ("next free slot · epic \(r.epic)", .good) }
        if !r.epic.isEmpty, kind == "running" { return ("running · epic \(r.epic)", .pending) }
        switch kind {
        case "next": return ("next free slot", .good)
        case "running": return ("running", .pending)
        case "wait": return (r.stage.reason, .muted)
        case "you": return (r.stage.reason, .warn)
        case "merge": return (r.stage.reason.isEmpty ? "auto-merge on" : r.stage.reason, .good)
        default: return ("", .muted)
        }
    }
}

/// A group of plan rows under a heading.
public struct PlanGroup: Identifiable, Equatable, Sendable {
    public var id: String { title }
    public let title: String
    public let rows: [PlanItem]
}

/// A check failing on several of your PRs, or a PR's review threads
/// waiting on you, and what the supervisor does about it.
public struct CaseRow: Identifiable, Equatable, Sendable {
    /// A drafted reply to a review thread: what Approve posts, as you.
    public struct Reply: Equatable, Sendable {
        public let place: String // "index/cache.go:42"
        public let author: String
        public let quote: String // the comment it answers, on one line
        public let body: String

        public init(place: String, author: String, quote: String, body: String) {
            self.place = place
            self.author = author
            self.quote = quote
            self.body = body
        }
    }

    public let id: String
    public let title: String // "acme/r lint", "acme/r#12 review threads"
    public let scope: String // "2 PRs, main too"; "" for review threads
    public let status: String
    public let tone: Tone
    /// The proposal's summary, on one line.
    public let summary: String
    /// Set when a proposal waits for you.
    public let proposal: String?
    /// The drafted replies a review_reply proposal posts.
    public let replies: [Reply]
    public var isReview: Bool { id.hasPrefix("review_reply|") }

    public init(_ c: Ghhud_V1_Case) {
        id = c.id
        let review = c.kind == "review_reply"
        title = review ? "\(c.repo)#\(c.pr) review threads" : "\(c.repo) \(c.check)"
        scope = review ? "" : "\(c.facts.prs.count) PR\(c.facts.prs.count == 1 ? "" : "s")" + (c.facts.onBase ? ", main too" : "")
        let waiting = c.hasProposal && !c.proposal.id.isEmpty
            && (c.proposal.status.isEmpty || c.proposal.status == "pending")
        if waiting {
            (status, tone) = ((review ? "supervisor drafted " : "supervisor proposes ") + Self.reason(c.proposal.decision), .warn)
        } else if c.hasDecision {
            (status, tone) = (Self.reason(c.decision) + (c.decision.by.isEmpty ? "" : " (\(c.decision.by))"), .muted)
        } else if c.supervising {
            (status, tone) = (review ? "supervisor drafting replies…" : "supervisor looking…", .pending)
        } else {
            (status, tone) = ("needs you", .warn)
        }
        summary = waiting ? c.proposal.decision.summary.split(whereSeparator: \.isWhitespace).joined(separator: " ") : ""
        proposal = waiting ? c.proposal.id : nil
        replies = waiting ? c.proposal.decision.replies.map { r in
            Reply(place: r.line > 0 ? "\(r.path):\(r.line)" : r.path, author: r.author,
                  quote: r.quote.split(whereSeparator: \.isWhitespace).joined(separator: " "), body: r.body)
        } : []
    }

    static func reason(_ d: Ghhud_V1_Decision) -> String {
        if d.kind == "answer" { return d.replies.count == 1 ? "1 reply" : "\(d.replies.count) replies" }
        if d.kind == "link_fix" { return "fix in #\(d.fixPr)" }
        return "escalated: " + d.summary.split(whereSeparator: \.isWhitespace).joined(separator: " ")
    }
}

/// An enrolled epic: where its plan stands, and its pieces still to do.
public struct EpicRow: Identifiable, Equatable, Sendable {
    public struct Piece: Identifiable, Equatable, Sendable {
        public var id: String { ref }
        public let ref: String // owner/repo#N
        public let short: String // "#N", or shorter for the epic's owner
        public let title: String
        public let state: String
        public let note: String
        /// Ready, in progress or running: drawn bright.
        public let active: Bool
        /// Held by a gate only you can clear (on an approved epic).
        public let clearable: Bool
    }

    public var id: String { ref }
    public let ref: String
    public let title: String
    public let url: URL?
    public let status: String
    public let tone: Tone
    /// The plan to approve, when one waits for you.
    public let planToApprove: String?
    /// Set when the epic's run budget is spent: approving the plan again
    /// grants this many more runs.
    public let budgetToGrant: Int?
    /// What that plan changed since you last approved one, a line each:
    /// set only when it waits for you again.
    public let changes: [String]
    /// The plan whose pieces are all finished, when the epic can be
    /// finished (gh-hud#22), and the comment finishing it posts as you.
    public let planToFinish: String?
    public let summary: String
    /// Pieces still to do, running and ready first.
    public let open: [Piece]
    public let done: Int
    public let total: Int

    public init(_ e: Ghhud_V1_Epic) {
        ref = e.ref
        title = e.title
        url = URL(string: e.url) ?? Links.forRef(e.ref)
        let nodes = e.hasPlan ? e.plan.nodes : []
        total = nodes.count
        done = nodes.filter { $0.state == "done" }.count
        func count(_ s: String) -> Int { nodes.filter { $0.state == s }.count }
        var counts = "\(done)/\(total) done, "
        if count("running") > 0 { counts += "\(count("running")) running, " }
        counts += "\(count("ready")) ready, \(count("in_progress")) in progress"
        switch e.state {
        case "reading":
            (status, tone) = ("reading the plan…", .pending)
        case "error":
            (status, tone) = ("can't read it: \(e.error)", .bad)
        case "closed":
            (status, tone) = (e.closedNote + (e.hasPlan ? " · " + counts : ""), .muted)
        default:
            if !e.hasPlan {
                (status, tone) = ("no plan yet", .muted)
            } else if e.plan.stale {
                (status, tone) = ("the epic changed: reread it · " + counts, .warn)
            } else if e.state == "planned" {
                (status, tone) = ("plan waits for you · " + counts, .warn)
            } else if e.hasComplete {
                (status, tone) = ("every piece finished · " + counts, .warn)
            } else if !e.held.isEmpty {
                (status, tone) = ("held by the \(e.held) label · " + counts, .warn)
            } else if Self.budgetSpent(e) {
                (status, tone) = ("run budget spent (\(e.runs)/\(e.runBudget) runs) · " + counts, .warn)
            } else {
                (status, tone) = ("approved · " + counts + (e.runBudget > 0 ? " · \(e.runs)/\(e.runBudget) runs" : ""), .good)
            }
        }
        let spent = Self.budgetSpent(e) && e.hasPlan && !e.plan.stale && !e.plan.id.isEmpty
        budgetToGrant = spent ? Int(e.runBudget) : nil
        planToApprove = (e.state == "planned" || spent) && e.hasPlan && !e.plan.stale && !e.plan.id.isEmpty ? e.plan.id : nil
        changes = planToApprove == nil ? [] : e.changes
        planToFinish = e.state == "approved" && e.hasComplete && e.hasPlan && !e.plan.stale && !e.summary.isEmpty ? e.plan.id : nil
        summary = planToFinish == nil ? "" : e.summary
        let repo = e.ref.split(separator: "#", maxSplits: 1).first.map(String.init) ?? e.ref
        let rank = ["running": 0, "ready": 1, "in_progress": 2, "gated": 3, "blocked": 4, "waiting": 5, "unknown": 6]
        let approved = e.state == "approved"
        open = nodes.enumerated()
            .filter { $0.element.state != "done" && $0.element.state != "dropped" }
            .sorted { (rank[$0.element.state] ?? 7, $0.offset) < (rank[$1.element.state] ?? 7, $1.offset) }
            .map { _, n in
                Piece(ref: n.ref, short: Self.short(repo, n.ref), title: n.title, state: n.state,
                      note: [n.note, n.area.isEmpty ? "" : "[" + n.area.joined(separator: ", ") + "]"].filter { !$0.isEmpty }.joined(separator: "  "),
                      active: ["ready", "in_progress", "running"].contains(n.state),
                      clearable: approved && n.state == "gated" && !n.gate.isEmpty && !n.cleared)
            }
    }

    /// A piece's ref, shortened within the epic's repo and owner.
    static func short(_ epicRepo: String, _ ref: String) -> String {
        if ref.hasPrefix(epicRepo + "#") { return String(ref.dropFirst(epicRepo.count)) }
        let owner = epicRepo.split(separator: "/").first.map(String.init) ?? epicRepo
        if ref.hasPrefix(owner + "/") { return String(ref.dropFirst(owner.count + 1)) }
        return ref
    }

    /// The TUI's piece marks.
    /// The epic's pieces used its run budget: nothing starts until you
    /// approve the plan again.
    static func budgetSpent(_ e: Ghhud_V1_Epic) -> Bool {
        e.state == "approved" && e.runBudget > 0 && e.runs >= e.runBudget
    }

    public static func mark(_ state: String) -> String {
        switch state {
        case "done": return "✓"
        case "dropped": return "✗"
        case "in_progress": return "▸"
        case "running": return "▶"
        case "ready": return "●"
        case "waiting": return "◌"
        case "gated": return "⏸"
        case "blocked": return "!"
        default: return "?"
        }
    }
}

public enum PlanList {
    /// The plan rows under their headings, in start order; the rows the
    /// next pass tries to start are numbered.
    public static func groups(_ s: Ghhud_V1_Snapshot) -> [PlanGroup] {
        var groups: [PlanGroup] = []
        var pos = 0
        var rows: [PlanItem] = []
        var title = ""
        for r in s.plan {
            let g = PlanItem.group(r)
            if g != title, !rows.isEmpty {
                groups.append(PlanGroup(title: title, rows: rows))
                rows = []
            }
            title = g
            var at: Int?
            if r.queued > 0 || r.stage.kind == "next" {
                pos += 1
                at = pos
            }
            rows.append(PlanItem(r, position: at))
        }
        if !rows.isEmpty { groups.append(PlanGroup(title: title, rows: rows)) }
        return groups
    }

    /// Rows the next pass tries to start: the sidebar's count.
    public static func starting(_ s: Ghhud_V1_Snapshot) -> Int {
        s.plan.filter(PlanItem.starts).count
    }

    /// "4 start next · slots chai 1/2, local 0/1 · read-only".
    public static func summary(_ s: Ghhud_V1_Snapshot) -> String {
        var parts = ["\(starting(s)) start next"]
        if !s.usage.isEmpty {
            parts.append("slots " + s.usage.map { $0.ok ? "\($0.name) \($0.used)/\($0.max)" : "\($0.name) —" }
                .joined(separator: ", "))
        }
        if !s.follower.isEmpty { parts.append("read-only") }
        return parts.joined(separator: " · ")
    }

    public static func cases(_ s: Ghhud_V1_Snapshot) -> [CaseRow] { s.cases.map(CaseRow.init) }
    public static func epics(_ s: Ghhud_V1_Snapshot) -> [EpicRow] { s.epics.map(EpicRow.init) }
}
