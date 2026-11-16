import Foundation

// The Toolkit pane, worked out from a hub snapshot: the TUI's T view of the
// tools the fleet pins and their rollouts. Pure functions, so the checks
// cover them (and the Android app's tests render the same text).

/// One tool the fleet pins.
public struct ToolRow: Identifiable, Equatable, Sendable {
    public var id: String { name }
    public let name: String
    /// On the dotfiles branch ("—" when it has none).
    public let pin: String
    /// The newest release ("—" when unknown).
    public let latest: String
    /// "◆" when a release waits for your go-ahead, "↑" when one is out
    /// that nobody proposed (declined, say), "" when the pin is the latest.
    public let mark: String
    public let tone: Tone
    /// The rollout under way for it, if any.
    public let rolling: String?
    /// The proposed rollout waiting on you, if any.
    public let proposal: String?
    /// What can be rolled out: the latest release, unless the pin has it
    /// or the tool is already rolling out.
    public let canRollOut: Bool
    /// "4/6 repos on 2.4.0": targets whose scaffold matches the pin.
    public let repos: String
    public let error: String

    public init(_ t: Ghhud_V1_ToolkitTool, rollouts: [Ghhud_V1_ToolkitRolloutState]) {
        name = t.name
        pin = t.pin.isEmpty ? "—" : t.pin
        latest = t.latest.isEmpty ? "—" : t.latest
        let mine = rollouts.filter { $0.tool == t.name }
        proposal = mine.first { $0.stage == "proposed" }?.id
        rolling = mine.first { ToolkitList.active($0.stage) }?.id
        let newer = !t.latest.isEmpty && !t.pin.isEmpty && t.latest != t.pin
        mark = proposal != nil ? "◆" : (newer ? "↑" : "")
        error = t.error
        tone = !t.error.isEmpty ? .bad : (proposal != nil || newer ? .warn : .good)
        canRollOut = rolling == nil && newer
        if t.repos.isEmpty || t.pin.isEmpty {
            repos = ""
        } else {
            let on = t.repos.filter { $0.version == t.pin }.count
            repos = "\(on)/\(t.repos.count) repos on \(t.pin)"
        }
    }
}

/// One tool release on its way out (or done, failed, or cancelled).
public struct RolloutRow: Identifiable, Equatable, Sendable {
    public let id: String
    public let tool: String
    public let version: String
    public let stage: String
    public let tone: Tone
    public let landed: Int
    public let open: Int
    public let held: Int
    public let refused: Int
    public let failed: Int
    /// "3/8 landed · 2 open · 1 held": the counts that aren't zero.
    public let counts: String
    /// The hub's one-line progress.
    public let summary: String
    public let error: String
    public let pinPR: URL?
    public let canCancel: Bool
    public let canRetry: Bool
    public let canDismiss: Bool
    public let targets: [TargetRow]

    public init(_ r: Ghhud_V1_ToolkitRolloutState) {
        id = r.id
        tool = r.tool
        version = r.version
        stage = r.stage
        tone = ToolkitList.stageTone(r.stage)
        targets = r.targets.map(TargetRow.init)
        landed = targets.filter(\.landed).count
        open = targets.filter { $0.state == "open" }.count
        held = targets.filter { $0.state == "held" }.count
        refused = targets.filter { $0.state == "refused" }.count
        failed = targets.filter { $0.state == "failed" }.count
        var parts: [String] = []
        if !targets.isEmpty { parts.append("\(landed)/\(targets.count) landed") }
        for (n, word) in [(open, "open"), (held, "held"), (refused, "refused"), (failed, "failed")] where n > 0 {
            parts.append("\(n) \(word)")
        }
        counts = parts.joined(separator: " · ")
        summary = r.summary
        error = r.error
        pinPR = r.pinPr.isEmpty ? nil : URL(string: r.pinPr)
        let active = ToolkitList.active(r.stage)
        canCancel = active
        canRetry = r.stage == "failed" || r.stage == "cancelled"
        canDismiss = !active && r.stage != "proposed"
    }
}

/// One repo in a rollout.
public struct TargetRow: Identifiable, Equatable, Sendable {
    public var id: String { target }
    public let target: String
    /// held, refused, failed, CI failing, merged, closed, pushed, up to
    /// date, open, or the hub's status.
    public let state: String
    public let tone: Tone
    public let landed: Bool
    /// The PR when there is one.
    public let url: URL?
    /// Where it is without a PR (a held branch) or why it stopped.
    public let note: String

    public init(_ t: Ghhud_V1_ToolkitTarget) {
        target = t.target
        url = t.url.isEmpty ? nil : URL(string: t.url)
        landed = t.prState == "MERGED" || t.status == "pushed" || t.status == "up to date"
        switch true {
        case !t.needsYou.isEmpty: state = t.needsYou
        case t.prState == "MERGED": state = "merged"
        case t.prState == "CLOSED": state = "closed"
        case t.status == "pushed", t.status == "up to date": state = t.status
        case url != nil: state = "open"
        default: state = t.status
        }
        switch state {
        case "merged", "pushed", "up to date": tone = .good
        case "held": tone = .warn
        case "refused", "failed", "CI failing": tone = .bad
        case "open": tone = .pending
        default: tone = .muted
        }
        note = url == nil && !t.branch.isEmpty ? t.branch : t.detail
    }
}

public enum ToolkitList {
    public static func tools(_ s: Ghhud_V1_Snapshot) -> [ToolRow] {
        guard s.hasToolkit else { return [] }
        return s.toolkit.tools.map { ToolRow($0, rollouts: s.toolkit.rollouts) }
    }

    /// Rollouts newest first, proposals left out: they're the tools' ◆.
    public static func rollouts(_ s: Ghhud_V1_Snapshot) -> [RolloutRow] {
        s.toolkit.rollouts.filter { $0.stage != "proposed" }.map(RolloutRow.init)
    }

    /// The proposal waiting for tool, for the release notes and Approve.
    public static func proposal(_ s: Ghhud_V1_Snapshot, tool: String) -> Ghhud_V1_ToolkitRolloutState? {
        s.toolkit.rollouts.first { $0.tool == tool && $0.stage == "proposed" }
    }

    /// The sidebar's badge: releases to approve and repos waiting on you.
    public static func waiting(_ s: Ghhud_V1_Snapshot) -> Int {
        Attention.releases(s).count + Attention.rollouts(s).count
    }

    /// The toolkit in a few words.
    public static func state(_ s: Ghhud_V1_Snapshot) -> String {
        let t = s.toolkit
        if !t.error.isEmpty { return t.error }
        var parts: [String] = []
        if t.checking {
            parts.append("checking…")
        } else if t.hasCheckedAt {
            parts.append("checked \(HubSummary.ago(t.checkedAt.date, now: s.fetchedAt.date))")
        }
        let rolling = t.rollouts.filter { active($0.stage) }.count
        if rolling > 0 { parts.append("\(rolling) rolling out") }
        return parts.joined(separator: " · ")
    }

    public static func active(_ stage: String) -> Bool {
        stage == "pin" || stage == "scaffold" || stage == "track"
    }

    public static func stageTone(_ stage: String) -> Tone {
        switch stage {
        case "done": return .good
        case "failed": return .bad
        case "proposed": return .warn
        case "cancelled": return .muted
        default: return .pending
        }
    }
}
