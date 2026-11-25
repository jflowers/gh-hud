import Connect
import Foundation
import Observation

/// A live connection to the hub: it finds the hub (through hub.json for
/// this Mac's, or at the address you entered for another Mac's),
/// subscribes, and keeps the latest snapshot. When the hub goes away it
/// says so and reconnects with backoff, reading hub.json again each time
/// (a restarted hub has a new port and token).
@MainActor
@Observable
public final class HubConnection {
    public enum Status: Equatable, Sendable {
        case connecting
        case connected
        /// No hub.json, or it can't be read: no hub is running. For another
        /// Mac's hub: its address or token is missing, or it can't be
        /// reached.
        case noHub(String)
        /// We had a hub and lost it; reconnecting.
        case lost(String)
    }

    public private(set) var status: Status = .connecting
    public private(set) var snapshot = Ghhud_V1_Snapshot()
    public private(set) var attention: Attention = .empty

    /// Called for each alert the hub sends (on the main actor).
    @ObservationIgnored public var onAlert: (@MainActor (Ghhud_V1_Alert) -> Void)?

    /// Where the hub is; setSource changes it.
    public private(set) var source: HubSource
    @ObservationIgnored private var task: Task<Void, Never>?
    @ObservationIgnored private var nativeNotifications = false
    @ObservationIgnored private var hub: HubDiscovery?
    /// The client for commands, made once per hub (a URLSession each).
    @ObservationIgnored private var commands: Ghhud_V1_HubServiceClient?

    private static let firstRetry: Duration = .milliseconds(250)
    private static let longestRetry: Duration = .seconds(5)

    public init(source: HubSource) {
        self.source = source
    }

    public convenience init(stateDir: URL = HubDiscovery.defaultStateDir) {
        self.init(source: .local(stateDir: stateDir))
    }

    /// Connected (or connecting) to a hub on another Mac.
    public var isRemote: Bool { source.isRemote }

    /// Why Watch and Open Transcript are off with a hub on another Mac:
    /// they open a terminal on the hub's machine.
    public static let remoteTerminalWhy = "Watch and Open Transcript open a terminal on the hub's Mac; use them there"

    /// Switches to another hub, reconnecting if running.
    public func setSource(_ s: HubSource) {
        guard s != source else { return }
        source = s
        hub = nil
        commands = nil
        snapshot = Ghhud_V1_Snapshot()
        attention = .empty
        status = .connecting
        if task != nil {
            stop()
            start()
        }
    }

    /// Starts connecting (again, after stop).
    public func start() {
        guard task == nil else { return }
        task = Task { await self.run() }
    }

    public func stop() {
        task?.cancel()
        task = nil
    }

    /// Whether this app shows the hub's alerts as notifications. While it
    /// does, the hub mutes its own; changing it resubscribes.
    public func setNativeNotifications(_ on: Bool) {
        guard on != nativeNotifications else { return }
        nativeNotifications = on
        if task != nil {
            stop()
            start()
        }
    }

    /// Asks the hub to refresh GitHub now. Returns why it failed, if it did.
    public func refresh(_ target: Ghhud_V1_RefreshTarget = .github) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_RefreshRequest()
        req.target = target
        return Self.why(await client.refresh(request: req, headers: hub.authHeaders))
    }

    /// Approves something waiting on you. Returns why it failed, if it did.
    public func approve(_ a: Approval) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        switch a {
        case .proposal(let id):
            var req = Ghhud_V1_ApproveProposalRequest()
            req.id = id
            return Self.why(await client.approveProposal(request: req, headers: hub.authHeaders))
        case .epic(let ref, let planID):
            var req = Ghhud_V1_ApproveEpicRequest()
            req.ref = ref
            req.planID = planID
            return Self.why(await client.approveEpic(request: req, headers: hub.authHeaders))
        case .gate(let ref, let piece):
            var req = Ghhud_V1_ClearEpicGateRequest()
            req.ref = ref
            req.piece = piece
            return Self.why(await client.clearEpicGate(request: req, headers: hub.authHeaders))
        case .release(_, let tool, let version):
            var req = Ghhud_V1_ToolkitRolloutRequest()
            req.tool = tool
            req.version = version
            return Self.why(await client.toolkitRollout(request: req, headers: hub.authHeaders))
        }
    }

    /// Rejects a supervisor proposal, or declines a tool release. Returns
    /// why it failed, if it did.
    public func reject(_ a: Approval) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        switch a {
        case .proposal(let id):
            var req = Ghhud_V1_RejectProposalRequest()
            req.id = id
            return Self.why(await client.rejectProposal(request: req, headers: hub.authHeaders))
        case .release(let id, _, _):
            var req = Ghhud_V1_ToolkitControlRequest()
            req.action = .dismiss
            req.id = id
            return Self.why(await client.toolkitControl(request: req, headers: hub.authHeaders))
        default:
            return "only a proposal or a release can be turned down"
        }
    }

    /// Where an agent can start on an item now, or why none can.
    public func menuChoices(url: String) async -> Result<Ghhud_V1_MenuChoicesResponse, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_MenuChoicesRequest()
        req.url = url
        return Self.result(await client.menuChoices(request: req, headers: hub.authHeaders))
    }

    /// Says why nothing can start on ref: the hub's reason, or, from a hub
    /// too old to give one, the likely causes.
    public static func nothingCanStart(on ref: String, why: String, guess: String) -> String {
        "Nothing can start on \(ref) now: \(why.isEmpty ? guess : why)."
    }

    /// Looks up an issue or PR you name (a GitHub URL or owner/repo#N), on
    /// the dashboard or else on GitHub, and every task that can start on it.
    public func itemChoices(ref: String) async -> Result<Ghhud_V1_ItemChoicesResponse, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_ItemChoicesRequest()
        req.ref = ref
        return Self.result(await client.itemChoices(request: req, headers: hub.authHeaders))
    }

    /// Starts an agent task on an item.
    public func dispatch(task: String, url: String) async -> Result<Ghhud_V1_DispatchResponse, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_DispatchRequest()
        req.task = task
        req.url = url
        return Self.result(await client.dispatch(request: req, headers: hub.authHeaders))
    }

    /// The tasks that can join your queue for an item.
    public func queueChoices(url: String) async -> Result<[String], CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_QueueChoicesRequest()
        req.url = url
        return Self.result(await client.queueChoices(request: req, headers: hub.authHeaders)).map(\.tasks)
    }

    /// Adds a task to your queue; the result is its place (1 is first).
    public func enqueue(task: String, url: String) async -> Result<Int64, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_EnqueueRequest()
        req.task = task
        req.url = url
        return Self.result(await client.enqueue(request: req, headers: hub.authHeaders)).map(\.position)
    }

    /// Takes a task off your queue.
    public func unqueue(task: String, ref: String) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_UnqueueRequest()
        req.task = task
        req.ref = ref
        return Self.why(await client.unqueue(request: req, headers: hub.authHeaders))
    }

    /// Moves a queued task later (positive) or earlier; the result is its
    /// new index (0 first).
    public func moveQueued(task: String, ref: String, by delta: Int64) async -> Result<Int64, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_MoveQueuedRequest()
        req.task = task
        req.ref = ref
        req.delta = delta
        return Self.result(await client.moveQueued(request: req, headers: hub.authHeaders)).map(\.index)
    }

    /// Enrolls an epic (an issue URL or owner/repo#N); the result is its ref.
    public func enrollEpic(_ target: String) async -> Result<String, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_EnrollEpicRequest()
        req.target = target
        return Self.result(await client.enrollEpic(request: req, headers: hub.authHeaders)).map(\.ref)
    }

    /// Stops following an epic.
    public func dropEpic(_ ref: String) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_DropEpicRequest()
        req.ref = ref
        return Self.why(await client.dropEpic(request: req, headers: hub.authHeaders))
    }

    /// Reads an epic again.
    public func rereadEpic(_ ref: String) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_RereadEpicRequest()
        req.ref = ref
        return Self.why(await client.rereadEpic(request: req, headers: hub.authHeaders))
    }

    /// Posts a finished epic's summary, as you, and closes it: the plan
    /// you saw finished, nothing newer.
    public func finishEpic(_ ref: String, planID: String) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_FinishEpicRequest()
        req.ref = ref
        req.planID = planID
        return Self.why(await client.finishEpic(request: req, headers: hub.authHeaders))
    }

    /// Drives the fleet rollout: pause, resume, apply now, skip the drain
    /// wait, or retry the failed machine.
    public func fleet(_ action: Ghhud_V1_FleetAction) async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_FleetControlRequest()
        req.action = action
        return Self.why(await client.fleetControl(request: req, headers: hub.authHeaders))
    }

    /// Starts (or with cancel, cancels) a pass that reindexes the Dewey
    /// vaults on a fleet machine, or with machine nil on every one; force
    /// reindexes every vault, not just the ones that need it. The hub's
    /// refusal comes back as it said it.
    public func deweyReindex(machine: String?, cancel: Bool = false, force: Bool = false)
        async -> Result<Ghhud_V1_DeweyReindexResponse, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_DeweyReindexRequest()
        if let machine { req.machine = machine } else { req.all = true }
        req.cancel = cancel
        req.force = force
        return Self.result(await client.deweyReindex(request: req, headers: hub.authHeaders))
    }

    /// Starts an action on one checkout of the Workspaces list (#172). It
    /// runs on the hub in the background; what started comes back, and its
    /// outcome is the checkout's last upkeep or reindex.
    public func checkoutAction(_ h: Ghhud_V1_CheckoutHealth, _ kind: Ghhud_V1_CheckoutActionKind)
        async -> Result<String, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_CheckoutActionRequest()
        req.machine = h.machine
        req.dir = h.dir
        req.action = kind
        return Self.result(await client.checkoutAction(request: req, headers: hub.authHeaders)).map(\.started)
    }

    /// Starts rolling a tool out at version ("" for its latest release).
    /// Returns why it failed, if it did.
    public func rollOut(tool: String, version: String = "") async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_ToolkitRolloutRequest()
        req.tool = tool
        req.version = version
        return Self.why(await client.toolkitRollout(request: req, headers: hub.authHeaders))
    }

    /// Cancels, retries or dismisses a rollout, or checks the tools' releases
    /// now (id ""). Returns why it failed, if it did.
    public func toolkit(_ action: Ghhud_V1_ToolkitAction, id: String = "") async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_ToolkitControlRequest()
        req.action = action
        req.id = id
        return Self.why(await client.toolkitControl(request: req, headers: hub.authHeaders))
    }

    /// Opens a terminal attached to an agent session's pane.
    public func watch(sessionID: String) async -> String? {
        if isRemote { return Self.remoteTerminalWhy }
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_WatchRequest()
        req.sessionID = sessionID
        return Self.why(await client.watch(request: req, headers: hub.authHeaders))
    }

    /// Opens a terminal on the hub's machine attached to a live run.
    public func watchRun(id: String) async -> String? {
        if isRemote { return Self.remoteTerminalWhy }
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_WatchRunRequest()
        req.runID = id
        return Self.why(await client.watchRun(request: req, headers: hub.authHeaders))
    }

    /// Inspects a finished run's outcome again; returns the run as it is now.
    public func reinspect(id: String) async -> Result<Ghhud_V1_Run, CommandError> {
        guard let hub, let client = commands else { return .failure(.notConnected) }
        var req = Ghhud_V1_ReinspectRequest()
        req.runID = id
        return Self.result(await client.reinspect(request: req, headers: hub.authHeaders)).map(\.run)
    }

    /// Opens a run's opencode session in a terminal on its machine.
    public func openTranscript(id: String) async -> String? {
        if isRemote { return Self.remoteTerminalWhy }
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_OpenTranscriptRequest()
        req.runID = id
        return Self.why(await client.openTranscript(request: req, headers: hub.authHeaders))
    }

    /// Why a command failed, in words.
    public enum CommandError: Error, Equatable, CustomStringConvertible {
        case notConnected
        case hub(String)

        public var description: String {
            switch self {
            case .notConnected: return "not connected"
            case .hub(let why): return why
            }
        }
    }

    private static func result<T>(_ resp: ResponseMessage<T>) -> Result<T, CommandError> {
        if let why = why(resp) { return .failure(.hub(why)) }
        guard let m = resp.message else { return .failure(.hub("empty answer")) }
        return .success(m)
    }

    private static func why<T>(_ resp: ResponseMessage<T>) -> String? {
        resp.error.map { $0.message ?? String(describing: $0.code) }
    }

    private func run() async {
        var wait = Self.firstRetry
        var hadHub = false
        while !Task.isCancelled {
            let d: HubDiscovery
            do {
                d = try source.discover()
            } catch {
                hub = nil
                commands = nil
                status = .noHub(error.description)
                try? await Task.sleep(for: Self.longestRetry)
                continue
            }
            if hub != d {
                hub = d
                commands = Self.client(for: d, streaming: false)
            }
            let (got, why) = await stream(d)
            if got {
                hadHub = true
                wait = Self.firstRetry
            }
            if Task.isCancelled { return }
            if hadHub {
                status = .lost("lost the hub; reconnecting…")
            } else if source.isRemote, let why {
                // A hub on another Mac: say why it can't be reached (wrong
                // address or token, the hub's lan off, the network).
                status = .noHub("can't reach the hub at \(d.url): \(why)")
            } else {
                status = .connecting
            }
            try? await Task.sleep(for: wait)
            wait = min(wait * 2, Self.longestRetry)
        }
    }

    /// Runs one subscription; reports whether it delivered anything, and
    /// why it ended when the hub said.
    private func stream(_ d: HubDiscovery) async -> (Bool, String?) {
        let client = Self.client(for: d, streaming: true)
        let st = client.subscribe(headers: d.authHeaders)
        defer { st.cancel() }
        var req = Ghhud_V1_SubscribeRequest()
        // Only this Mac's hub stops its own notifications for the app: a hub
        // on another Mac keeps notifying there.
        req.nativeNotifications = nativeNotifications && !source.isRemote
        do {
            try st.send(req)
        } catch {
            return (false, String(describing: error))
        }
        var got = false
        var why: String?
        for await result in st.results() {
            if Task.isCancelled { break }
            if case .complete(_, let error, _) = result, let error {
                why = (error as? ConnectError)?.message ?? String(describing: error)
            }
            guard case .message(let m) = result else { continue }
            switch m.msg {
            case .snapshot(let s):
                apply(s)
                got = true
                status = .connected
            case .event(let e):
                if e.kind == .alert {
                    onAlert?(e.alert)
                }
            case nil:
                break
            }
        }
        return (got, why)
    }

    /// Applies a snapshot as if it came from the hub (also for previews
    /// and checks).
    public func apply(_ s: Ghhud_V1_Snapshot) {
        snapshot = s
        attention = Attention(s)
    }

    /// A client for the hub. The subscription gets its own session with a
    /// long timeout: an idle hub sends nothing, and URLSession would
    /// otherwise give up on the stream after a minute of quiet.
    private static func client(for d: HubDiscovery, streaming: Bool) -> Ghhud_V1_HubServiceClient {
        let conf = URLSessionConfiguration.ephemeral
        if streaming {
            conf.timeoutIntervalForRequest = 24 * 3600
            conf.timeoutIntervalForResource = .infinity
        } else {
            conf.timeoutIntervalForRequest = 15
        }
        let pc = ProtocolClient(
            httpClient: URLSessionHTTPClient(configuration: conf),
            config: ProtocolClientConfig(host: d.url, networkProtocol: .connect, codec: ProtoCodec()))
        return Ghhud_V1_HubServiceClient(client: pc)
    }
}
