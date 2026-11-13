import Connect
import Foundation
import Observation

/// A live connection to the hub: it finds the hub through hub.json,
/// subscribes, and keeps the latest snapshot. When the hub goes away it
/// says so and reconnects with backoff, reading hub.json again each time
/// (a restarted hub has a new port and token).
@MainActor
@Observable
public final class HubConnection {
    public enum Status: Equatable, Sendable {
        case connecting
        case connected
        /// No hub.json, or it can't be read: no hub is running.
        case noHub(String)
        /// We had a hub and lost it; reconnecting.
        case lost(String)
    }

    public private(set) var status: Status = .connecting
    public private(set) var snapshot = Ghhud_V1_Snapshot()
    public private(set) var attention: Attention = .empty

    /// Called for each alert the hub sends (on the main actor).
    @ObservationIgnored public var onAlert: (@MainActor (Ghhud_V1_Alert) -> Void)?

    @ObservationIgnored private let stateDir: URL
    @ObservationIgnored private var task: Task<Void, Never>?
    @ObservationIgnored private var nativeNotifications = false
    @ObservationIgnored private var hub: HubDiscovery?
    /// The client for commands, made once per hub (a URLSession each).
    @ObservationIgnored private var commands: Ghhud_V1_HubServiceClient?

    private static let firstRetry: Duration = .milliseconds(250)
    private static let longestRetry: Duration = .seconds(5)

    public init(stateDir: URL = HubDiscovery.defaultStateDir) {
        self.stateDir = stateDir
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
    public func refresh() async -> String? {
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_RefreshRequest()
        req.target = .github
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
        }
    }

    /// Rejects a supervisor proposal. Returns why it failed, if it did.
    public func reject(_ a: Approval) async -> String? {
        guard case .proposal(let id) = a else { return "only a proposal can be rejected" }
        guard let hub, let client = commands else { return "not connected" }
        var req = Ghhud_V1_RejectProposalRequest()
        req.id = id
        return Self.why(await client.rejectProposal(request: req, headers: hub.authHeaders))
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
                d = try HubDiscovery.load(stateDir: stateDir)
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
            if await stream(d) {
                hadHub = true
                wait = Self.firstRetry
            }
            if Task.isCancelled { return }
            status = hadHub ? .lost("lost the hub; reconnecting…") : .connecting
            try? await Task.sleep(for: wait)
            wait = min(wait * 2, Self.longestRetry)
        }
    }

    /// Runs one subscription; reports whether it delivered anything.
    private func stream(_ d: HubDiscovery) async -> Bool {
        let client = Self.client(for: d, streaming: true)
        let st = client.subscribe(headers: d.authHeaders)
        defer { st.cancel() }
        var req = Ghhud_V1_SubscribeRequest()
        req.nativeNotifications = nativeNotifications
        do {
            try st.send(req)
        } catch {
            return false
        }
        var got = false
        for await result in st.results() {
            if Task.isCancelled { break }
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
        return got
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
