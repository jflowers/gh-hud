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
    let n = ItemList.counts(s)
    check(n[.all] == 4 && n[.mine] == 1 && n[.issues] == 1 && n[.ci] == 1 && n[.queue] == 0, "counts \(n)")

    let now = Date(timeIntervalSince1970: 1_000_000)
    let secs: [TimeInterval] = [0, 300, 10_800, 172_800, 691_200, 6_048_000]
    let ages = secs
        .map { ItemList.age(now.addingTimeInterval(-$0), now: now) }
    check(ages == ["now", "5m", "3h", "2d", "1w", "2mo"], "ages \(ages)")
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
    var alert = Ghhud_V1_Alert()
    alert.run.ref = "acme/r#7"
    check(Links.forAlert(alert)?.absoluteString == "https://github.com/acme/r/issues/7", "alert falls back to its run")
}

// MARK: live (optional)

// `swift run GHHudChecks --live <state dir>` also connects to the hub
// serving there, the way the app does, and waits for its first snapshot.
if let i = CommandLine.arguments.firstIndex(of: "--live"), i + 1 < CommandLine.arguments.count {
    let dir = URL(fileURLWithPath: CommandLine.arguments[i + 1], isDirectory: true)
    let conn = HubConnection(stateDir: dir)
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
    conn.stop()
}

print("\(checks - failures)/\(checks) checks passed")
exit(failures == 0 ? 0 : 1)
