import Foundation

// The dashboard's item list, worked out from a hub snapshot: the filters
// (the TUI's tabs 0-5, 7 and 8), search, and what each row shows. Pure
// functions, so the checks cover them without a hub or a UI.

/// How a status reads, for the UI to color.
public enum Tone: Equatable, Sendable {
    case good, bad, warn, pending, muted
}

/// A filter over the items: one of the dashboard's sections, or all.
public enum ItemFilter: String, CaseIterable, Identifiable, Hashable, Sendable {
    case all, review, mine, issues, ci, inbox, ready, queue

    public var id: String { rawValue }

    public var title: String {
        switch self {
        case .all: return "All items"
        case .review: return "Review requested"
        case .mine: return "Mine"
        case .issues: return "Issues"
        case .ci: return "CI"
        case .inbox: return "Inbox"
        case .ready: return "Ready for a maintainer"
        case .queue: return "Agent review queue"
        }
    }

    /// A short name, for the sidebar.
    public var shortTitle: String {
        switch self {
        case .all: return "All"
        case .review: return "Review"
        case .ready: return "Ready"
        case .queue: return "Agent queue"
        default: return title
        }
    }

    /// The section it shows; nil for all.
    public var category: Category? {
        switch self {
        case .all: return nil
        case .review: return .reviewRequested
        case .mine: return .mine
        case .issues: return .issues
        case .ci: return .ci
        case .inbox: return .notifications
        case .ready: return .readyForMaintainer
        case .queue: return .readyForAgentReview
        }
    }

    public func matches(_ it: Ghhud_V1_Item) -> Bool {
        guard let c = category else { return true }
        return it.isIn(c)
    }
}

/// One row of the item list.
public struct ItemRow: Identifiable, Equatable, Sendable {
    /// Why a row stands out, the TUI's ◆.
    public enum Marker: Equatable, Sendable {
        /// Your PR, waiting only on a maintainer (green).
        case ready
        /// The next move is yours (yellow).
        case yourMove
    }

    public let id: String // the URL
    public let url: URL?
    public let kind: String // "mine", "review", "issue", "ci", "inbox"
    public let marker: Marker?
    public let isPR: Bool
    public let ci: Tone?
    public let review: Tone?
    public let conflict: Bool
    public let num: Int
    public let repo: String
    public let title: String
    public let draft: Bool
    public let author: String
    /// Unresolved threads opened by people, and by bots.
    public let threads: Int
    public let botThreads: Int
    /// Who acted last: "you" when it was you.
    public let lastActor: String
    public let updated: Date?
    public let labels: [String]

    /// "owner/repo#N", or the URL when the item has no number.
    public var ref: String { num > 0 ? "\(repo)#\(num)" : id }

    public init(_ it: Ghhud_V1_Item, me: String) {
        id = it.url
        url = URL(string: it.url)
        kind = Self.kind(it)
        if it.isIn(.readyForMaintainer) {
            marker = .ready
        } else if it.waitingOn == "me" {
            marker = .yourMove
        } else {
            marker = nil
        }
        isPR = it.kind == .pr || it.kind == .run
        ci = Self.tone(it.ci)
        review = Self.reviewTone(it.review)
        conflict = it.merge == "⚠️"
        num = Int(it.num)
        repo = it.repo
        title = it.title
        draft = it.draft
        author = it.author
        threads = Int(it.threads)
        botThreads = Int(it.botThreads)
        lastActor = !me.isEmpty && it.lastActor == me ? "you" : it.lastActor
        updated = it.hasUpdatedAt ? it.updatedAt.date : nil
        labels = it.labels
    }

    /// The kind badge, as the TUI picks it.
    static func kind(_ it: Ghhud_V1_Item) -> String {
        if it.kind == .run { return "ci" }
        if it.isIn(.mine) { return "mine" }
        if it.isIn(.reviewRequested) { return "review" }
        if it.isIn(.issues) { return "issue" }
        return "inbox"
    }

    static func tone(_ s: Ghhud_V1_CIState) -> Tone? {
        switch s {
        case .ok: return .good
        case .fail: return .bad
        case .pending: return .pending
        case .none: return .muted
        default: return nil
        }
    }

    /// The hub sends the review state as the TUI's emoji.
    static func reviewTone(_ r: String) -> Tone? {
        switch r {
        case "✅": return .good
        case "❌", "‼️": return .bad
        case "❗": return .warn
        case "🟡": return .pending
        default: return nil
        }
    }
}

public enum ItemList {
    /// The rows a filter and a search show, in the hub's order (most
    /// pressing first). The search matches repo, title and ref, ignoring
    /// case, like the TUI's /.
    public static func rows(_ s: Ghhud_V1_Snapshot, filter: ItemFilter, query: String = "") -> [ItemRow] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        return s.result.items
            .filter { filter.matches($0) }
            .filter { it in
                q.isEmpty || "\(it.repo) \(it.title) \(it.ref)".lowercased().contains(q)
            }
            .map { ItemRow($0, me: s.result.user) }
    }

    /// How many items each filter shows (before searching).
    public static func counts(_ s: Ghhud_V1_Snapshot) -> [ItemFilter: Int] {
        var out: [ItemFilter: Int] = [:]
        for f in ItemFilter.allCases {
            out[f] = s.result.items.filter { f.matches($0) }.count
        }
        return out
    }

    /// "now", "5m", "3h", "2d", "1w", "4mo": the TUI's age column.
    public static func age(_ d: Date, now: Date = Date()) -> String {
        let mins = max(0, Int(now.timeIntervalSince(d) / 60))
        switch mins {
        case ..<1: return "now"
        case ..<60: return "\(mins)m"
        case ..<1440: return "\(mins / 60)h"
        case ..<10080: return "\(mins / 1440)d"
        case ..<43200: return "\(mins / 10080)w"
        default: return "\(mins / 43200)mo"
        }
    }
}
