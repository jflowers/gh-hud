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

/// One row in the menu.
public struct AttentionRow: Identifiable, Equatable, Sendable {
    public let id: String
    public let title: String
    public let detail: String
    public let url: URL?
    public let updated: Date?
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

    /// What needs you in a snapshot: reviews requested of you, your PRs
    /// ready for a maintainer, and agents waiting on you. Newest first.
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
            AttentionSection(id: "review", title: "Review requested", rows: rows(.reviewRequested)),
            AttentionSection(id: "ready", title: "Ready for a maintainer", rows: rows(.readyForMaintainer)),
        ]
        self.init(sections: all.filter { !$0.rows.isEmpty },
                  count: all.reduce(0) { $0 + $1.rows.count })
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
