// GHHudKit's checks: `swift run GHHudChecks` (from macos/). XCTest and
// Swift Testing need Xcode; this needs only the Command Line Tools.
import Foundation
import GHHudKit
import SwiftProtobuf

nonisolated(unsafe) var failures = 0
nonisolated(unsafe) var checks = 0

func check(_ ok: @autoclosure () -> Bool, _ what: String, file: String = #fileID, line: Int = #line) {
    checks += 1
    if !ok() {
        failures += 1
        print("FAIL \(file):\(line): \(what)")
    }
}

func item(_ num: Int64, _ cats: UInt32, title: String, updated: TimeInterval, author: String = "") -> Ghhud_V1_Item {
    var it = Ghhud_V1_Item()
    it.repo = "acme/r"
    it.num = num
    it.url = "https://github.com/acme/r/pull/\(num)"
    it.title = title
    it.categories = cats
    it.author = author
    it.updatedAt = Google_Protobuf_Timestamp(timeIntervalSince1970: updated)
    return it
}

// MARK: discovery

do {
    let d = try HubDiscovery.parse(Data(#"{"url":"http://127.0.0.1:5","token":"t0k","pid":9}"#.utf8))
    check(d == HubDiscovery(url: "http://127.0.0.1:5", token: "t0k", pid: 9), "parse hub.json")
    check(d.authHeaders == ["Authorization": ["Bearer t0k"]], "auth header")
} catch {
    check(false, "parse hub.json: \(error)")
}
do {
    _ = try HubDiscovery.parse(Data(#"{"url":"","token":"t","pid":1}"#.utf8))
    check(false, "empty url accepted")
} catch {
    check(error == .invalid("url or token missing"), "empty url rejected: \(error)")
}
do {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    _ = try HubDiscovery.load(stateDir: dir)
    check(false, "missing hub.json loaded")
} catch {
    check(error == .noHub, "missing hub.json is noHub: \(error)")
}

// MARK: attention

do {
    var s = Ghhud_V1_Snapshot()
    s.result.items = [
        item(1, Category.reviewRequested.rawValue, title: "older review", updated: 100, author: "ann"),
        item(2, Category.reviewRequested.rawValue | Category.notifications.rawValue, title: "newer review", updated: 200),
        item(3, Category.readyForMaintainer.rawValue | Category.mine.rawValue, title: "mine, ready", updated: 50),
        item(4, Category.mine.rawValue, title: "mine, not ready", updated: 300),
    ]
    var blocked = Ghhud_V1_Session()
    blocked.id = "lap1/s1"
    blocked.blocked = true
    blocked.task = "ci"
    blocked.ref = "acme/r#3"
    blocked.machine = "lap1"
    var idle = Ghhud_V1_Session()
    idle.id = "s2"
    s.sessions = [blocked, idle]

    let a = Attention(s)
    check(a.count == 4, "count = \(a.count), want 4 (2 reviews, 1 ready, 1 blocked)")
    check(a.sections.map(\.id) == ["blocked", "review", "ready"], "sections \(a.sections.map(\.id))")
    let review = a.sections.first { $0.id == "review" }?.rows ?? []
    check(review.map(\.title) == ["newer review", "older review"], "reviews newest first: \(review.map(\.title))")
    check(review.last?.detail == "acme/r#1 · ann", "detail \(review.last?.detail ?? "")")
    let agent = a.sections.first?.rows.first
    check(agent?.detail == "ci · acme/r#3 · lap1", "agent detail \(agent?.detail ?? "")")
    check(agent?.url?.absoluteString == "https://github.com/acme/r/issues/3", "agent link")
    check(Attention(Ghhud_V1_Snapshot()) == .empty, "empty snapshot needs nothing")
}

// MARK: approvals

do {
    var s = Ghhud_V1_Snapshot()
    var pending = Ghhud_V1_Case()
    pending.id = "shared_failure|acme/r|lint"
    pending.repo = "acme/r"
    pending.check = "lint"
    pending.facts.prs = [5, 6]
    pending.proposal.id = "p1"
    pending.proposal.status = "pending"
    pending.proposal.decision.kind = "link_fix"
    pending.proposal.decision.fixPr = 9
    pending.proposal.decision.summary = "#9 fixes lint for #5 and #6\nmore detail"
    var done = pending
    done.id = "other"
    done.proposal.id = "p2"
    done.proposal.status = "applied"
    var undecided = Ghhud_V1_Case()
    undecided.id = "none"
    s.cases = [pending, done, undecided]

    var planned = Ghhud_V1_Epic()
    planned.ref = "acme/r#100"
    planned.url = "https://github.com/acme/r/issues/100"
    planned.title = "Big thing"
    planned.state = "planned"
    planned.plan.id = "plan-1"
    var n1 = Ghhud_V1_EpicNode()
    n1.state = "done"
    var n2 = Ghhud_V1_EpicNode()
    n2.state = "ready"
    planned.plan.nodes = [n1, n2]
    var stale = planned
    stale.ref = "acme/r#101"
    stale.plan.stale = true
    var approved = planned
    approved.ref = "acme/r#102"
    approved.state = "approved"
    s.epics = [planned, stale, approved]

    let a = Attention(s)
    check(a.sections.map(\.id) == ["proposals", "epics"], "approval sections \(a.sections.map(\.id))")
    check(a.count == 2, "count = \(a.count), want 2 (1 proposal, 1 epic)")
    let p = a.sections.first?.rows.first
    check(p?.approval == .proposal(id: "p1"), "proposal approval \(String(describing: p?.approval))")
    check(p?.title == "#9 fixes lint for #5 and #6", "proposal title is the summary's first line: \(p?.title ?? "")")
    check(p?.detail == "link fix · acme/r · lint", "proposal detail \(p?.detail ?? "")")
    check(p?.url?.absoluteString == "https://github.com/acme/r/issues/9", "proposal links the fix PR")
    let e = a.sections.last?.rows.first
    check(e?.approval == .epic(ref: "acme/r#100", planID: "plan-1"), "epic approval \(String(describing: e?.approval))")
    check(e?.detail == "acme/r#100 · 2 pieces, 1 open", "epic detail \(e?.detail ?? "")")
    check(Approval.proposal(id: "x").rejectable && !Approval.epic(ref: "a/b#1", planID: "p").rejectable,
          "only proposals reject")
}

// MARK: epic gates

do {
    func node(_ ref: String, _ state: String, gate: String = "", cleared: Bool = false) -> Ghhud_V1_EpicNode {
        var n = Ghhud_V1_EpicNode()
        n.ref = ref
        n.title = "piece " + ref
        n.state = state
        n.gate = gate
        n.cleared = cleared
        return n
    }
    var approved = Ghhud_V1_Epic()
    approved.ref = "acme/r#200"
    approved.state = "approved"
    approved.plan.id = "plan-2"
    approved.plan.nodes = [
        node("acme/r#201", "gated", gate: "after the 2.0 release"),
        node("acme/r#202", "waiting", gate: "after QA signs off"), // still waits on a piece
        node("acme/r#203", "ready", gate: "done already", cleared: true),
        node("acme/r#204", "done"),
    ]
    var planned = approved
    planned.ref = "acme/r#300"
    planned.state = "planned" // offered whole as a plan, not gate by gate
    var s = Ghhud_V1_Snapshot()
    s.epics = [approved, planned]

    let a = Attention(s)
    check(a.sections.map(\.id) == ["epics", "gates"], "gate sections \(a.sections.map(\.id))")
    let g = a.sections.last?.rows ?? []
    check(g.count == 1, "one gate to clear, got \(g.count)")
    check(g.first?.approval == .gate(ref: "acme/r#200", piece: "acme/r#201"),
          "gate approval \(String(describing: g.first?.approval))")
    check(g.first?.title == "after the 2.0 release", "gate title is the condition: \(g.first?.title ?? "")")
    check(g.first?.detail == "acme/r#201 · piece acme/r#201 · epic acme/r#200", "gate detail \(g.first?.detail ?? "")")
    check(g.first?.url?.absoluteString == "https://github.com/acme/r/issues/201", "gate links its piece")
    check(Approval.gate(ref: "a/b#1", piece: "a/b#2").verb == "Clear" && Approval.epic(ref: "a/b#1", planID: "p").verb == "Approve",
          "gates clear, the rest approve")
    check(!Approval.gate(ref: "a/b#1", piece: "a/b#2").rejectable, "a gate isn't rejected")
    let rel = Approval.release(id: "uf@0.22.0", tool: "uf", version: "0.22.0")
    check(rel.rejectable && rel.verb == "Approve" && rel.rejectVerb == "Decline"
          && Approval.proposal(id: "x").rejectVerb == "Reject", "a release is approved or declined")
}

// MARK: item list

do {
    var s = Ghhud_V1_Snapshot()
    s.result.user = "me"
    var mine = item(1, Category.mine.rawValue | Category.readyForMaintainer.rawValue, title: "Fix the parser", updated: 1000)
    mine.kind = .pr
    mine.ci = .ok
    mine.review = "✅"
    mine.lastActor = "me"
    var review = item(2, Category.reviewRequested.rawValue, title: "Add widgets", updated: 900, author: "pat")
    review.kind = .pr
    review.ci = .fail
    review.merge = "⚠️"
    review.waitingOn = "me"
    review.threads = 2
    review.repo = "acme/widgets"
    review.url = "https://github.com/acme/widgets/pull/2"
    var issue = item(3, Category.issues.rawValue | Category.notifications.rawValue, title: "Crash on start", updated: 800)
    issue.kind = .issue
    issue.ci = .blank
    var run = item(0, Category.ci.rawValue, title: "nightly failed", updated: 700)
    run.kind = .run
    run.url = "https://github.com/acme/r/actions/runs/9"
    s.result.items = [mine, review, issue, run]

    let all = ItemList.rows(s, filter: .all)
    check(all.map(\.num) == [1, 2, 3, 0], "all rows keep the hub's order: \(all.map(\.num))")
    check(all.map(\.kind) == ["mine", "review", "issue", "ci"], "kind badges \(all.map(\.kind))")
    check(all[0].marker == .ready && all[1].marker == .yourMove && all[2].marker == nil, "markers")
    check(all[0].ci == .good && all[0].review == .good && !all[0].conflict, "mine's status")
    check(all[1].ci == .bad && all[1].conflict && all[1].threads == 2, "review's status")
    check(all[2].ci == nil && !all[2].isPR && all[3].isPR, "issues have no status cluster, runs do")
    check(all[0].lastActor == "you", "your own last action reads you: \(all[0].lastActor)")
    check(all[3].ref == "https://github.com/acme/r/actions/runs/9", "a numberless row's ref is its URL")

    check(ItemList.rows(s, filter: .review).map(\.num) == [2], "review filter")
    check(ItemList.rows(s, filter: .inbox).map(\.num) == [3], "inbox filter")
    check(ItemList.rows(s, filter: .ready).map(\.num) == [1], "ready filter")
    check(ItemList.rows(s, filter: .queue).isEmpty, "queue filter")
    check(ItemList.rows(s, filter: .all, query: "WIDGETS").map(\.num) == [2], "search matches the repo, ignoring case")
    check(ItemList.rows(s, filter: .all, query: "crash").map(\.num) == [3], "search matches the title")
    check(ItemList.rows(s, filter: .all, query: "acme/r#1").map(\.num) == [1], "search matches the ref")
    check(ItemList.rows(s, filter: .mine, query: "widgets").isEmpty, "search stays inside the filter")
    func order<K: Comparable>(_ k: KeyPath<ItemRow, K> & Sendable, _ o: SortOrder = .forward) -> [Int] {
        all.sorted(using: KeyPathComparator(k, order: o)).map(\.num)
    }
    check(order(\.statusRank) == [2, 1, 3, 0], "status: the failing, conflicted PR first, no status last: \(order(\.statusRank))")
    check(order(\.ageKey) == [1, 2, 3, 0] && order(\.ageKey, .reverse) == [0, 3, 2, 1], "age newest first")
    check(order(\.numKey) == [1, 2, 3, 0], "numberless rows sort last")
    check(order(\.markerRank) == [2, 1, 3, 0], "your move, then ready, then the rest in order: \(order(\.markerRank))")
    check(order(\.threadsKey, .reverse).first == 2, "most threads first, descending")
    // Column filters, kept and typed.
    func nums(_ q: String, _ kept: [ColumnFilter] = []) -> [Int] {
        ItemList.rows(s, filter: .all, query: q, kept: kept).map(\.num)
    }
    check(nums("kind:issue") == [3], "a typed term")
    check(nums("KIND:Review") == [2], "terms ignore case")
    check(nums("-kind:issue -kind:ci") == [1, 2], "hide terms")
    check(nums("kind:issue kind:mine") == [1, 3], "one column's filters are alternatives")
    check(nums("kind:review repo:widgets") == [2] && nums("kind:mine repo:widgets").isEmpty, "columns all apply")
    check(nums("status:failing") == [2] && nums("status:conflict") == [2] && nums("status:approved") == [1],
          "status words: \(all.map(\.statusWords))")
    check(nums("last:you") == [1] && nums("author:pat") == [2], "people")
    check(nums("parser", [ColumnFilter("kind", "mine")]) == [1] && nums("crash", [ColumnFilter("kind", "mine")]).isEmpty,
          "kept filters and text together")
    check(nums("nope:x").isEmpty, "an unknown column stays text")
    let typed = TableQuery.parse(#"label:"good first issue" -repo:acme/r fix it"#, columns: ItemRow.filterColumns)
    check(typed.filters == [ColumnFilter("label", "good first issue"), ColumnFilter("repo", "acme/r", exclude: true)]
          && typed.text == "fix it", "quoted values: \(typed)")
    check(typed.filters.map(\.term) == [#"label:"good first issue""#, "-repo:acme/r"], "terms read back")
    var kept: [ColumnFilter] = [ColumnFilter("kind", "issue")]
    let left = kept.keepTerms(of: "crash kind:issue -author:pat", columns: ItemRow.filterColumns)
    check(left == "crash" && kept == [ColumnFilter("kind", "issue"), ColumnFilter("author", "pat", exclude: true)],
          "Return keeps the terms once: \(kept)")
    check(all[1].filterChoices.map(\.column) == ["kind", "status", "status", "repo", "title", "author"],
          "menu choices: \(all[1].filterChoices.map(\.column))")
    let n = ItemList.counts(s)
    check(n[.all] == 4 && n[.mine] == 1 && n[.issues] == 1 && n[.ci] == 1 && n[.queue] == 0, "counts \(n)")

    let now = Date(timeIntervalSince1970: 1_000_000)
    let secs: [TimeInterval] = [0, 300, 10_800, 172_800, 691_200, 6_048_000]
    let ages = secs
        .map { ItemList.age(now.addingTimeInterval(-$0), now: now) }
    check(ages == ["now", "5m", "3h", "2d", "1w", "2mo"], "ages \(ages)")
}

// MARK: agent tags and commands

do {
    let now = Date(timeIntervalSince1970: 2_000_000)
    func ts(_ ago: TimeInterval) -> Google_Protobuf_Timestamp {
        Google_Protobuf_Timestamp(date: now.addingTimeInterval(-ago))
    }
    func run(_ id: String, _ ref: String, _ status: String, started: TimeInterval, finished: TimeInterval? = nil,
             session: String = "") -> Ghhud_V1_Run {
        var r = Ghhud_V1_Run()
        r.id = id
        r.ref = ref
        r.task = "fix"
        r.status = status
        r.startedAt = ts(started)
        if let finished { r.finishedAt = ts(finished) }
        r.sessionID = session
        return r
    }

    var s = Ghhud_V1_Snapshot()
    s.result.user = "me"
    var a = item(1, Category.mine.rawValue | Category.readyForMaintainer.rawValue, title: "a", updated: 0)
    a.url = "https://github.com/acme/r/pull/1"
    let b = item(2, Category.mine.rawValue, title: "b", updated: 0)
    let c = item(3, Category.mine.rawValue, title: "c", updated: 0)
    a.waitingOn = "me"
    s.result.items = [a, b, c]
    // newest first: #1's live run hides its older failure
    s.runs = [
        run("r3", "acme/r#1", "running", started: 300, session: "ses_1"),
        run("r1", "acme/r#1", "failed", started: 9000, finished: 8000),
        run("r2", "acme/r#2", "ok", started: 3 * 86400, finished: 2 * 86400),
    ]
    var you = Ghhud_V1_Stage()
    you.kind = "you"
    you.task = "merge"
    you.reason = "approve?"
    s.stages = [a.url: you]
    var next = Ghhud_V1_Stage()
    next.kind = "next"
    next.task = "review"
    s.stages[c.url] = next

    check(ItemList.latestRuns(s)["acme/r#1"]?.id == "r3", "the newest run per ref wins")
    let rows = ItemList.rows(s, filter: .all, now: now)
    check(rows[0].marker == .needsYou, "a stage waiting on you outranks ready: \(String(describing: rows[0].marker))")
    check(rows[0].run?.text(now: now) == "fix ● 5m" && rows[0].run?.tone == .pending, "live run tag \(rows[0].run?.text(now: now) ?? "nil")")
    check(rows[0].stage?.text == "▸you: merge approve?" && rows[0].stage?.tone == .warn, "you stage \(rows[0].stage?.text ?? "nil")")
    check(rows[0].latestRun == RunRef(s.runs[0]) && rows[0].latestRun!.live && !rows[0].latestRun!.finished
          && rows[0].latestRun!.hasTranscript, "live run commands")
    check(rows[1].run == nil, "a success over a day old has no tag")
    check(rows[1].latestRun?.id == "r2" && rows[1].latestRun!.finished && !rows[1].latestRun!.live,
          "but its run can still be re-checked")
    check(rows[2].run == nil && rows[2].latestRun == nil && rows[2].stage?.text == "▸review", "no runs, next stage")

    check(RunTag(run("x", "r", "failed", started: 6 * 86400, finished: 6 * 86400), now: now) != nil, "a failure keeps its tag for a week")
    check(RunTag(run("x", "r", "left-work", started: 8 * 86400, finished: 8 * 86400), now: now) == nil, "but not longer")
    check(RunTag(run("x", "r", "blocked", started: 30 * 86400), now: now)?.tone == .warn, "a blocked run always shows")
    check(RunTag.words("left-work") == "left work behind" && RunTag.words("weird") == "outcome unknown", "run words")

    var wait = Ghhud_V1_Stage()
    wait.kind = "wait"
    wait.task = "fix"
    wait.reason = "cooldown"
    check(StageTag(wait)?.text == "▸fix wait", "a cooldown reads wait")
    wait.reason = "ci running"
    check(StageTag(wait)?.text == "▸fix ci running", "other waits say why")
    var merge = Ghhud_V1_Stage()
    merge.kind = "merge"
    check(StageTag(merge)?.text == "▸merge" && StageTag(merge)?.tone == .good, "merge stage")
    check(StageTag(Ghhud_V1_Stage()) == nil, "no stage, no tag")

    check(ItemList.commandsOff(s) == "Dispatch is off on the hub", "commands need dispatch")
    s.dispatch = true
    check(ItemList.commandsOff(s) == nil, "commands on")
    s.follower = "123"
    check(ItemList.commandsOff(s) == "Read-only: gh-hud pid 123 owns dispatch", "a follower hub refuses commands")
}

// MARK: agents and workspaces

do {
    let now = Date(timeIntervalSince1970: 3_000_000)
    func session(_ id: String, task: String = "", ref: String = "", runID: String = "", running: Bool = false,
                 updated: TimeInterval) -> Ghhud_V1_Session {
        var se = Ghhud_V1_Session()
        se.id = id
        se.title = "title " + id
        se.task = task
        se.ref = ref
        se.runID = runID
        se.running = running
        se.updatedAt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-updated))
        return se
    }
    var s = Ghhud_V1_Snapshot()
    var it = item(7, Category.mine.rawValue, title: "Seven", updated: 0)
    it.repo = "acme/r"
    s.result.items = [it]
    var run = Ghhud_V1_Run()
    run.id = "run-1"
    run.status = "left-work"
    run.startedAt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-3900))
    run.finishedAt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-600))
    s.runs = [run]
    var blocked = session("s-blocked", updated: 10)
    blocked.blocked = true
    blocked.machine = "chai"
    s.sessions = [
        session("s-run", task: "fix", ref: "acme/r#7", runID: "run-1", updated: 600),
        blocked,
        session("s-live", running: true, updated: 3 * 86400),
        session("s-old", updated: 3 * 86400),
    ]
    let rows = AgentList.rows(s, showStale: false, now: now)
    check(rows.map(\.id) == ["run-1", "s-blocked", "s-live"], "stale sessions hidden, dispatches keyed by run: \(rows.map(\.id))")
    check(AgentList.rows(s, showStale: true, now: now).count == 4, "shown on asking")
    check(rows[0].name == "acme/r#7  Seven" && rows[0].status == "left work" && rows[0].tone == .warn,
          "a dispatch reads its run: \(rows[0].name) \(rows[0].status)")
    check(rows[0].took.map(AgentList.duration) == "55m" && rows[0].run?.id == "run-1", "took \(String(describing: rows[0].took))")
    check(rows[1].status == "blocked" && rows[1].machine == "chai" && rows[1].name == "title s-blocked", "blocked session")
    check(rows[2].status == "running" && rows[2].tone == .pending && rows[2].machine == "local", "running session")
    check(AgentList.running(s) == 1, "running count")
    let live = AgentList.rows(s, showStale: false, liveOnly: true, now: now)
    check(live.map(\.id) == ["s-blocked", "s-live"], "live only: blocked and running, not the finished run: \(live.map(\.id))")

    // A task dispatched again on the same item and machine reuses the
    // session ID: each run is still its own row, with its own status.
    var again = Ghhud_V1_Snapshot()
    var newer = run
    newer.id = "run-2"
    newer.status = "running"
    newer.clearFinishedAt()
    again.runs = [newer, run]
    var idle = session("lap2/gh-issue-7", updated: 10)
    idle.blocked = true
    again.sessions = [
        session("lap2/gh-issue-7", task: "issue", ref: "acme/r#7", runID: "run-2", running: true, updated: 60),
        session("lap2/gh-issue-7", task: "issue", ref: "acme/r#7", runID: "run-1", updated: 600),
        idle, idle,
    ]
    let twice = AgentList.rows(again, showStale: false, now: now)
    check(twice.map(\.id) == ["run-2", "run-1", "lap2/gh-issue-7", "lap2/gh-issue-7#1"], "unique row IDs: \(twice.map(\.id))")
    check(twice.map(\.status) == ["running", "left work", "blocked", "blocked"], "each run its own status: \(twice.map(\.status))")
    check(twice[1].session.id == "lap2/gh-issue-7", "the session ID stays for watching and copying")
    check([45, 720, 3900].map { AgentList.duration(TimeInterval($0)) } == ["45s", "12m", "1h05"], "durations")

    // Sorting: status by how much it needs you, age newest first, unknowns
    // last, and ties left in the hub's order.
    check(rows.sorted(using: KeyPathComparator(\.statusRank)).map(\.status) == ["left work", "blocked", "running"],
          "agents by status")
    check(rows.sorted(using: KeyPathComparator(\.ageKey)).map(\.id) == ["s-blocked", "run-1", "s-live"],
          "agents newest first: \(rows.sorted(using: KeyPathComparator(\.ageKey)).map(\.id))")
    check(twice.sorted(using: KeyPathComparator(\.tookKey)).map(\.id).last == "lap2/gh-issue-7#1",
          "no duration sorts last, ties stay in order")

    func health(_ ws: String, _ change: (inout Ghhud_V1_CheckoutHealth) -> Void) -> Ghhud_V1_CheckoutHealth {
        var h = Ghhud_V1_CheckoutHealth()
        h.workspace = ws
        h.repo = "acme/r"
        h.dir = "/w/" + ws
        h.branch = "main"
        h.defaultBranch = "main"
        change(&h)
        return h
    }
    s.workspaces = [
        health("ready") { $0.behind = 3 },
        health("busy") { $0.busy = "acme/r#12"; $0.machine = "chai" },
        health("dirty") { $0.dirty = ["M a.go", "?? b.go"]; $0.ahead = 1; $0.behind = 0; $0.stashes = 1 },
        health("diverged") { $0.ahead = 2; $0.behind = 1; $0.localOnly = ["feat\t2", "wip\t1"] },
        health("lost") { $0.noUpstream = true; $0.ahead = -1; $0.behind = -1 },
        health("broken") { $0.error = "ssh: no route" },
        health("elsewhere") { $0.branch = "feat" },
        health("plain") { $0.machine = "lap1"; $0.upkeepOnly = true },
    ]
    let ws = WorkspaceList.rows(s)
    check(ws.map(\.state) == ["ready", "running #12", "2 uncommitted", "main diverged", "no upstream remote", "ssh: no route", "ready", "upkeep only"],
          "workspace states \(ws.map(\.state))")
    check(ws.map(\.tone) == [.good, .pending, .bad, .bad, .warn, .bad, .good, .muted], "workspace tones")
    check(ws.map(\.sync) == ["↓3", "=", "↑1", "↓1 ↑2", "—", "=", "=", "="], "sync \(ws.map(\.sync))")
    check(ws[1].place == "chai:busy" && ws[0].place == "ready" && ws[1].machine == "chai", "places")
    check(ws[2].notes == "1 stash" && ws[3].notes == "3 local-only", "notes \(ws[2].notes) / \(ws[3].notes)")
    check(ws[0].explanation == "A run here fast-forwards main to upstream.", "ready explanation")
    check(ws[6].explanation == "A run here switches to main and fast-forwards it.", "other branch explanation")
    check(!ws[7].ready && ws[7].explanation?.hasPrefix("ghws clones new instances") == true, "plain dir: upkeep only, never ready")
    check(ws[2].explanation == "A run here is refused until these are committed or discarded.", "dirty explanation")
    check(ws[3].explanation == "main can't fast-forward to upstream/main.", "diverged explanation")
    check(WorkspaceList.ready(s) == 2, "ready count")
    check(TableFilter.rows(ws, query: "where:chai", kept: []).map(\.workspace) == ["busy"], "workspace where")
    check(TableFilter.rows(ws, query: "uncommitted", kept: []).map(\.workspace) == ["dirty"], "free text, any column")
    check(TableFilter.rows(ws, query: "", kept: [ColumnFilter("state", "ready", exclude: true)]).count == 6, "hide ready")
    let bySync = ws.sorted(using: KeyPathComparator(\.syncKey)).map(\.workspace)
    check(bySync == ["busy", "broken", "elsewhere", "plain", "dirty", "diverged", "ready", "lost"],
          "sync: in step first, unknown last: \(bySync)")
    check(ws.sorted(using: KeyPathComparator(\.stateRank)).map(\.workspace).prefix(3) == ["dirty", "diverged", "broken"],
          "problems first")
    check(WorkspaceList.stale(s, now: now), "never read is stale")
    s.workspacesAt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-10))
    check(!WorkspaceList.stale(s, now: now), "read 10s ago is fresh")
    s.workspacesAt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-40))
    s.workspacesLoading = true
    check(!WorkspaceList.stale(s, now: now), "not while a read is under way")
}

// MARK: plan

do {
    func row(_ num: Int64, task: String, kind: String = "", reason: String = "", queued: Int64 = 0, note: String = "",
             missing: Bool = false, epic: String = "") -> Ghhud_V1_PlanRow {
        var r = Ghhud_V1_PlanRow()
        r.item = item(num, 0, title: "t\(num)", updated: 0)
        r.task = task
        r.stage.kind = kind
        r.stage.reason = reason
        r.queued = queued
        r.note = note
        r.missing = missing
        r.epic = epic
        return r
    }
    var s = Ghhud_V1_Snapshot()
    s.plan = [
        row(1, task: "fix", kind: "running", queued: 1, note: "running"),
        row(2, task: "review", queued: 2, note: "next free slot"),
        row(3, task: "fix", queued: 3, note: "no free checkout"),
        row(4, task: "fix", queued: 4, note: "not in the latest refresh", missing: true),
        row(5, task: "review", kind: "next"),
        row(6, task: "implement", kind: "next", epic: "acme/r#50"),
        row(7, task: "fix", kind: "wait", reason: "CI running"),
        row(8, task: "implement", kind: "you", reason: "gate: after QA", epic: "acme/r#50"),
        row(9, task: "", kind: "merge"),
    ]
    let gs = PlanList.groups(s)
    check(gs.map(\.title) == ["Your queue", "Auto, next", "Waiting", "Needs you", "Merging"], "plan groups \(gs.map(\.title))")
    let rows = gs.flatMap(\.rows)
    check(rows.map(\.position) == [1, 2, 3, 4, 5, 6, nil, nil, nil], "start order \(rows.map(\.position))")
    check(rows.map(\.tone) == [.muted, .good, .bad, .muted, .good, .good, .muted, .warn, .good], "plan tones")
    check(rows[5].status == "next free slot · epic acme/r#50", "an epic piece says its epic: \(rows[5].status)")
    check(rows[8].status == "auto-merge on", "merge without a reason")
    check(rows[3].quiet && rows[6].quiet && rows[8].quiet && !rows[0].quiet, "quiet rows")
    check(rows[0].queued == 1 && rows[4].queued == nil, "queue places")
    check(rows[7].gated && !rows[5].gated, "a gate on an epic piece")
    check(rows[0].ref == "acme/r#1" && rows[0].id == "fix|acme/r#1", "plan ids")
    check(PlanList.starting(s) == 4, "starting: queued not running or missing, plus next: \(PlanList.starting(s))")
    var u1 = Ghhud_V1_MachineUsage()
    u1.name = "local"
    u1.used = 1
    u1.max = 2
    u1.ok = true
    var u2 = Ghhud_V1_MachineUsage()
    u2.name = "oak"
    s.usage = [u1, u2]
    check(PlanList.summary(s) == "4 start next · slots local 1/2, oak —", "summary \(PlanList.summary(s))")
    s.follower = "9"
    check(PlanList.summary(s).hasSuffix(" · read-only"), "a follower says read-only")

    var c = Ghhud_V1_Case()
    c.id = "k"
    c.repo = "acme/r"
    c.check = "lint"
    c.facts.prs = [1, 2]
    c.facts.onBase = true
    c.proposal.id = "p"
    c.proposal.decision.kind = "link_fix"
    c.proposal.decision.fixPr = 9
    c.proposal.decision.summary = "#9  fixes\nit"
    let cr = CaseRow(c)
    check(cr.title == "acme/r lint" && cr.scope == "2 PRs, main too", "case title \(cr.title) \(cr.scope)")
    check(cr.status == "supervisor proposes fix in #9" && cr.proposal == "p" && cr.summary == "#9 fixes it", "case proposal")
    c.proposal.status = "applied"
    c.decision.kind = "escalate"
    c.decision.summary = "flaky runner"
    c.decision.by = "supervisor"
    let decided = CaseRow(c)
    check(decided.proposal == nil && decided.status == "escalated: flaky runner (supervisor)", "decided case \(decided.status)")
    c.clearDecision()
    c.supervising = true
    check(CaseRow(c).status == "supervisor looking…", "supervising case")

    func node(_ ref: String, _ state: String, gate: String = "") -> Ghhud_V1_EpicNode {
        var n = Ghhud_V1_EpicNode()
        n.ref = ref
        n.state = state
        n.gate = gate
        return n
    }
    var e = Ghhud_V1_Epic()
    e.ref = "acme/r#50"
    e.state = "approved"
    e.plan.id = "p1"
    e.plan.nodes = [node("acme/r#51", "done"), node("acme/r#52", "waiting"), node("acme/r#53", "gated", gate: "after QA"),
                    node("acme/lib#4", "ready"), node("other/x#1", "running"), node("acme/r#54", "dropped")]
    let er = EpicRow(e)
    check(er.status == "approved · 1/6 done, 1 running, 1 ready, 0 in progress", "epic status \(er.status)")
    check(er.open.map(\.short) == ["other/x#1", "lib#4", "#53", "#52"], "open pieces, running first: \(er.open.map(\.short))")
    check(er.open[2].clearable && !er.open[3].clearable && er.planToApprove == nil, "approved: gates clear, no plan to approve")
    e.state = "planned"
    check(EpicRow(e).planToApprove == "p1" && !EpicRow(e).open[2].clearable, "planned: approve the plan, gates wait")
    e.plan.stale = true
    check(EpicRow(e).planToApprove == nil && EpicRow(e).status.hasPrefix("the epic changed"), "stale plan")
    e.state = "error"
    e.error = "no such issue"
    check(EpicRow(e).status == "can't read it: no such issue" && EpicRow(e).tone == .bad, "epic error")
    check(EpicRow.mark("gated") == "⏸" && EpicRow.mark("in_progress") == "▸", "piece marks")
}

// MARK: fleet

do {
    func machine(_ name: String, _ state: String, reachable: Bool = true, applied: String = "aaa",
                 waited: Int64 = 0, error: String = "") -> Ghhud_V1_FleetMachine {
        var m = Ghhud_V1_FleetMachine()
        m.name = name
        m.state = state
        m.reachable = reachable
        m.appliedShort = applied
        m.waited.seconds = waited
        m.lastError = error
        return m
    }
    var s = Ghhud_V1_Snapshot()
    check(FleetList.rows(s).isEmpty, "no fleet, no rows")
    s.fleet.targetShort = "bbb"
    s.fleet.machines = [
        machine("local", "current", applied: "bbb"),
        machine("chai", "draining", waited: 200),
        machine("oak", "unreachable", reachable: false, error: "ssh:\nno route"),
        machine("elm", "pending", reachable: false, applied: ""),
    ]
    var u = Ghhud_V1_MachineUsage()
    u.name = "chai"
    u.used = 1
    u.max = 2
    u.ok = true
    s.usage = [u]
    let rows = FleetList.rows(s)
    check(rows.map(\.applied) == ["bbb", "aaa → bbb", "aaa → bbb", "— → bbb"], "applied \(rows.map(\.applied))")
    check(rows.map(\.reach) == ["yes", "yes", "no", "unknown"], "reach \(rows.map(\.reach))")
    check(rows.map(\.tone) == [.good, .pending, .warn, .muted], "fleet tones")
    check(rows[1].slots == "1/2" && rows[0].slots == "—", "slots")
    check(rows[1].waited == 200 && rows[0].waited == nil, "waited only while draining")
    check(rows[2].error == "ssh: no route", "errors on one line: \(rows[2].error)")
    check(FleetList.progress(s) == "1/4", "progress")
    check(FleetList.state(s.fleet) == ("up to date", .good), "idle fleet")
    s.fleet.active = true
    check(FleetList.state(s.fleet).0 == "rolling out", "active fleet")
    s.fleet.paused = true
    check(FleetList.state(s.fleet).0 == "paused", "paused outranks active")
    s.fleet.failed = "oak"
    check(FleetList.state(s.fleet) == ("failed on oak", .bad), "failed outranks paused")
    check(FleetList.ciTone("no checks") == .warn && FleetList.ciTone("green") == .good && FleetList.ciTone("") == .muted, "CI tones")
    check(FleetList.tone("reindexing") == .pending, "a rollout's reindex is in progress, not a warning")
}

// MARK: dewey index (#139)

do {
    let now = Date(timeIntervalSince1970: 5_000_000)
    let day: TimeInterval = 86400
    func health(_ ws: String, _ change: (inout Ghhud_V1_CheckoutHealth) -> Void) -> Ghhud_V1_CheckoutHealth {
        var h = Ghhud_V1_CheckoutHealth()
        h.workspace = ws
        h.repo = "acme/r"
        h.dir = "/w/" + ws
        h.branch = "main"
        h.defaultBranch = "main"
        change(&h)
        return h
    }
    func status(_ h: inout Ghhud_V1_CheckoutHealth, pages: Int64, blocks: Int64, embedded: Int64) {
        h.indexBuilt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-2 * day))
        h.indexStatusAt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-300))
        h.indexPages = pages
        h.indexBlocks = blocks
        h.indexEmbeddings = embedded
    }
    let format = "index format 0, this Dewey writes 1"
    let hs = [
        health("ok") { status(&$0, pages: 12, blocks: 40, embedded: 40); $0.index = "ok"; $0.indexDeweyVersion = "3.9.0"; $0.indexFormat = 1 },
        health("old") {
            status(&$0, pages: 12, blocks: 40, embedded: 40)
            $0.index = "reindex needed: " + format
            $0.indexReasons = [format]
        },
        health("bare") { $0.index = "reindex needed: unindexed"; $0.indexReasons = ["unindexed"] },
        health("two") {
            status(&$0, pages: 3, blocks: 10, embedded: 0)
            $0.index = "reindex needed: no embeddings, " + format
            $0.indexReasons = ["no embeddings", format]
        },
        health("noembed") {
            status(&$0, pages: 3, blocks: 10, embedded: 0)
            $0.index = "machine problem: no embeddings, and the granite embedding model isn't available"
        },
        health("old-dewey") { $0.index = "machine problem: dewey 3.8.0 is older than 3.9.0: can't tell whether the index needs a reindex" },
        health("broken") { $0.index = "check failed: dewey status: exit 1" },
        health("busy") { $0.index = "reindex needed: unindexed"; $0.indexReasons = ["unindexed"]; $0.reindexing = true },
        health("plain") { $0.index = "none" },
        health("unread") { _ in },
        health("fresh") { $0.index = "unchecked"; $0.indexBuilt = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-3600)) },
    ]
    var s = Ghhud_V1_Snapshot()
    s.workspaces = hs
    let ws = WorkspaceList.rows(s, now: now)
    check(ws.map(\.index) == ["ok 2d 100%", "old format 2d 100%", "unindexed", "no embeddings +1 2d 0%",
                              "no embeddings 2d 0%", "dewey too old", "check failed", "reindexing…", "—", "—",
                              "unchecked 1h"],
          "index column \(ws.map(\.index))")
    check(ws.map(\.indexTone) == [.good, .pending, .pending, .pending, .warn, .warn, .bad, .pending, .muted, .muted, .muted],
          "index tones")
    check(ws[1].state == "ready" && ws[1].ready, "a reindex need is still dispatchable")
    check(ws[7].state == "reindexing" && ws[7].tone == .pending && !ws[7].ready, "reindexing takes no dispatch")
    let byIndex = ws.sorted(using: KeyPathComparator(\.indexRank)).map(\.workspace)
    check(byIndex == ["broken", "noembed", "old-dewey", "old", "bare", "two", "busy", "fresh", "ok", "plain", "unread"],
          "index sort: most in need first \(byIndex)")
    check(TableFilter.rows(ws, query: "index:\"old format\"", kept: []).map(\.workspace) == ["old"], "filter index:old format")
    check(TableFilter.rows(ws, query: "index:reindexing", kept: []).map(\.workspace) == ["busy"], "filter index:reindexing")
    check(TableFilter.rows(ws, query: "", kept: [ColumnFilter("index", "ok", exclude: true)]).count == 10, "hide ok")

    // Reasons from the structured list, not the string: Dewey's contain commas.
    check(DeweyIndex.reasons(hs[3]) == ["no embeddings", format], "structured reasons \(DeweyIndex.reasons(hs[3]))")
    var old = hs[3]
    old.indexReasons = []
    check(DeweyIndex.reasons(old) == ["no embeddings", "index format 0", "this Dewey writes 1"], "an older hub's split string")
    let p = DeweyIndex.parts("reindex needed: unindexed; machine problem: no dewey binary found")
    check(p.reasons == ["unindexed"] && p.machine == "no dewey binary found" && p.failed.isEmpty, "parts")
    check(DeweyIndex.short("dewey says so") == "dewey: reindex", "an unknown Dewey reason")

    let d = DeweyIndex.detail(hs[3], now: now)
    check(d?.verdict == "Needs a reindex (still dispatchable)" && d?.tone == .pending, "detail verdict")
    check(d?.reasons.map(\.reason) == ["no embeddings", format], "each reason on its own line")
    check(d?.reasons.first?.means == "keyword search only: semantic search finds nothing", "what a reason means")
    check(d?.facts.map(\.key) == ["built", "contents"], "detail facts \(d?.facts.map(\.key) ?? [])")
    check(d?.facts.first?.value.hasSuffix("(2d ago), by a Dewey before 3.9.0 (format 0)") == true, "built by an old Dewey: \(d?.facts.first?.value ?? "")")
    check(d?.facts.last?.value == "3 pages · 10 blocks · 0 embedded (0%), as of dewey status 5m ago", "contents: \(d?.facts.last?.value ?? "")")
    check(DeweyIndex.detail(hs[0], now: now)?.facts.first?.value.hasSuffix("(2d ago), format 1 by 3.9.0") == true, "built by")
    let m = DeweyIndex.detail(hs[4], now: now)
    check(m?.tone == .warn && m?.problems == ["machine problem: no embeddings, and the granite embedding model isn't available"]
          && m?.verdict.contains("wouldn't help") == true, "machine problem detail")
    check(DeweyIndex.detail(hs[8], now: now)?.verdict == "Not a Dewey vault", "not a vault")
    check(DeweyIndex.detail(hs[9], now: now) == nil, "not read: no section")
    check(DeweyIndex.detail(hs[7], now: now)?.verdict.hasPrefix("Reindexing now") == true, "reindexing verdict")

    var r = Ghhud_V1_ReindexResult()
    r.at = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-7200))
    r.duration.seconds = 192
    r.reasons = [format]
    r.deweyVersion = "3.9.0"
    r.indexFormat = 1
    let line = DeweyIndex.reindexLine(r, now: now)
    check(line.hasPrefix("ok in 3m12s, ") && line.hasSuffix(" (2h ago), for \(format); now format 1 by 3.9.0"), "last reindex: \(line)")
    r.error = "dewey reindex: boom"
    r.duration.seconds = 20
    check(DeweyIndex.outcome(r) == "failed after 20s: dewey reindex: boom", "failed reindex")
    r.skipped = "dewey.lock held"
    check(DeweyIndex.outcome(r) == "skipped: dewey.lock held", "skipped reindex")
    check(DeweyIndex.goDuration(0) == "0s" && DeweyIndex.goDuration(3723) == "1h2m3s" && DeweyIndex.goDuration(180) == "3m0s",
          "Go durations")
    var failed = hs[0]
    failed.lastReindex = r
    failed.lastReindex.skipped = ""
    check(WorkspaceRow(failed, now: now).notes == "reindex failed", "a failed reindex is a note")
    check(DeweyIndex.detail(failed, now: now)?.facts.last?.key == "last reindex", "last reindex in the detail")

    // A run's index health, next to Pushed.
    check(DeweyIndex.run("") == nil, "no health: no section")
    check(DeweyIndex.run("ok")?.tone == .good, "ok run")
    let rh = DeweyIndex.run("reindex needed: unindexed; machine problem: no dewey binary found")
    check(rh?.lines == ["reindex needed: unindexed", "machine problem: no dewey binary found"] && rh?.tone == .warn, "run health lines")
    check(DeweyIndex.run("check failed: x")?.tone == .bad && DeweyIndex.run("reindex needed: unindexed")?.tone == .pending, "run tones")
}

// MARK: dewey in the fleet (#139)

do {
    let now = Date(timeIntervalSince1970: 6_000_000)
    var m = Ghhud_V1_FleetMachine()
    m.name = "lap2"
    m.state = "current"
    check(DeweyFleet.column(m) == ("", .muted) && DeweyFleet.lines(m, now: now).isEmpty, "no reindex, nothing shown")
    m.state = "reindexing"
    m.reindexDone = 7
    m.reindexTotal = 15
    m.reindexNote = "dewey pin changed (1.0.0 → 1.1.0): reindexing"
    m.reindexFailures = ["/w/acme@a/r: failed after 3s:\ndewey reindex: boom"]
    check(DeweyFleet.column(m) == ("reindexing 7/15", .pending), "rollout reindex progress")
    check(DeweyFleet.lines(m, now: now).map(\.text) == ["rollout reindex: reindexing 7/15", "✗ /w/acme@a/r: failed after 3s: dewey reindex: boom"],
          "rollout lines \(DeweyFleet.lines(m, now: now).map(\.text))")
    m.state = "current"
    m.reindexNote = "reindexed 14/15, 1 failed"
    check(DeweyFleet.column(m) == ("reindexed 14/15, 1 failed", .bad), "rollout summary")

    var p = Ghhud_V1_DeweyPass()
    p.running = true
    p.done = 3
    p.total = 12
    p.started = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-240))
    m.deweyPass = p
    check(DeweyFleet.column(m) == ("reindexing 3/12", .pending), "a running pass outranks the rollout's summary")
    check(DeweyFleet.lines(m, now: now).last?.text == "dewey pass: reindexing 3/12 · started 4m ago", "running pass line")
    check(DeweyFleet.running(m), "running")
    p.running = false
    p.done = 5
    p.total = 5
    p.reindexed = 3
    p.note = "reindexed 3/5, 1 skipped, 1 failed"
    p.failed = ["/h/acme@b/r: failed after 2s: dewey reindex: boom"]
    p.skipped = ["/h/acme@a/r: dewey.lock held"]
    p.current = ["/h/acme@c/r"]
    p.forced = true
    p.ended = Google_Protobuf_Timestamp(date: now.addingTimeInterval(-120))
    m.deweyPass = p
    check(DeweyFleet.column(m) == ("reindexed 3/5, 1 skipped, 1 failed", .bad), "finished pass summary")
    check(Array(DeweyFleet.lines(m, now: now).map(\.text).dropFirst(2)) == [
        "dewey pass (forced): reindexed 3/5, 1 skipped, 1 failed · 2m ago",
        "✗ /h/acme@b/r: failed after 2s: dewey reindex: boom",
        "– /h/acme@a/r: dewey.lock held",
        "1 vault up to date",
    ], "pass lines \(DeweyFleet.lines(m, now: now).map(\.text))")
    check(!DeweyFleet.running(m), "not running")
    var s = Ghhud_V1_Snapshot()
    s.fleet.machines = [m]
    let row = FleetList.rows(s)[0]
    check(row.dewey == "reindexed 3/5, 1 skipped, 1 failed" && row.deweyTone == .bad, "fleet row's Dewey column")
    check(TableFilter.rows([row], query: "dewey:failed", kept: []).count == 1, "filter dewey:")

    // What asking did: where it started, and the hub's refusals as it said them.
    var resp = Ghhud_V1_DeweyReindexResponse()
    resp.machines = ["lap1"]
    resp.refused = ["lap2: a pass already runs there"]
    check(DeweyFleet.said(resp, cancel: false, force: false)
          == ("Reindexing the Dewey vaults that need it on lap1 · refused: lap2: a pass already runs there", false),
          "started and refused: \(DeweyFleet.said(resp, cancel: false, force: false))")
    resp.machines = []
    check(DeweyFleet.said(resp, cancel: false, force: true) == ("refused: lap2: a pass already runs there", true), "only refused is bad")
    check(DeweyFleet.said(Ghhud_V1_DeweyReindexResponse(), cancel: true, force: false) == ("No Dewey pass to cancel", true), "nothing to cancel")
    resp.machines = ["lap1", "lap2"]
    resp.refused = []
    check(DeweyFleet.said(resp, cancel: true, force: false).0 == "Cancelled the Dewey pass on lap1, lap2", "cancelled")

    let ask = DeweyFleet.Ask(machine: "lap2", force: true)
    check(ask.title == "Reindex every Dewey vault on lap2?" && ask.button == "Reindex Every Vault", "forced ask")
    check(DeweyFleet.Ask(machine: nil, force: false).title == "Reindex the Dewey vaults that need it on every machine?", "every machine ask")

    check(Links.alertTitle(.reindexFailed) == "A Dewey reindex failed" && Links.paneForAlert(.reindexFailed) == "workspaces",
          "reindex-failed alert opens Workspaces")
    var alert = Ghhud_V1_Alert()
    alert.type = .reindexFailed
    alert.ref = "lap2:r@hoopoe"
    check(Links.forAlert(alert) == nil, "a reindex alert has no GitHub link")
    check(Links.paneForAlert(.toolRelease) == "toolkit" && Links.paneForAlert(.ready) == nil, "other alerts' panes")
}

// MARK: summary and links

do {
    var s = Ghhud_V1_Snapshot()
    s.result.user = "me"
    let now = Date(timeIntervalSince1970: 10_000)
    s.fetchedAt = Google_Protobuf_Timestamp(timeIntervalSince1970: 10_000 - 180)
    check(HubSummary.line(s, now: now) == "me · updated 3m ago", "summary: \(HubSummary.line(s, now: now))")
    s.follower = "42"
    s.fleet.paused = true
    check(HubSummary.line(s, now: now) == "me · updated 3m ago · read-only: gh-hud pid 42 owns dispatch · fleet paused",
          "follower and fleet: \(HubSummary.line(s, now: now))")
    s.fetchError = "gh: not logged in"
    check(HubSummary.line(s, now: now) == "GitHub: gh: not logged in", "fetch error wins")
    check(HubSummary.ago(now.addingTimeInterval(-90_000), now: now) == "1d ago", "ago in days")

    check(Links.forRef("acme/r#12")?.absoluteString == "https://github.com/acme/r/issues/12", "ref link")
    check(Links.forRef("acme/r") == nil && Links.forRef("r#1") == nil && Links.forRef("a/b#x") == nil, "bad refs")
    for s in ["https://github.com/acme/r/pull/12", " github.com/acme/r/issues/3#issuecomment-9\n",
              "https://github.com/acme/r/pull/12/files"] {
        check(Links.isItemLink(s), "item link \(s)")
    }
    for s in ["", "https://github.com/acme/r", "https://github.com/acme/r/commit/abc", "https://gitlab.com/a/r/pull/1",
              "https://github.com/acme/r/pull/0", "see https://github.com/acme/r/pull/1"] {
        check(!Links.isItemLink(s), "not an item link \(s)")
    }
    var alert = Ghhud_V1_Alert()
    alert.run.ref = "acme/r#7"
    check(Links.forAlert(alert)?.absoluteString == "https://github.com/acme/r/issues/7", "alert falls back to its run")
}

// MARK: hub source (another Mac on the network)

do {
    check(HubSource.normalize("macbook.local:7377") == "http://macbook.local:7377", "bare host:port gets http")
    check(HubSource.normalize(" http://10.0.0.5:7377/ ") == "http://10.0.0.5:7377", "trimmed, no trailing slash")
    check(HubSource.normalize("macbook.example.com") == "http://macbook.example.com:7377", "bare host gets the hub's port")
    check(HubSource.normalize("[fd7a::1]") == "http://[fd7a::1]:7377", "bare IPv6 gets the hub's port")
    check(HubSource.normalize("http://hub.example") == "http://hub.example", "a typed scheme keeps its own port")
    check(HubSource.normalize("https://hub.example:443") == "https://hub.example:443", "https kept")
    check(HubSource.normalize("") == nil && HubSource.normalize("ftp://x:1") == nil, "empty and ftp refused")
    check(HubSource.normalize("http://x:1/path") == nil && HubSource.normalize("http://") == nil, "path or no host refused")
    let r = try? HubSource.remote(url: "macbook.local:7377", token: " tok\n").discover()
    check(r == HubDiscovery(url: "http://macbook.local:7377", token: "tok", pid: 0), "remote discovery \(String(describing: r))")
    check((try? HubSource.remote(url: "macbook.local:7377", token: "").discover()) == nil, "no token, no hub")
    check((try? HubSource.remote(url: "", token: "t").discover()) == nil, "no address, no hub")
    check(HubSource.remote(url: "a:1", token: "t").isRemote && !HubSource.thisMac.isRemote, "isRemote")
    let remote = HubConnection(source: .remote(url: "a:1", token: "t"))
    let why = await remote.watchRun(id: "x")
    check(why == HubConnection.remoteTerminalWhy, "remote watch is refused: \(why ?? "nil")")
    let tr = await remote.openTranscript(id: "x")
    check(tr == HubConnection.remoteTerminalWhy, "remote transcript is refused")
}

// The hub's reason when nothing can start, else the likely causes (#114).
check(HubConnection.nothingCanStart(on: "acme/r#7", why: "an agent is already running on it (fix, lap2:r@hoopoe)", guess: "no checkout")
      == "Nothing can start on acme/r#7 now: an agent is already running on it (fix, lap2:r@hoopoe).", "nothing can start: the hub's reason")
check(HubConnection.nothingCanStart(on: "acme/r#7", why: "", guess: "no checkout of acme/r is free")
      == "Nothing can start on acme/r#7 now: no checkout of acme/r is free.", "nothing can start: an old hub")

// MARK: shared fixtures (testdata/clients)

checkFixtures()

// MARK: live (optional)

// `swift run GHHudChecks --live <state dir>` also connects to the hub
// serving there, the way the app does, and waits for its first snapshot.
if let i = CommandLine.arguments.firstIndex(of: "--live"), i + 1 < CommandLine.arguments.count {
    let dir = URL(fileURLWithPath: CommandLine.arguments[i + 1], isDirectory: true)
    // With --lan <url> <token>, as another Mac would: the LAN listener.
    let conn: HubConnection
    if let l = CommandLine.arguments.firstIndex(of: "--lan"), l + 2 < CommandLine.arguments.count {
        conn = HubConnection(source: .remote(url: CommandLine.arguments[l + 1], token: CommandLine.arguments[l + 2]))
    } else {
        conn = HubConnection(stateDir: dir)
    }
    conn.start()
    var connected = false
    for _ in 0..<100 where !connected {
        try? await Task.sleep(for: .milliseconds(100))
        connected = conn.status == .connected
    }
    check(connected, "live: connected to the hub in \(dir.path) (status \(conn.status))")
    if connected {
        print("live: seq \(conn.snapshot.seq) · \(HubSummary.line(conn.snapshot))")
        let err = await conn.refresh()
        check(err == nil, "live: refresh: \(err ?? "")")
    }
    // `--approve` (only against a scratch hub such as a fake): approve
    // every row waiting on you, wait for the hub to drop them, then check
    // that an old epic plan is refused with the hub's reason.
    if connected, CommandLine.arguments.contains("--approve") {
        for row in conn.attention.sections.flatMap(\.rows) {
            guard let a = row.approval else { continue }
            let err = await conn.approve(a)
            check(err == nil, "live: approve \(row.id): \(err ?? "")")
        }
        for _ in 0..<50 where conn.attention.sections.contains(where: { $0.rows.contains { $0.approval != nil } }) {
            try? await Task.sleep(for: .milliseconds(100))
        }
        check(!conn.attention.sections.contains { $0.rows.contains { $0.approval != nil } },
              "live: nothing left to approve")
        let err = await conn.approve(.epic(ref: "acme/widgets#30", planID: "old-plan"))
        check(err != nil, "live: an old plan is refused: \(err ?? "nil")")
        if let err { print("live: hub said: \(err)") }
    }
    // `--commands` (only against the scratch fake hub in build/fakehub):
    // the item commands round trip, with the fake's canned answers.
    if connected, CommandLine.arguments.contains("--commands") {
        let url = "https://github.com/acme/widgets/pull/398"
        let menu = await conn.menuChoices(url: url)
        check((try? menu.get())?.choices.map(\.target) == ["chai:ws-3", "ws-1"], "live: menu choices \(menu)")
        let rec = await conn.dispatch(task: "fix", url: url)
        check((try? rec.get())?.workspace == "ws-3" && (try? rec.get())?.machine == "chai", "live: dispatch \(rec)")
        let opts = await conn.queueChoices(url: url)
        check((try? opts.get()) == ["fix", "review"], "live: queue choices \(opts)")
        let pos = await conn.enqueue(task: "review", url: url)
        check((try? pos.get()) == 2, "live: enqueue \(pos)")
        let watched = await conn.watchRun(id: "run-live")
        check(watched == nil, "live: watch run: \(watched ?? "")")
        let again = await conn.reinspect(id: "run-done")
        check((try? again.get())?.status == "ok", "live: re-check \(again)")
        let tr = await conn.openTranscript(id: "run-live")
        check(tr == nil, "live: transcript: \(tr ?? "")")
        let rows = ItemList.rows(conn.snapshot, filter: .all)
        let live = rows.first { $0.num == 398 }
        check(live?.run?.status == "running" && live?.latestRun?.id == "run-live", "live: the running row's tag")
        check(ItemList.commandsOff(conn.snapshot) == nil, "live: commands on")
        let w = await conn.watch(sessionID: "ses_live")
        check(w == nil, "live: watch a session: \(w ?? "")")
        let ra = await conn.refresh(.agents)
        let rw = await conn.refresh(.workspaces)
        check(ra == nil && rw == nil, "live: refresh agents and workspaces: \(ra ?? "") \(rw ?? "")")
        let agents = AgentList.rows(conn.snapshot, showStale: false)
        check(agents.map(\.status) == ["running", "left work", "blocked"], "live: agent rows \(agents.map(\.status))")
        check(WorkspaceList.ready(conn.snapshot) == 2, "live: ready checkouts")
        let un = await conn.unqueue(task: "fix", ref: "acme/api#70")
        check(un == nil, "live: unqueue: \(un ?? "")")
        let mv = await conn.moveQueued(task: "review", ref: "acme/widgets#401", by: -1)
        check((try? mv.get()) == 2, "live: move queued \(mv)")
        let en = await conn.enrollEpic("acme/widgets#60")
        check((try? en.get()) == "acme/widgets#60", "live: enroll \(en)")
        let rr = await conn.rereadEpic("acme/widgets#30")
        let dr = await conn.dropEpic("acme/widgets#60")
        check(rr == nil && dr == nil, "live: reread and drop: \(rr ?? "") \(dr ?? "")")
        let groups = PlanList.groups(conn.snapshot)
        check(groups.map(\.title) == ["Your queue", "Auto, next", "Waiting", "Needs you", "Merging"], "live: plan groups")
        check(PlanList.starting(conn.snapshot) == 3, "live: starting \(PlanList.starting(conn.snapshot))")
        for a in [Ghhud_V1_FleetAction.pause, .resume, .applyNow, .skipWait, .retry] {
            let err = await conn.fleet(a)
            check(err == nil, "live: fleet \(a): \(err ?? "")")
        }
        check(FleetList.rows(conn.snapshot).map(\.state) == ["current", "draining", "unreachable"], "live: fleet rows")
    }
    conn.stop()
}

print("\(checks - failures)/\(checks) checks passed")
exit(failures == 0 ? 0 : 1)
