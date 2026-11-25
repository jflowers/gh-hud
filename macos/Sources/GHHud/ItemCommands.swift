import AppKit
import GHHudKit
import SwiftUI

// Commands on items: start an agent, queue one, and the run commands
// (watch, re-check, transcript). The hub does the work; the window shows
// how it went in the notice bar.

/// A request to pick an agent task for an item: to start now, or to queue.
/// With no row, you name the item (a URL or owner/repo#N) in the sheet,
/// and it may be anywhere on GitHub; named is what the field starts with.
struct AgentRequest: Identifiable {
    let id = UUID()
    let row: ItemRow?
    let queue: Bool
    var named = ""

    init(row: ItemRow, queue: Bool) {
        self.row = row
        self.queue = queue
    }

    init(named: String) {
        row = nil
        queue = false
        self.named = named
    }
}

/// The window's last command result.
struct Notice: Equatable {
    let text: String
    let bad: Bool
}

extension DashboardState {
    /// Shows a result. Good news clears itself; a failure stays until
    /// dismissed or replaced.
    func say(_ text: String, bad: Bool = false) {
        let n = Notice(text: text, bad: bad)
        notice = n
        guard !bad else { return }
        Task { @MainActor [weak self] in
            try? await Task.sleep(for: .seconds(8))
            if self?.notice == n { self?.notice = nil }
        }
    }
}

/// The commands, shared by the toolbar and the context menu.
@MainActor
struct ItemCommands {
    let conn: HubConnection
    let ui: DashboardState

    /// Why commands are off, if they are.
    var off: String? {
        conn.status == .connected ? ItemList.commandsOff(conn.snapshot) : "Not connected to the hub"
    }

    func startAgent(_ r: ItemRow) { ui.sheet = AgentRequest(row: r, queue: false) }
    /// Start Agent on URL…: the field starts with a GitHub link on the
    /// clipboard, if there is one.
    func startAgentOnURL() { ui.sheet = AgentRequest(named: Self.clipboardLink()) }

    static func clipboardLink() -> String {
        let s = NSPasteboard.general.string(forType: .string)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return Links.isItemLink(s) ? s : ""
    }
    func queue(_ r: ItemRow) { ui.sheet = AgentRequest(row: r, queue: true) }

    func watch(_ r: ItemRow) {
        guard let run = r.latestRun else { return }
        Task {
            if let why = await conn.watchRun(id: run.id) {
                ui.say("Can't watch \(run.task) on \(r.ref): \(why)", bad: true)
            } else {
                ui.say("Watching \(run.task) on \(r.ref) in a terminal on the hub's machine")
            }
        }
    }

    func recheck(_ r: ItemRow) {
        guard let run = r.latestRun else { return }
        ui.say("Re-checking \(run.task) on \(r.ref)…")
        Task {
            switch await conn.reinspect(id: run.id) {
            case .success(let now):
                ui.say("Re-checked \(run.task) on \(r.ref): \(Self.outcome(now))")
            case .failure(let why):
                ui.say("Can't re-check \(run.task) on \(r.ref): \(why)", bad: true)
            }
        }
    }

    func transcript(_ r: ItemRow) {
        guard let run = r.latestRun else { return }
        Task {
            if let why = await conn.openTranscript(id: run.id) {
                ui.say("Can't open the transcript of \(run.task) on \(r.ref): \(why)", bad: true)
            } else {
                ui.say("Opened the transcript of \(run.task) on \(r.ref) on its machine")
            }
        }
    }

    /// Asks to cancel a live run (the window confirms it).
    func askCancel(id: String, label: String) {
        ui.cancelling = CancelAsk(id: id, label: label)
    }

    func cancel(_ a: CancelAsk) {
        ui.say("Cancelling \(a.label)…")
        Task {
            if let why = await conn.cancelRun(id: a.id) {
                ui.say("Can't cancel \(a.label): \(why)", bad: true)
            } else {
                ui.say("Cancelled \(a.label); its checkout is being cleaned up")
            }
        }
    }

    static func outcome(_ r: Ghhud_V1_Run) -> String {
        let words = RunTag.words(r.status)
        return r.summary.isEmpty ? words : "\(words) — \(r.summary)"
    }

    /// The agent commands for the context menu.
    @ViewBuilder
    func menu(_ r: ItemRow) -> some View {
        let off = off
        Button("Start Agent…") { startAgent(r) }
            .disabled(off != nil)
        Button("Add to Queue…") { queue(r) }
            .disabled(off != nil)
        if let run = r.latestRun {
            Divider()
            Button("Watch \(run.task) Run") { watch(r) }
                .disabled(off != nil || !run.live || conn.isRemote)
            Button("Re-check \(run.task) Run") { recheck(r) }
                .disabled(off != nil || !run.finished)
            Button("Open \(run.task) Transcript") { transcript(r) }
                .disabled(off != nil || !run.hasTranscript || conn.isRemote)
            if run.live {
                Button("Cancel \(run.task) Run…") { askCancel(id: run.id, label: "\(run.task) on \(r.ref)") }
                    .disabled(off != nil)
            }
        }
    }
}

/// A live run you asked to cancel, waiting for you to confirm.
struct CancelAsk: Identifiable, Equatable {
    let id: String // the run's
    let label: String // "feedback on acme/r#5"
}

/// Loads the choices for an item and runs the one you pick.
@MainActor
final class AgentSheetModel: ObservableObject {
    struct Choice: Identifiable, Equatable {
        var id: String { task + "|" + target }
        let task: String
        let target: String
        let cmd: String
        var suggested = false
    }

    enum Phase: Equatable {
        /// Waiting for you to name the item.
        case asking
        case loading
        case choices([Choice])
        case failed(String)
    }

    @Published var phase: Phase = .loading
    @Published var running: String?
    @Published var error: String?
    /// The item you named, and what the hub found for it.
    @Published var named = ""
    @Published var found: Ghhud_V1_Item?

    /// The item's URL for the hub, and its ref for messages.
    func target(_ req: AgentRequest) -> (url: String, ref: String)? {
        if let row = req.row { return (row.id, row.ref) }
        guard let it = found else { return nil }
        return (it.url, "\(it.repo)#\(it.num)")
    }

    func load(_ conn: HubConnection, _ req: AgentRequest) async {
        guard let row = req.row else {
            named = req.named
            if named.isEmpty {
                phase = .asking
            } else {
                await lookUp(conn)
            }
            return
        }
        if req.queue {
            switch await conn.queueChoices(url: row.id) {
            case .success(let tasks):
                phase = tasks.isEmpty
                    ? .failed("No agent tasks to queue for this item.")
                    : .choices(tasks.map { Choice(task: $0, target: "your queue", cmd: "") })
            case .failure(let why):
                phase = .failed("Can't queue: \(why)")
            }
        } else {
            switch await conn.menuChoices(url: row.id) {
            case .success(let res):
                phase = res.choices.isEmpty
                    ? .failed(HubConnection.nothingCanStart(on: row.ref, why: res.why,
                                                            guess: "no agent task fits it, or no checkout of \(row.repo) is free"))
                    : .choices(res.choices.map { Choice(task: $0.task, target: $0.target, cmd: $0.cmd) })
            case .failure(let why):
                phase = .failed("The agent menu failed: \(why)")
            }
        }
    }

    /// Asks the hub about the item you named.
    func lookUp(_ conn: HubConnection) async {
        let ref = named.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !ref.isEmpty else { return }
        found = nil
        error = nil
        phase = .loading
        switch await conn.itemChoices(ref: ref) {
        case .success(let res):
            found = res.item
            phase = res.choices.isEmpty
                ? .failed(HubConnection.nothingCanStart(on: "\(res.item.repo)#\(res.item.num)", why: res.why,
                                                        guess: "no checkout of \(res.item.repo) is free, or a person handles it (human-gate)"))
                : .choices(res.choices.map { Choice(task: $0.task, target: $0.target, cmd: $0.cmd, suggested: $0.suggested) })
        case .failure(let why):
            phase = .failed("\(why)")
        }
    }

    /// Runs a choice; returns what to say when it worked.
    func run(_ c: Choice, _ conn: HubConnection, _ req: AgentRequest) async -> String? {
        guard let (url, ref) = target(req) else { return nil }
        running = c.id
        error = nil
        defer { running = nil }
        if req.queue {
            switch await conn.enqueue(task: c.task, url: url) {
            case .success(let pos):
                return "Queued \(c.task) for \(ref), number \(pos) in your queue"
            case .failure(let why):
                error = "\(why)"
                return nil
            }
        }
        switch await conn.dispatch(task: c.task, url: url) {
        case .success(let rec):
            let at = rec.workspace.isEmpty ? "" : " in \(rec.machine.isEmpty ? "local" : rec.machine):\(rec.workspace)"
            return "Started \(rec.task.isEmpty ? c.task : rec.task) on \(ref)\(at)"
        case .failure(let why):
            error = "\(why)"
            return nil
        }
    }
}

/// The sheet that offers the agent tasks for an item.
struct AgentSheet: View {
    let conn: HubConnection
    let req: AgentRequest
    @ObservedObject var ui: DashboardState
    @StateObject private var model = AgentSheetModel()

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(req.queue ? "Add to your queue" : "Start an agent").font(.headline)
                if let row = req.row {
                    Text("\(row.ref) · \(row.title)")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            if req.row == nil { nameField }
            content
            if let err = model.error {
                Label(err, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red)
                    .font(.callout)
            }
            HStack {
                Spacer()
                Button("Cancel") { ui.sheet = nil }
                    .keyboardShortcut(.cancelAction)
            }
        }
        .padding(16)
        .frame(width: 460)
        .task { await model.load(conn, req) }
    }

    /// Where you name the item, and what the hub found.
    @ViewBuilder
    private var nameField: some View {
        HStack {
            TextField("Issue or PR URL, or owner/repo#N", text: $model.named)
                .textFieldStyle(.roundedBorder)
                .onSubmit { Task { await model.lookUp(conn) } }
            Button("Look Up") { Task { await model.lookUp(conn) } }
                .disabled(model.named.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                    || model.phase == .loading || model.running != nil)
        }
        if let it = model.found {
            Text(verbatim: "\(it.kind == .issue ? "Issue" : "PR") \(it.repo)#\(it.num) · \(it.title)")
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineLimit(2)
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.phase {
        case .asking:
            Text("Paste a link to any open issue or pull request. The hub offers every task that fits it, the ones it looks like it needs first.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        case .loading:
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text(req.row == nil ? "Asking the hub about it…"
                    : req.queue ? "Asking the hub what can be queued…" : "Asking the hub where an agent can start…")
                    .foregroundStyle(.secondary)
            }
        case .failed(let why):
            Text(why).foregroundStyle(.secondary)
        case .choices(let cs):
            VStack(alignment: .leading, spacing: 6) {
                ForEach(cs) { c in
                    choice(c)
                }
            }
        }
    }

    private func choice(_ c: AgentSheetModel.Choice) -> some View {
        Button {
            Task {
                if let said = await model.run(c, conn, req) {
                    ui.sheet = nil
                    ui.say(said)
                }
            }
        } label: {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(c.task).font(.body.weight(.medium))
                        if c.suggested && req.row == nil {
                            Text("suggested")
                                .font(.caption2.weight(.medium))
                                .padding(.horizontal, 5)
                                .padding(.vertical, 1)
                                .background(Capsule().fill(Color.accentColor.opacity(0.18)))
                        }
                    }
                    if !c.cmd.isEmpty {
                        Text(c.cmd).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer()
                if model.running == c.id {
                    ProgressView().controlSize(.small)
                } else {
                    Text(c.target).foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.bordered)
        .disabled(model.running != nil)
    }
}

/// The last result, along the bottom of the window.
struct NoticeBar: View {
    let notice: Notice
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: notice.bad ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
                .foregroundStyle(notice.bad ? .red : .green)
            Text(notice.text).lineLimit(2).textSelection(.enabled)
            Spacer()
            Button(action: dismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.borderless)
            .help("Dismiss")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.bar)
    }
}
