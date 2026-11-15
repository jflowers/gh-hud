import Foundation

// What the tables sort on when a column's header is clicked. Each column
// sorts by a key that orders the way the column reads: status by how much
// it needs you, age newest first, unknowns last. With no column picked the
// tables keep the hub's order, and rows that tie keep it too.

extension Tone {
    /// Most in need of attention first: failed, then warnings, then in
    /// progress, then fine, then nothing to say.
    public var rank: Int {
        switch self {
        case .bad: return 0
        case .warn: return 1
        case .pending: return 2
        case .good: return 3
        case .muted: return 4
        }
    }
}

enum SortKey {
    /// An age column: ascending is newest first, no date last.
    static func age(_ d: Date?) -> Double {
        d.map { -$0.timeIntervalSinceReferenceDate } ?? .infinity
    }

    /// A duration column: unknown last.
    static func duration(_ t: TimeInterval?) -> Double {
        t ?? .infinity
    }
}

extension ItemRow {
    /// Ready, your move, needs you, then the rest.
    public var markerRank: Int {
        switch marker {
        case .needsYou: return 0
        case .yourMove: return 1
        case .ready: return 2
        case nil: return 3
        }
    }

    /// The status cluster: a conflict or the worst of CI and review.
    public var statusRank: Int {
        if conflict { return Tone.bad.rank }
        return [ci, review].compactMap { $0?.rank }.min() ?? Tone.muted.rank + 1
    }

    /// Issues without a number sort last.
    public var numKey: Int { num > 0 ? num : .max }
    /// People's threads first, bots' breaking ties.
    public var threadsKey: Int { threads * 1000 + botThreads }
    public var ageKey: Double { SortKey.age(updated) }
}

extension AgentRow {
    public var statusRank: Int { status.isEmpty ? Tone.muted.rank + 1 : tone.rank }
    public var tookKey: Double { SortKey.duration(took) }
    public var ageKey: Double { SortKey.age(updated) }
}

extension WorkspaceRow {
    public var workspace: String { health.workspace }
    public var branch: String { health.branch }
    public var stateRank: Int { tone.rank }
    /// Furthest behind upstream first when descending; unknown last.
    public var syncKey: Int {
        health.behind < 0 || health.ahead < 0 ? .max : Int(health.behind) * 1000 + Int(health.ahead)
    }
}

extension FleetRow {
    public var stateRank: Int { tone.rank }
    public var waitedKey: Double { SortKey.duration(waited) }
}
