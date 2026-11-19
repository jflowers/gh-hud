import Foundation

// The Fleet pane, worked out from a hub snapshot: the TUI's F view of the
// GitOps rollout. Pure functions, so the checks cover them.

/// One machine in the rollout.
public struct FleetRow: Identifiable, Equatable, Sendable {
    public var id: String { name }
    public let name: String
    public let state: String
    public let tone: Tone
    /// "yes", "no" or "unknown": whether the last probe reached it.
    public let reach: String
    /// Agent slots in use, "1/2", or "—".
    public let slots: String
    /// Slots in use, for sorting; -1 when unknown.
    public let slotsUsed: Int
    /// The applied commit, "→ target" while it isn't current.
    public let applied: String
    /// How long it has waited for agents to drain.
    public let waited: TimeInterval?
    public let error: String
    /// The machine's Dewey reindex: a running pass or rollout reindex
    /// ("reindexing 3/12"), else the latest one's summary.
    public let dewey: String
    public let deweyTone: Tone
    /// The machine as the hub sent it, for the detail.
    public let machine: Ghhud_V1_FleetMachine

    public init(_ m: Ghhud_V1_FleetMachine, target: String, usage: [Ghhud_V1_MachineUsage]) {
        name = m.name
        state = m.state
        tone = FleetList.tone(m.state)
        switch m.state {
        case "unreachable", "skipped": reach = "no"
        case "pending" where !m.reachable: reach = "unknown"
        default: reach = m.reachable ? "yes" : "unknown"
        }
        if let u = usage.first(where: { $0.name == m.name }) {
            slots = u.ok ? "\(u.used)/\(u.max)" : "—"
            slotsUsed = u.ok ? Int(u.used) : -1
        } else {
            slots = "—"
            slotsUsed = -1
        }
        let have = m.appliedShort.isEmpty ? "—" : m.appliedShort
        applied = m.state == "current" || target.isEmpty || m.appliedShort == target ? have : "\(have) → \(target)"
        let secs = TimeInterval(m.waited.seconds) + TimeInterval(m.waited.nanos) / 1e9
        waited = m.state == "draining" && secs > 0 ? secs : nil
        error = m.lastError.split(whereSeparator: \.isNewline).joined(separator: " ")
        (dewey, deweyTone) = DeweyFleet.column(m)
        machine = m
    }
}

public enum FleetList {
    public static func rows(_ s: Ghhud_V1_Snapshot) -> [FleetRow] {
        guard s.hasFleet else { return [] }
        return s.fleet.machines.map { FleetRow($0, target: s.fleet.targetShort, usage: s.usage) }
    }

    /// How a machine's state reads.
    public static func tone(_ state: String) -> Tone {
        switch state {
        case "current": return .good
        case "pending": return .muted
        case "draining", "applying", "reindexing", "verifying": return .pending
        case "failed": return .bad
        default: return .warn // unreachable, skipped
        }
    }

    /// How the target's CI reads.
    public static func ciTone(_ ci: String) -> Tone {
        switch ci {
        case "green": return .good
        case "pending": return .pending
        case "failed": return .bad
        case "no checks": return .warn
        default: return .muted
        }
    }

    /// "2/3": machines on the target, of all. The sidebar's badge.
    public static func progress(_ s: Ghhud_V1_Snapshot) -> String {
        let ms = s.fleet.machines
        return "\(ms.filter { $0.state == "current" }.count)/\(ms.count)"
    }

    /// The rollout in a few words: "failed on oak", "paused", "rolling
    /// out", "blocked", or "up to date".
    public static func state(_ f: Ghhud_V1_FleetSnapshot) -> (String, Tone) {
        if !f.failed.isEmpty { return ("failed on \(f.failed)", .bad) }
        if f.paused { return ("paused", .warn) }
        if f.blocked { return ("blocked", .warn) }
        if f.active { return ("rolling out", .pending) }
        return ("up to date", .good)
    }
}
