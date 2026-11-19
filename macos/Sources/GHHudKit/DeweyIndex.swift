import Foundation

// Each checkout's Dewey index (#139), as the Workspaces pane, a run's
// detail and the Fleet pane show it: the TUI's index column and detail
// (internal/ui/workspaces_index.go) and its fleet "dewey" lines. A reindex
// need doesn't block a dispatch; a machine problem is one a reindex won't
// fix. Pure functions, so the checks cover them.

public enum DeweyIndex {
    /// CheckoutHealth.index for a checkout that isn't a Dewey vault.
    public static let none = "none"
    /// CheckoutHealth.index for an index whose dewey status hasn't been
    /// read since it was built.
    public static let unchecked = "unchecked"

    /// An index verdict ("reindex needed: a, b; machine problem: …") split
    /// into its reindex reasons, machine problem and check error. Prefer
    /// CheckoutHealth.indexReasons for the reasons: Dewey's contain commas.
    public static func parts(_ index: String) -> (reasons: [String], machine: String, failed: String) {
        var reasons: [String] = []
        var machine = "", failed = ""
        for p in index.components(separatedBy: "; ") {
            if p.hasPrefix("reindex needed: ") {
                reasons = p.dropFirst("reindex needed: ".count).components(separatedBy: ", ")
            } else if p.hasPrefix("machine problem: ") {
                machine = String(p.dropFirst("machine problem: ".count))
            } else if p.hasPrefix("check failed: ") {
                failed = String(p.dropFirst("check failed: ".count))
            }
        }
        return (reasons, machine, failed)
    }

    /// The checkout's reindex reasons: indexReasons, else split out of
    /// index (a hub that doesn't send the list).
    public static func reasons(_ h: Ghhud_V1_CheckoutHealth) -> [String] {
        let split = parts(h.index).reasons
        return !h.indexReasons.isEmpty && !split.isEmpty ? h.indexReasons : split
    }

    /// A reason as the column names it: gh-hud's own as they are, Dewey's
    /// (sentences like "index format 0, this Dewey writes 1") by what
    /// they're about. The detail has them in full.
    public static func short(_ reason: String) -> String {
        if reason == "unindexed" || reason == "no embeddings" { return reason }
        if reason.hasPrefix("index format") { return "old format" }
        return "dewey: reindex"
    }

    /// A machine problem in a word or two.
    static func machineShort(_ machine: String) -> String {
        if machine.contains("no dewey") { return "no dewey" }
        if machine.contains("is older than") { return "dewey too old" }
        return "no embeddings"
    }

    /// The share of blocks with an embedding, "" when dewey status hasn't
    /// been read or the index is empty.
    public static func coverage(_ h: Ghhud_V1_CheckoutHealth) -> String {
        guard h.hasIndexStatusAt, h.indexBlocks > 0 else { return "" }
        return "\(h.indexEmbeddings * 100 / h.indexBlocks)%"
    }

    /// The index's state in a word or two, without its age: the Index
    /// column's filter value. "" when the checkout wasn't read.
    public static func state(_ h: Ghhud_V1_CheckoutHealth) -> (String, Tone) {
        if h.reindexing { return ("reindexing…", .pending) }
        switch h.index {
        case "": return ("", .muted)
        case none: return ("not a vault", .muted)
        default: break
        }
        let p = parts(h.index)
        let rs = reasons(h)
        if let first = rs.first {
            return (short(first) + (rs.count > 1 ? " +\(rs.count - 1)" : ""), .pending)
        }
        if !p.machine.isEmpty { return (machineShort(p.machine), .warn) }
        if !p.failed.isEmpty { return ("check failed", .bad) }
        if h.index == unchecked { return ("unchecked", .muted) }
        return ("ok", .good)
    }

    /// The Index column: the state, the index's age and its embedding
    /// coverage ("ok 2d 100%"); "—" for a checkout that isn't a vault or
    /// wasn't read.
    public static func column(_ h: Ghhud_V1_CheckoutHealth, now: Date = Date()) -> (String, Tone) {
        let (st, tone) = state(h)
        if h.index == "" || h.index == none { return h.reindexing ? (st, tone) : ("—", .muted) }
        var text = st
        if !h.reindexing {
            if h.hasIndexBuilt { text += " " + ItemList.age(h.indexBuilt.date, now: now) }
            let c = coverage(h)
            if !c.isEmpty { text += " " + c }
        }
        return (text, tone)
    }

    /// Sorting the Index column: most in need of you first (a failed
    /// check, a machine problem, a reindex need), then reindexing,
    /// unchecked and ok, then not a vault, then not read.
    public static func rank(_ h: Ghhud_V1_CheckoutHealth) -> Int {
        if h.reindexing { return 3 }
        switch h.index {
        case "": return 7
        case none: return 6
        default: break
        }
        let p = parts(h.index)
        if !p.failed.isEmpty { return 0 }
        if !p.machine.isEmpty { return 1 }
        if !reasons(h).isEmpty { return 2 }
        if h.index == unchecked { return 4 }
        return 5
    }

    /// What an agent dispatched here works with, per reason; Dewey's own
    /// reasons all mean the same.
    public static func means(_ reason: String) -> String {
        switch reason {
        case "unindexed": return "no index: the agent's first search builds one mid-run"
        case "no embeddings": return "keyword search only: semantic search finds nothing"
        default: return "Dewey says to rebuild it: search may miss what the newer Dewey would index"
        }
    }

    /// The selected checkout's Dewey section.
    public struct Detail: Equatable, Sendable {
        /// "Needs a reindex (still dispatchable)", "OK", "Not a Dewey vault"…
        public let verdict: String
        public let tone: Tone
        /// Each reindex reason, and what it means for an agent here.
        public let reasons: [Reason]
        /// The machine problem or check error, said in full.
        public let problems: [String]
        /// built, contents, last reindex.
        public let facts: [Fact]

        public struct Reason: Equatable, Sendable {
            public let reason: String
            public let means: String
        }

        public struct Fact: Equatable, Sendable {
            public let key: String
            public let value: String
        }
    }

    /// The Dewey section of a checkout's detail; nil when the index wasn't
    /// read.
    public static func detail(_ h: Ghhud_V1_CheckoutHealth, now: Date = Date()) -> Detail? {
        if h.index.isEmpty, !h.reindexing, !h.hasLastReindex { return nil }
        let p = parts(h.index)
        let rs = reasons(h)
        let (_, tone) = state(h)
        let verdict: String
        switch true {
        case h.reindexing: verdict = "Reindexing now: no dispatch here until it ends"
        case h.index.isEmpty: verdict = "Not read yet"
        case h.index == none: verdict = "Not a Dewey vault"
        case !rs.isEmpty: verdict = "Needs a reindex (still dispatchable)"
        case !p.machine.isEmpty: verdict = "A machine problem: a reindex here wouldn't help, fix the machine"
        case !p.failed.isEmpty: verdict = "The index check failed"
        case h.index == unchecked: verdict = "Unchecked: dewey status not read since the index was built"
        default: verdict = "OK"
        }
        var problems: [String] = []
        if !p.machine.isEmpty { problems.append("machine problem: " + p.machine) }
        if !p.failed.isEmpty { problems.append("check failed: " + p.failed) }
        var facts: [Detail.Fact] = []
        if h.hasIndexBuilt {
            var v = stamp(h.indexBuilt.date, now: now)
            if h.hasIndexStatusAt {
                v += h.indexDeweyVersion.isEmpty && h.indexFormat == 0
                    ? ", by a Dewey before 3.9.0 (format 0)"
                    : ", " + builtBy(h.indexDeweyVersion, h.indexFormat)
            }
            facts.append(.init(key: "built", value: v))
        }
        if h.hasIndexStatusAt {
            var v = "\(h.indexPages) pages · \(h.indexBlocks) blocks · \(h.indexEmbeddings) embedded"
            let c = coverage(h)
            if !c.isEmpty { v += " (\(c))" }
            facts.append(.init(key: "contents", value: v + ", as of dewey status \(ago(h.indexStatusAt.date, now: now))"))
        } else if h.hasIndexBuilt {
            facts.append(.init(key: "contents", value: "dewey status not read yet"))
        }
        if h.hasLastReindex {
            facts.append(.init(key: "last reindex", value: reindexLine(h.lastReindex, now: now)))
        }
        return Detail(verdict: verdict, tone: tone, reasons: rs.map { .init(reason: $0, means: means($0)) },
                      problems: problems, facts: facts)
    }

    /// Whether a last reindex ran and failed.
    public static func failed(_ r: Ghhud_V1_ReindexResult) -> Bool { !r.error.isEmpty }

    /// A reindex's outcome without its time: "ok in 3m12s", "failed after
    /// 20s: <err>", "skipped: <why>".
    public static func outcome(_ r: Ghhud_V1_ReindexResult) -> String {
        if !r.skipped.isEmpty { return "skipped: " + r.skipped }
        let d = goDuration(TimeInterval(r.duration.seconds) + TimeInterval(r.duration.nanos) / 1e9)
        if !r.error.isEmpty { return "failed after \(d): \(r.error)" }
        return "ok in " + d
    }

    /// A last reindex for the detail: "ok in 3m12s, Nov 18 09:30 (2h ago),
    /// for index format 0, this Dewey writes 1; now format 1 by 3.9.0".
    public static func reindexLine(_ r: Ghhud_V1_ReindexResult, now: Date = Date()) -> String {
        var s = outcome(r)
        if r.hasAt { s += ", " + stamp(r.at.date, now: now) }
        if !r.reasons.isEmpty { s += ", for " + r.reasons.joined(separator: ", ") }
        if !r.deweyVersion.isEmpty || r.indexFormat > 0 { s += "; now " + builtBy(r.deweyVersion, r.indexFormat) }
        return s
    }

    /// What built an index: "format 1 by 3.9.0".
    public static func builtBy(_ version: String, _ format: Int64) -> String {
        "format \(format)" + (version.isEmpty ? "" : " by " + version)
    }

    /// A run's index health, as its detail shows it: one line per part
    /// ("reindex needed: …", "machine problem: …"), and how it reads.
    public static func run(_ health: String) -> (lines: [String], tone: Tone)? {
        guard !health.isEmpty else { return nil }
        let p = parts(health)
        let tone: Tone = health == "ok" ? .good
            : !p.failed.isEmpty ? .bad
            : !p.machine.isEmpty ? .warn
            : !p.reasons.isEmpty ? .pending
            : .muted
        return (health.components(separatedBy: "; "), tone)
    }

    /// Go's time.Duration.String() of a duration rounded to the second:
    /// "0s", "20s", "3m12s", "1h2m3s".
    public static func goDuration(_ t: TimeInterval) -> String {
        let s = Int(t.rounded())
        if s < 60 { return "\(s)s" }
        if s < 3600 { return "\(s / 60)m\(s % 60)s" }
        return "\(s / 3600)h\((s % 3600) / 60)m\(s % 60)s"
    }

    static func ago(_ d: Date, now: Date) -> String {
        let a = ItemList.age(d, now: now)
        return a == "now" ? "just now" : a + " ago"
    }

    /// "Nov 18 09:30 (2h ago)".
    static func stamp(_ d: Date, now: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "MMM d HH:mm"
        return f.string(from: d) + " (\(ago(d, now: now)))"
    }
}

/// A machine's Dewey reindexes in the Fleet pane: the rollout's, after its
/// apply changed Dewey (#134), and the pass you asked for (#139).
public enum DeweyFleet {
    /// One line under a machine: a summary, or a vault that failed (✗) or
    /// was skipped (–).
    public struct Line: Equatable, Sendable {
        public let text: String
        public let tone: Tone
    }

    /// The pass in a few words, as the TUI's fleet view: "reindexing 3/12"
    /// while it runs, else its summary.
    public static func passText(_ p: Ghhud_V1_DeweyPass) -> String {
        if p.running {
            return p.total > 0 ? "reindexing \(p.done)/\(p.total)" : (p.note.isEmpty ? "listing vaults…" : p.note)
        }
        return p.note
    }

    /// The rollout's reindex in a few words: "reindexing 7/15" while the
    /// machine's state is reindexing, else its summary.
    public static func rolloutText(_ m: Ghhud_V1_FleetMachine) -> String {
        if m.state == "reindexing", m.reindexTotal > 0 { return "reindexing \(m.reindexDone)/\(m.reindexTotal)" }
        return m.reindexNote
    }

    /// The Fleet table's Dewey column: the running pass or reindex first,
    /// then the latest pass's summary, then the rollout's.
    public static func column(_ m: Ghhud_V1_FleetMachine) -> (String, Tone) {
        let p = m.deweyPass
        if m.hasDeweyPass, p.running { return (passText(p), .pending) }
        if m.state == "reindexing" { return (rolloutText(m), .pending) }
        if m.hasDeweyPass, !p.note.isEmpty {
            return (p.note, !p.failed.isEmpty ? .bad : !p.skipped.isEmpty ? .warn : .good)
        }
        if !m.reindexNote.isEmpty { return (m.reindexNote, m.reindexFailures.isEmpty ? .good : .bad) }
        return ("", .muted)
    }

    /// The selected machine's lines: the rollout's reindex, then the pass.
    public static func lines(_ m: Ghhud_V1_FleetMachine, now: Date = Date()) -> [Line] {
        var out: [Line] = []
        let rollout = rolloutText(m)
        if !rollout.isEmpty || !m.reindexFailures.isEmpty {
            out.append(Line(text: "rollout reindex: " + (rollout.isEmpty ? "—" : rollout),
                            tone: m.state == "reindexing" ? .pending : .muted))
            out += m.reindexFailures.map { Line(text: "✗ " + oneLine($0), tone: .bad) }
        }
        guard m.hasDeweyPass, m.deweyPass.hasStarted else { return out }
        let p = m.deweyPass
        var head = "dewey pass" + (p.forced ? " (forced)" : "") + ": " + passText(p)
        if p.running {
            head += " · started \(ItemList.age(p.started.date, now: now)) ago"
        } else if p.hasEnded {
            head += " · \(ItemList.age(p.ended.date, now: now)) ago"
        }
        out.append(Line(text: head, tone: p.running ? .pending : .muted))
        out += p.failed.map { Line(text: "✗ " + oneLine($0), tone: .bad) }
        out += p.skipped.map { Line(text: "– " + oneLine($0), tone: .warn) }
        if !p.current.isEmpty {
            out.append(Line(text: "\(p.current.count) vault\(p.current.count == 1 ? "" : "s") up to date", tone: .muted))
        }
        return out
    }

    /// Whether a pass runs on the machine (what Cancel Pass cancels).
    public static func running(_ m: Ghhud_V1_FleetMachine) -> Bool { m.hasDeweyPass && m.deweyPass.running }

    /// What asking for a pass did, for the notice: where it started (or
    /// was cancelled) and the hub's refusals as it said them. bad when
    /// nothing started.
    public static func said(_ r: Ghhud_V1_DeweyReindexResponse, cancel: Bool, force: Bool) -> (String, bad: Bool) {
        let verb = cancel ? "Cancelled the Dewey pass on" : force ? "Reindexing every Dewey vault on" : "Reindexing the Dewey vaults that need it on"
        var parts: [String] = []
        if !r.machines.isEmpty { parts.append("\(verb) \(r.machines.joined(separator: ", "))") }
        if !r.refused.isEmpty { parts.append("refused: " + r.refused.joined(separator: "; ")) }
        if parts.isEmpty { return (cancel ? "No Dewey pass to cancel" : "No Dewey pass started", true) }
        return (parts.joined(separator: " · "), r.machines.isEmpty)
    }

    /// The pass waiting for you to confirm it, and the confirmation's words.
    public struct Ask: Equatable, Sendable {
        /// nil: every fleet machine.
        public let machine: String?
        /// Every vault, not just the ones whose check says so.
        public let force: Bool

        public init(machine: String?, force: Bool) {
            self.machine = machine
            self.force = force
        }

        var on: String { machine ?? "every machine" }

        public var title: String {
            force ? "Reindex every Dewey vault on \(on)?" : "Reindex the Dewey vaults that need it on \(on)?"
        }

        public var button: String { force ? "Reindex Every Vault" : "Reindex" }

        public var message: String {
            let which = force
                ? "Every Dewey vault on \(on) is reindexed, whether or not its index needs it."
                : "The Dewey vaults on \(on) whose index check says so are reindexed; the rest are left alone."
            return which + " One vault at a time per machine; a vault with a live agent, leftovers or a held dewey.lock is skipped, and no agent starts in a vault while it's reindexed."
        }
    }

    static func oneLine(_ s: String) -> String {
        s.split(whereSeparator: \.isNewline).joined(separator: " ")
    }
}
