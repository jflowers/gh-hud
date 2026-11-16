import Foundation
import GHHudKit
import SwiftProtobuf

// Everything the views make of a snapshot, as the text in
// testdata/clients/<name>.views.txt. The Android app's tests render the
// same text (android/core/src/test/.../Views.kt); the README there says how.

enum Views {
    /// The searches every fixture is rendered with.
    static let itemSearches = ["gizmo", "repo:widgets -author:dependabot", "label:\"good first\"", "status:\"ci failing\" kind:mine"]
    static let agentSearches = ["where:chai", "-status:failed fix"]

    static func render(_ s: Ghhud_V1_Snapshot) -> String {
        let now = s.fetchedAt.date
        var out = Out()

        let counts = ItemList.counts(s)
        for f in ItemFilter.allCases { out["items.count.\(f.rawValue)"] = counts[f] }
        out["items.commandsOff"] = ItemList.commandsOff(s)
        let items = ItemList.rows(s, filter: .all, now: now)
        for (i, r) in items.enumerated() { item(&out, "items.all[\(i)]", r, now) }
        sort(&out, "items.sort.markerRank", items, \.id, \.markerRank)
        sort(&out, "items.sort.statusRank", items, \.id, \.statusRank)
        sort(&out, "items.sort.numKey", items, \.id, \.numKey)
        sort(&out, "items.sort.threadsKey", items, \.id, \.threadsKey)
        sort(&out, "items.sort.ageKey", items, \.id, \.ageKey)
        for f in ItemFilter.allCases.dropFirst() {
            out["items.\(f.rawValue)"] = ItemList.rows(s, filter: f, now: now).map(\.id)
        }
        for q in itemSearches { out["items.search[\(quote(q))]"] = ItemList.rows(s, filter: .all, query: q, now: now).map(\.id) }

        out["agents.running"] = AgentList.running(s)
        out["agents.recent"] = AgentList.rows(s, showStale: false, now: now).map(\.id)
        out["agents.live"] = AgentList.rows(s, showStale: false, liveOnly: true, now: now).map(\.id)
        let agents = AgentList.rows(s, showStale: true, now: now)
        for (i, r) in agents.enumerated() {
            let p = "agents.all[\(i)]"
            out[p] = r.id
            out["\(p).task"] = r.task
            out["\(p).name"] = r.name
            out["\(p).machine"] = r.machine
            out["\(p).workspace"] = r.workspace
            out["\(p).status"] = r.status
            out["\(p).tone"] = tone(r.tone)
            out["\(p).statusRank"] = r.statusRank
            out["\(p).took"] = r.took.map(AgentList.duration)
            out["\(p).updated"] = epoch(r.updated)
            out["\(p).recent"] = r.recent
            out["\(p).live"] = r.live
        }
        sort(&out, "agents.sort.statusRank", agents, \.id, \.statusRank)
        sort(&out, "agents.sort.tookKey", agents, \.id, \.tookKey)
        sort(&out, "agents.sort.ageKey", agents, \.id, \.ageKey)
        for q in agentSearches {
            out["agents.search[\(quote(q))]"] = TableFilter.rows(agents, query: q, kept: []).map(\.id)
        }

        out["workspaces.ready"] = WorkspaceList.ready(s)
        out["workspaces.stale"] = WorkspaceList.stale(s, now: now)
        for (i, r) in WorkspaceList.rows(s).enumerated() {
            let p = "workspaces[\(i)]"
            out[p] = r.place
            out["\(p).machine"] = r.machine
            out["\(p).repo"] = r.repo
            out["\(p).sync"] = r.sync
            out["\(p).syncKey"] = r.syncKey
            out["\(p).state"] = r.state
            out["\(p).tone"] = tone(r.tone)
            out["\(p).notes"] = r.notes
            out["\(p).explanation"] = r.explanation
            out["\(p).ready"] = r.ready
        }

        let ws = WorkspaceList.rows(s)
        sort(&out, "workspaces.sort.stateRank", ws, \.place, \.stateRank)
        sort(&out, "workspaces.sort.syncKey", ws, \.place, \.syncKey)

        if s.hasFleet {
            let (state, t) = FleetList.state(s.fleet)
            out["fleet.state"] = state
            out["fleet.tone"] = tone(t)
            out["fleet.ci"] = tone(FleetList.ciTone(s.fleet.ci))
            out["fleet.progress"] = FleetList.progress(s)
        }
        for (i, r) in FleetList.rows(s).enumerated() {
            let p = "fleet[\(i)]"
            out[p] = r.name
            out["\(p).state"] = r.state
            out["\(p).tone"] = tone(r.tone)
            out["\(p).reach"] = r.reach
            out["\(p).slots"] = r.slots
            out["\(p).slotsUsed"] = r.slotsUsed
            out["\(p).applied"] = r.applied
            out["\(p).waited"] = r.waited.map(AgentList.duration)
            out["\(p).error"] = r.error
        }

        let fleet = FleetList.rows(s)
        sort(&out, "fleet.sort.stateRank", fleet, \.name, \.stateRank)
        sort(&out, "fleet.sort.waitedKey", fleet, \.name, \.waitedKey)
        sort(&out, "fleet.sort.slotsUsed", fleet, \.name, \.slotsUsed)

        out["plan.summary"] = PlanList.summary(s)
        out["plan.starting"] = PlanList.starting(s)
        for (g, group) in PlanList.groups(s).enumerated() {
            out["plan[\(g)]"] = group.title
            for (i, r) in group.rows.enumerated() {
                let p = "plan[\(g)][\(i)]"
                out[p] = r.id
                out["\(p).position"] = r.position
                out["\(p).title"] = r.title
                out["\(p).url"] = r.url?.absoluteString
                out["\(p).status"] = r.status
                out["\(p).tone"] = tone(r.tone)
                out["\(p).quiet"] = r.quiet
                out["\(p).queued"] = r.queued
                out["\(p).missing"] = r.missing
                out["\(p).epic"] = r.epic
                out["\(p).gated"] = r.gated
            }
        }
        for (i, c) in PlanList.cases(s).enumerated() {
            let p = "cases[\(i)]"
            out[p] = c.id
            out["\(p).title"] = c.title
            out["\(p).scope"] = c.scope
            out["\(p).status"] = c.status
            out["\(p).tone"] = tone(c.tone)
            out["\(p).summary"] = c.summary
            out["\(p).proposal"] = c.proposal
        }
        for (i, e) in PlanList.epics(s).enumerated() {
            let p = "epics[\(i)]"
            out[p] = e.ref
            out["\(p).title"] = e.title
            out["\(p).url"] = e.url?.absoluteString
            out["\(p).status"] = e.status
            out["\(p).tone"] = tone(e.tone)
            out["\(p).planToApprove"] = e.planToApprove
            for (j, c) in e.changes.enumerated() { out["\(p).changes[\(j)]"] = c }
            out["\(p).done"] = e.done
            out["\(p).total"] = e.total
            for (j, n) in e.open.enumerated() {
                let q = "\(p).open[\(j)]"
                out[q] = n.ref
                out["\(q).short"] = n.short
                out["\(q).title"] = n.title
                out["\(q).state"] = n.state
                out["\(q).mark"] = EpicRow.mark(n.state)
                out["\(q).note"] = n.note
                out["\(q).active"] = n.active
                out["\(q).clearable"] = n.clearable
            }
        }

        if s.hasToolkit {
            out["toolkit.state"] = ToolkitList.state(s)
            out["toolkit.waiting"] = ToolkitList.waiting(s)
        }
        for (i, t) in ToolkitList.tools(s).enumerated() {
            let p = "toolkit.tools[\(i)]"
            out[p] = t.name
            out["\(p).pin"] = t.pin
            out["\(p).latest"] = t.latest
            out["\(p).mark"] = t.mark
            out["\(p).tone"] = tone(t.tone)
            out["\(p).rolling"] = t.rolling
            out["\(p).proposal"] = t.proposal
            out["\(p).canRollOut"] = t.canRollOut
            out["\(p).repos"] = t.repos
            out["\(p).error"] = t.error
        }
        for (i, r) in ToolkitList.rollouts(s).enumerated() {
            let p = "toolkit.rollouts[\(i)]"
            out[p] = r.id
            out["\(p).stage"] = r.stage
            out["\(p).tone"] = tone(r.tone)
            out["\(p).counts"] = r.counts
            out["\(p).summary"] = r.summary
            out["\(p).error"] = r.error
            out["\(p).pinPR"] = r.pinPR?.absoluteString
            out["\(p).actions"] = [r.canCancel ? "cancel" : nil, r.canRetry ? "retry" : nil, r.canDismiss ? "dismiss" : nil]
                .compactMap { $0 }
            for (j, t) in r.targets.enumerated() {
                let q = "\(p).targets[\(j)]"
                out[q] = t.target
                out["\(q).state"] = t.state
                out["\(q).tone"] = tone(t.tone)
                out["\(q).url"] = t.url?.absoluteString
                out["\(q).note"] = t.note
            }
        }

        out["summary"] = HubSummary.line(s, now: now)
        let a = Attention(s)
        out["attention.count"] = a.count
        for (i, sec) in a.sections.enumerated() {
            out["attention[\(i)]"] = sec.id
            out["attention[\(i)].title"] = sec.title
            for (j, r) in sec.rows.enumerated() {
                let p = "attention[\(i)][\(j)]"
                out[p] = r.id
                out["\(p).title"] = r.title
                out["\(p).detail"] = r.detail
                out["\(p).url"] = r.url?.absoluteString
                out["\(p).updated"] = epoch(r.updated)
                switch r.approval {
                case let .proposal(id): out["\(p).approval"] = "proposal \(id)"
                case let .epic(ref, plan): out["\(p).approval"] = "epic \(ref) \(plan)"
                case let .gate(ref, piece): out["\(p).approval"] = "gate \(ref) \(piece)"
                case let .release(id, tool, version): out["\(p).approval"] = "release \(id) \(tool) \(version)"
                case nil: out["\(p).approval"] = String?.none
                }
            }
        }
        return out.text
    }

    static func item(_ out: inout Out, _ p: String, _ r: ItemRow, _ now: Date) {
        out[p] = r.id
        out["\(p).ref"] = r.ref
        out["\(p).kind"] = r.kind
        switch r.marker {
        case .ready: out["\(p).marker"] = "ready"
        case .yourMove: out["\(p).marker"] = "yourMove"
        case .needsYou: out["\(p).marker"] = "needsYou"
        case nil: out["\(p).marker"] = String?.none
        }
        out["\(p).markerRank"] = r.markerRank
        out["\(p).isPR"] = r.isPR
        out["\(p).ci"] = r.ci.map(tone)
        out["\(p).review"] = r.review.map(tone)
        out["\(p).statusRank"] = r.statusRank
        out["\(p).status"] = r.statusWords
        out["\(p).title"] = r.title
        out["\(p).author"] = r.author
        out["\(p).threads"] = "\(r.threads)/\(r.botThreads)"
        out["\(p).lastActor"] = r.lastActor
        out["\(p).updated"] = epoch(r.updated)
        out["\(p).age"] = r.updated.map { ItemList.age($0, now: now) }
        out["\(p).labels"] = r.labels
        out["\(p).run"] = r.run.map { "\($0.text(now: now)) (\(tone($0.tone)), \($0.words))" }
        out["\(p).stage"] = r.stage.map { "\($0.text) (\(tone($0.tone)))" }
        out["\(p).latestRun"] = r.latestRun.map {
            "\($0.id) \($0.task) live=\($0.live) finished=\($0.finished) transcript=\($0.hasTranscript)"
        }
    }

    /// A key's ascending order, ties kept in the list's order.
    static func sort<R, K: Comparable>(_ out: inout Out, _ name: String, _ rows: [R], _ id: KeyPath<R, String>,
                                       _ key: KeyPath<R, K>) {
        out[name] = rows.enumerated()
            .sorted { ($0.element[keyPath: key], $0.offset) < ($1.element[keyPath: key], $1.offset) }
            .map { $0.element[keyPath: id] }
    }

    static func tone(_ t: Tone) -> String { "\(t)" }

    static func epoch(_ d: Date?) -> Int64? { d.map { Int64($0.timeIntervalSince1970.rounded(.down)) } }

    /// Strings in double quotes, with \, " and newlines escaped.
    static func quote(_ s: String) -> String {
        "\"" + s.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
            .replacingOccurrences(of: "\n", with: "\\n") + "\""
    }

    struct Out {
        private var lines: [String] = []

        subscript(key: String) -> String? {
            get { nil }
            set { lines.append("\(key) = \(newValue.map(Views.quote) ?? "nil")") }
        }

        subscript(key: String) -> [String] {
            get { [] }
            set { lines.append("\(key) = [\(newValue.map(Views.quote).joined(separator: ", "))]") }
        }

        subscript<N: BinaryInteger>(key: String) -> N? {
            get { nil }
            set { lines.append("\(key) = \(newValue.map { "\($0)" } ?? "nil")") }
        }

        subscript(key: String) -> Bool {
            get { false }
            set { lines.append("\(key) = \(newValue)") }
        }

        var text: String { lines.joined(separator: "\n") + "\n" }
    }
}

/// The shared fixtures: each snapshot's views must be what its
/// .views.txt says.
func checkFixtures() {
    let dir = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().appendingPathComponent("../../../testdata/clients").standardized
    let bins = ((try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? [])
        .filter { $0.hasSuffix(".binpb") }.sorted()
    check(!bins.isEmpty, "fixtures in \(dir.path)")
    for bin in bins {
        let name = String(bin.dropLast(".binpb".count))
        do {
            let s = try Ghhud_V1_Snapshot(serializedBytes: try Data(contentsOf: dir.appendingPathComponent(bin)))
            let got = Views.render(s)
            let want = try String(contentsOf: dir.appendingPathComponent(name + ".views.txt"), encoding: .utf8)
            if got != want {
                let g = got.split(separator: "\n", omittingEmptySubsequences: false)
                let w = want.split(separator: "\n", omittingEmptySubsequences: false)
                let at = zip(g, w).enumerated().first { $0.element.0 != $0.element.1 }?.offset ?? min(g.count, w.count)
                let gl = at < g.count ? String(g[at]) : "(end)"
                let wl = at < w.count ? String(w[at]) : "(end)"
                check(false, "\(name).views.txt line \(at + 1):\n  mac:  \(gl)\n  file: \(wl)")
            } else {
                check(true, name)
            }
        } catch {
            check(false, "\(name): \(error)")
        }
    }
}
