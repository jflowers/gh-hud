import Foundation

// What needs you, worked out from a hub snapshot. Pure functions, so the
// checks can cover them without a hub or a UI.

/// Dashboard section bits (Item.categories), as the hub defines them.
public enum Category: UInt32, Sendable {
    case reviewRequested = 1
    case mine = 2
    case issues = 4
    case ci = 8
    case notifications = 16
    case readyForMaintainer = 32
    case readyForAgentReview = 64
}

public extension Ghhud_V1_Item {
    func isIn(_ c: Category) -> Bool { categories & c.rawValue != 0 }

    /// "owner/repo#N", or the URL when the item has no number.
    var ref: String { num > 0 ? "\(repo)#\(num)" : url }
}

/// Something you can approve from the menu.
public enum Approval: Equatable, Sendable {
    /// A supervisor proposal (Case.proposal), approved or rejected by id.
    case proposal(id: String)
    /// An epic's plan, approved by the epic's ref and the plan seen.
    case epic(ref: String, planID: String)
    /// A gate on a piece of an approved epic, cleared by the epic's ref and
    /// the piece's.
    case gate(ref: String, piece: String)
    /// A tool release proposed for rollout (a toolkit rollout in stage
    /// "proposed"): approving rolls it out, declining keeps it from being
    /// proposed again.
    case release(id: String, tool: String, version: String)

    /// Whether it can be turned down: a proposal or a release (an epic
    /// plan is approved or left, a gate cleared or left).
    public var rejectable: Bool {
        switch self {
        case .proposal, .release: return true
        default: return false
        }
    }

    /// The button that says no, when there is one.
    public var rejectVerb: String {
        if case .release = self { return "Decline" }
        return "Reject"
    }

    /// The button that says yes: "Clear" for a gate, "Approve" otherwise.
    public var verb: String {
        if case .gate = self { return "Clear" }
        return "Approve"
    }
}

/// One row in the menu.
public struct AttentionRow: Identifiable, Equatable, Sendable {
    public let id: String
    public let title: String
    public let detail: String
    public let url: URL?
    public let updated: Date?
    /// Set when the row waits for your approval.
    public let approval: Approval?

    public init(id: String, title: String, detail: String, url: URL?, updated: Date?,
                approval: Approval? = nil) {
        self.id = id
        self.title = title
        self.detail = detail
        self.url = url
        self.updated = updated
        self.approval = approval
    }
}

/// A titled group of rows.
public struct AttentionSection: Identifiable, Equatable, Sendable {
    public let id: String
    public let title: String
    public let rows: [AttentionRow]
}

public struct Attention: Equatable, Sendable {
    /// Sections that have rows, in menu order.
    public let sections: [AttentionSection]
    /// How many things need you: the badge on the menu bar icon.
    public let count: Int

    public static let empty = Attention(sections: [], count: 0)

    /// What needs you in a snapshot: agents waiting on you, supervisor
    /// proposals and epic plans to approve, epic gates to clear, tool releases to roll out,
    /// repos in tool rollouts held, refused or failing, reviews requested of you, and
    /// your PRs ready for a maintainer. Newest first.
    public init(_ s: Ghhud_V1_Snapshot) {
        let items = s.result.items
        func rows(_ c: Category) -> [AttentionRow] {
            items.filter { $0.isIn(c) }
                .sorted { $0.updatedAt.date > $1.updatedAt.date }
                .map { it in
                    AttentionRow(
                        id: it.url, title: it.title,
                        detail: it.author.isEmpty ? it.ref : "\(it.ref) · \(it.author)",
                        url: URL(string: it.url),
                        updated: it.hasUpdatedAt ? it.updatedAt.date : nil)
                }
        }
        let blocked = s.sessions.filter(\.blocked)
            .sorted { $0.blockedSince.date < $1.blockedSince.date } // longest waiting first
            .map { se in
                AttentionRow(
                    id: "session:" + se.id,
                    title: se.title.isEmpty ? se.id : se.title,
                    detail: [se.task, se.ref, se.machine.isEmpty ? "local" : se.machine]
                        .filter { !$0.isEmpty }.joined(separator: " · "),
                    url: Links.forRef(se.ref),
                    updated: se.hasBlockedSince ? se.blockedSince.date : nil)
            }
        let all = [
            AttentionSection(id: "blocked", title: "Agents waiting on you", rows: blocked),
            AttentionSection(id: "proposals", title: "Proposals awaiting approval", rows: Self.proposals(s)),
            AttentionSection(id: "epics", title: "Epic plans awaiting approval", rows: Self.epics(s)),
            AttentionSection(id: "gates", title: "Epic gates only you can clear", rows: Self.gates(s)),
            AttentionSection(id: "releases", title: "Tool releases awaiting approval", rows: Self.releases(s)),
            AttentionSection(id: "rollouts", title: "Tool rollouts waiting on you", rows: Self.rollouts(s)),
            AttentionSection(id: "review", title: "Review requested", rows: rows(.reviewRequested)),
            AttentionSection(id: "ready", title: "Ready for a maintainer", rows: rows(.readyForMaintainer)),
        ]
        self.init(sections: all.filter { !$0.rows.isEmpty },
                  count: all.reduce(0) { $0 + $1.rows.count })
    }

    /// Cases whose supervisor proposal waits for you, oldest first.
    static func proposals(_ s: Ghhud_V1_Snapshot) -> [AttentionRow] {
        s.cases.filter { c in
            c.hasProposal && !c.proposal.id.isEmpty
                && (c.proposal.status.isEmpty || c.proposal.status == "pending")
        }
        .sorted { $0.proposal.created.date < $1.proposal.created.date }
        .map { c in
            let p = c.proposal
            let d = p.decision
            let summary = d.summary.split(whereSeparator: \.isNewline).first.map(String.init) ?? ""
            let kind = d.kind.replacingOccurrences(of: "_", with: " ")
            let title = summary.isEmpty ? "\(kind.isEmpty ? "proposal" : kind) for \(c.repo)" : summary
            let pr = d.fixPr > 0 ? d.fixPr : (c.facts.prs.first ?? 0)
            return AttentionRow(
                id: "proposal:" + p.id,
                title: title,
                detail: [kind, c.repo, c.check].filter { !$0.isEmpty }.joined(separator: " · "),
                url: pr > 0 ? Links.forRef("\(c.repo)#\(pr)") : nil,
                updated: p.hasCreated ? p.created.date : nil,
                approval: .proposal(id: p.id))
        }
    }

    /// Epics whose current plan waits for approval. A stale plan (the epic
    /// changed since it was read) isn't offered: the hub reads it again.
    static func epics(_ s: Ghhud_V1_Snapshot) -> [AttentionRow] {
        s.epics.filter { $0.state == "planned" && $0.hasPlan && !$0.plan.stale && !$0.plan.id.isEmpty }
            .map { e in
                let open = e.plan.nodes.filter { $0.state != "done" && $0.state != "dropped" }.count
                return AttentionRow(
                    id: "epic:" + e.ref,
                    title: e.title.isEmpty ? e.ref : e.title,
                    detail: "\(e.ref) · \(e.plan.nodes.count) pieces, \(open) open",
                    url: URL(string: e.url) ?? Links.forRef(e.ref),
                    updated: e.plan.hasCreated ? e.plan.created.date : nil,
                    approval: .epic(ref: e.ref, planID: e.plan.id))
            }
    }

    /// Tool releases the toolkit proposes, oldest first.
    static func releases(_ s: Ghhud_V1_Snapshot) -> [AttentionRow] {
        s.toolkit.rollouts.filter { $0.stage == "proposed" }
            .sorted { $0.started.date < $1.started.date }
            .map { r in
                AttentionRow(
                    id: "release:" + r.id,
                    title: "\(r.tool) \(r.version)",
                    detail: r.from.isEmpty ? "new release" : "new release · the fleet runs \(r.from)",
                    url: r.notesURL.isEmpty ? nil : URL(string: r.notesURL),
                    updated: r.hasStarted ? r.started.date : nil,
                    approval: .release(id: r.id, tool: r.tool, version: r.version))
            }
    }

    /// Tool rollouts that wait on you: a pin PR to merge (nothing goes on
    /// until it does), then repos held for a look, refused, failed, or
    /// failing CI, as the hub says, newest rollout first. A cancelled
    /// rollout's don't: you stopped it. The row opens the PR when there is
    /// one.
    static func rollouts(_ s: Ghhud_V1_Snapshot) -> [AttentionRow] {
        s.toolkit.rollouts.filter { $0.stage != "cancelled" && $0.stage != "proposed" }
            .flatMap { r in
                var rows: [AttentionRow] = []
                if pinWaits(r) {
                    rows.append(AttentionRow(
                        id: "rollout:\(r.id) pin",
                        title: "Merge the \(r.tool) \(r.version) pin",
                        detail: ["pin PR open", prRef(r.pinPr)].filter { !$0.isEmpty }.joined(separator: " · "),
                        url: URL(string: r.pinPr),
                        updated: r.hasUpdated ? r.updated.date : nil))
                }
                return rows + r.targets.filter { !$0.needsYou.isEmpty }.map { t in
                    let place = t.url.isEmpty ? (t.branch.isEmpty ? t.detail : t.branch) : ""
                    return AttentionRow(
                        id: "rollout:\(r.id) \(t.target)",
                        title: "\(r.tool) \(r.version) on \(t.target)",
                        detail: [t.needsYou, place].filter { !$0.isEmpty }.joined(separator: " · "),
                        url: t.url.isEmpty ? nil : URL(string: t.url),
                        updated: r.hasUpdated ? r.updated.date : nil)
                }
            }
    }

    /// "owner/repo#N" for a GitHub PR URL, "" for anything else.
    static func prRef(_ url: String) -> String {
        let p = (URLComponents(string: url)?.path ?? "").split(separator: "/")
        guard p.count == 4, p[2] == "pull", Int(p[3]) != nil else { return "" }
        return "\(p[0])/\(p[1])#\(p[3])"
    }

    /// Whether a rollout waits for you to merge its pin PR.
    static func pinWaits(_ r: Ghhud_V1_ToolkitRolloutState) -> Bool {
        r.stage == "pin" && !r.pinPr.isEmpty && r.pinState != "MERGED" && r.pinState != "CLOSED"
    }

    /// Pieces of approved epics held only by a gate: everything they wait
    /// on is done, and the condition is yours to say holds. A plan waiting
    /// for approval is offered whole instead.
    static func gates(_ s: Ghhud_V1_Snapshot) -> [AttentionRow] {
        s.epics.filter { $0.state == "approved" && $0.hasPlan }
            .flatMap { e in
                e.plan.nodes.filter { $0.state == "gated" && !$0.gate.isEmpty && !$0.cleared }
                    .map { n in
                        AttentionRow(
                            id: "gate:" + e.ref + " " + n.ref,
                            title: n.gate,
                            detail: [n.ref, n.title, "epic " + e.ref].filter { !$0.isEmpty }
                                .joined(separator: " · "),
                            url: Links.forRef(n.ref),
                            updated: nil,
                            approval: .gate(ref: e.ref, piece: n.ref))
                    }
            }
    }

    public init(sections: [AttentionSection], count: Int) {
        self.sections = sections
        self.count = count
    }
}

/// The hub's state in a few words, for the top of the menu.
public enum HubSummary {
    public static func line(_ s: Ghhud_V1_Snapshot, now: Date = Date()) -> String {
        if !s.fetchError.isEmpty {
            return "GitHub: \(s.fetchError)"
        }
        var parts: [String] = []
        if s.hasResult, !s.result.user.isEmpty {
            parts.append(s.result.user)
        }
        if s.fetching {
            parts.append("refreshing…")
        } else if s.hasFetchedAt {
            parts.append("updated \(ago(s.fetchedAt.date, now: now))")
        } else {
            parts.append("first refresh…")
        }
        if !s.follower.isEmpty {
            parts.append("read-only: gh-hud pid \(s.follower) owns dispatch")
        }
        if s.hasFleet {
            if !s.fleet.failed.isEmpty {
                parts.append("fleet failed on \(s.fleet.failed)")
            } else if s.fleet.paused {
                parts.append("fleet paused")
            }
        }
        return parts.joined(separator: " · ")
    }

    /// "now", "3m ago", "2h ago", "4d ago".
    public static func ago(_ d: Date, now: Date) -> String {
        let secs = max(0, Int(now.timeIntervalSince(d)))
        switch secs {
        case ..<60: return "now"
        case ..<3600: return "\(secs / 60)m ago"
        case ..<86400: return "\(secs / 3600)h ago"
        default: return "\(secs / 86400)d ago"
        }
    }
}
