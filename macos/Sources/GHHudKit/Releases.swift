import Foundation

// The Releases pane (#218), worked out from a hub snapshot: every repo
// jflowers/ship releases, from its repos.yaml, with the latest release, the
// commits since it, the ship version it's on and its last release run. Pure
// functions, so the checks cover them.

/// One repo ship releases.
public struct ReleaseRow: Identifiable, Equatable, Sendable {
    public var id: String { repo }
    public let repo: String
    /// "go-cli, cask".
    public let kinds: String
    /// The latest release's tag, "—" for none.
    public let latest: String
    /// How long ago it was released ("3d"), "" for none.
    public let released: String
    /// The commits since it: "12", "0", or "—" when unknown.
    public let unreleased: String
    public let unreleasedTone: Tone
    /// The ship version repos.yaml records ("v0.10.0"), "—" for none.
    /// Dependabot moves the pins themselves, so it can lag; the sync check
    /// in ship reports the real pins.
    public let ship: String
    /// The last release run: "success · 2h ago", "running", "failure · 3d
    /// ago", or "" when it never ran.
    public let run: String
    public let runTone: Tone
    public let error: String

    public let releaseURL: URL?
    public let compareURL: URL?
    public let runURL: URL?
    public let repoURL: URL?

    /// Releases it would make sense to cut: commits since the last one.
    public var hasUnreleased: Bool { unreleasedTone == .pending }

    /// What `gh ship release` takes, for the Copy menu.
    public var releaseCommand: String { "gh ship release \(repo) " }

    public init(_ r: Ghhud_V1_ReleaseRepo, now: Date) {
        repo = r.repo
        kinds = r.kinds.joined(separator: ", ")
        latest = r.latest.isEmpty ? "—" : r.latest
        released = r.latest.isEmpty || !r.hasReleasedAt ? "" : ItemList.age(r.releasedAt.date, now: now)
        switch r.ahead {
        case ..<0:
            unreleased = "—"
            unreleasedTone = .muted
        case 0:
            unreleased = "0"
            unreleasedTone = .good
        default:
            unreleased = "\(r.ahead)"
            unreleasedTone = .pending
        }
        ship = r.ship.isEmpty ? "—" : r.ship
        if r.hasLastRun {
            let x = r.lastRun
            let when = x.hasAt ? " · \(ItemList.age(x.at.date, now: now))" : ""
            if x.status != "completed" {
                run = x.status == "queued" ? "queued" : "running"
                runTone = .pending
            } else {
                run = (x.conclusion.isEmpty ? "done" : x.conclusion) + when
                switch x.conclusion {
                case "success": runTone = .good
                case "failure", "timed_out", "startup_failure": runTone = .bad
                default: runTone = .muted
                }
            }
        } else {
            run = ""
            runTone = .muted
        }
        error = r.error
        releaseURL = URL(string: r.releaseURL).flatMap { $0.scheme == nil ? nil : $0 }
        compareURL = r.ahead > 0 ? URL(string: r.compareURL).flatMap { $0.scheme == nil ? nil : $0 } : nil
        runURL = r.hasLastRun ? URL(string: r.lastRun.url).flatMap { $0.scheme == nil ? nil : $0 } : nil
        repoURL = URL(string: "https://github.com/\(r.repo)")
    }
}

public enum ReleaseList {
    /// The repos in repos.yaml's order.
    public static func rows(_ s: Ghhud_V1_Snapshot, now: Date = Date()) -> [ReleaseRow] {
        guard s.hasReleases else { return [] }
        return s.releases.repos.map { ReleaseRow($0, now: now) }
    }

    /// The sidebar's badge: repos whose last release run failed.
    public static func failing(_ s: Ghhud_V1_Snapshot) -> Int {
        s.releases.repos.filter { $0.hasLastRun && $0.lastRun.status == "completed" && $0.lastRun.conclusion == "failure" }.count
    }

    /// The pane's header: "15 repos · 9 with unreleased commits · checked
    /// 3m ago", or why repos.yaml couldn't be read.
    public static func state(_ s: Ghhud_V1_Snapshot, now: Date = Date()) -> String {
        let r = s.releases
        if !r.error.isEmpty { return r.error }
        var parts: [String] = []
        if !r.repos.isEmpty {
            parts.append("\(r.repos.count) repos")
            let ahead = r.repos.filter { $0.ahead > 0 }.count
            if ahead > 0 { parts.append("\(ahead) with unreleased commits") }
        }
        if r.checking {
            parts.append("checking…")
        } else if r.hasCheckedAt {
            parts.append("checked \(HubSummary.ago(r.checkedAt.date, now: now))")
        }
        return parts.joined(separator: " · ")
    }
}
