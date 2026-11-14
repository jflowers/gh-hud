import AppKit
import GHHudKit
import SwiftUI

// Commands on items: start an agent, queue one, and the run commands
// (watch, re-check, transcript). The hub does the work; the window shows
// how it went in the notice bar.

/// A request to pick an agent task for an item: to start now, or to queue.
struct AgentRequest: Identifiable {
    let id = UUID()
    let row: ItemRow
    let queue: Bool
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
                .disabled(off != nil || !run.live)
            Button("Re-check \(run.task) Run") { recheck(r) }
                .disabled(off != nil || !run.finished)
            Button("Open \(run.task) Transcript") { transcript(r) }
                .disabled(off != nil || !run.hasTranscript)
        }
    }
}

/// Loads the choices for an item and runs the one you pick.
@MainActor
final class AgentSheetModel: ObservableObject {
    struct Choice: Identifiable, Equatable {
        var id: String { task + "|" + target }
        let task: String
        let target: String
        let cmd: String
    }

    enum Phase: Equatable {
        case loading
        case choices([Choice])
        case failed(String)
    }

    @Published var phase: Phase = .loading
    @Published var running: String?
    @Published var error: String?

    func load(_ conn: HubConnection, _ req: AgentRequest) async {
        if req.queue {
            switch await conn.queueChoices(url: req.row.id) {
            case .success(let tasks):
                phase = tasks.isEmpty
                    ? .failed("No agent tasks to queue for this item.")
                    : .choices(tasks.map { Choice(task: $0, target: "your queue", cmd: "") })
            case .failure(let why):
                phase = .failed("Can't queue: \(why)")
            }
        } else {
            switch await conn.menuChoices(url: req.row.id) {
            case .success(let cs):
                phase = cs.isEmpty
                    ? .failed("Nothing can start on this item now: no agent task fits it, or no checkout of \(req.row.repo) is free.")
                    : .choices(cs.map { Choice(task: $0.task, target: $0.target, cmd: $0.cmd) })
            case .failure(let why):
                phase = .failed("The agent menu failed: \(why)")
            }
        }
    }

    /// Runs a choice; returns what to say when it worked.
    func run(_ c: Choice, _ conn: HubConnection, _ req: AgentRequest) async -> String? {
        running = c.id
        error = nil
        defer { running = nil }
        let ref = req.row.ref
        if req.queue {
            switch await conn.enqueue(task: c.task, url: req.row.id) {
            case .success(let pos):
                return "Queued \(c.task) for \(ref), number \(pos) in your queue"
            case .failure(let why):
                error = "\(why)"
                return nil
            }
        }
        switch await conn.dispatch(task: c.task, url: req.row.id) {
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
                Text("\(req.row.ref) · \(req.row.title)")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
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

    @ViewBuilder
    private var content: some View {
        switch model.phase {
        case .loading:
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text(req.queue ? "Asking the hub what can be queued…" : "Asking the hub where an agent can start…")
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
                    Text(c.task).font(.body.weight(.medium))
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
