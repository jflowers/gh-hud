package io.github.jflowers.ghhud.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import com.connectrpc.ProtocolClientConfig
import com.connectrpc.ResponseMessage
import com.connectrpc.StreamType
import com.connectrpc.extensions.GoogleJavaLiteProtobufStrategy
import com.connectrpc.impl.ProtocolClient
import com.connectrpc.okhttp.ConnectOkHttpClient
import com.connectrpc.protocols.NetworkProtocol
import io.github.jflowers.ghhud.api.v1.Alert
import io.github.jflowers.ghhud.api.v1.EventKind
import io.github.jflowers.ghhud.api.v1.HubServiceClient
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.SubscribeResponse
import io.github.jflowers.ghhud.api.v1.DispatchResponse
import io.github.jflowers.ghhud.api.v1.DeweyReindexResponse
import io.github.jflowers.ghhud.api.v1.deweyReindexRequest
import io.github.jflowers.ghhud.api.v1.CheckoutActionKind
import io.github.jflowers.ghhud.api.v1.checkoutActionRequest
import io.github.jflowers.ghhud.api.v1.ItemChoicesResponse
import io.github.jflowers.ghhud.api.v1.MenuChoicesResponse
import io.github.jflowers.ghhud.api.v1.itemChoicesRequest
import io.github.jflowers.ghhud.api.v1.Run
import io.github.jflowers.ghhud.api.v1.approveEpicRequest
import io.github.jflowers.ghhud.api.v1.dispatchRequest
import io.github.jflowers.ghhud.api.v1.dropEpicRequest
import io.github.jflowers.ghhud.api.v1.FleetAction
import io.github.jflowers.ghhud.api.v1.TranscriptResponse
import io.github.jflowers.ghhud.api.v1.readPaneRequest
import io.github.jflowers.ghhud.api.v1.transcriptRequest
import io.github.jflowers.ghhud.api.v1.enrollEpicRequest
import io.github.jflowers.ghhud.api.v1.fleetControlRequest
import io.github.jflowers.ghhud.api.v1.moveQueuedRequest
import io.github.jflowers.ghhud.api.v1.rereadEpicRequest
import io.github.jflowers.ghhud.api.v1.finishEpicRequest
import io.github.jflowers.ghhud.api.v1.unqueueRequest
import io.github.jflowers.ghhud.api.v1.enqueueRequest
import io.github.jflowers.ghhud.api.v1.menuChoicesRequest
import io.github.jflowers.ghhud.api.v1.queueChoicesRequest
import io.github.jflowers.ghhud.api.v1.reinspectRequest
import io.github.jflowers.ghhud.api.v1.approveProposalRequest
import io.github.jflowers.ghhud.api.v1.clearEpicGateRequest
import io.github.jflowers.ghhud.api.v1.refreshRequest
import io.github.jflowers.ghhud.api.v1.rejectProposalRequest
import io.github.jflowers.ghhud.api.v1.subscribeRequest
import io.github.jflowers.ghhud.api.v1.RefreshTarget
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.api.v1.toolkitControlRequest
import io.github.jflowers.ghhud.api.v1.toolkitFixDriftRequest
import io.github.jflowers.ghhud.api.v1.toolkitRolloutRequest
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import java.util.concurrent.TimeUnit
import kotlin.time.Duration
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.Duration.Companion.seconds

/**
 * Where the hub is: its URL on the tailnet ("https://gh-hud.tailnet.ts.net"),
 * or, while developing, the Mac's loopback hub through `adb reverse` with
 * the token from hub.json.
 */
public data class HubAddress(val url: String, val token: String = "") {
    val isSet: Boolean get() = url.isNotBlank()
}

public sealed interface HubStatus {
    /** No address yet: the app asks for one. */
    public data object NotSet : HubStatus

    public data object Connecting : HubStatus

    public data object Connected : HubStatus

    /** We had no hub or lost it; retrying. */
    public data class Lost(val why: String) : HubStatus
}

public data class HubState(
    val status: HubStatus,
    val snapshot: Snapshot = Snapshot.getDefaultInstance(),
    val attention: Attention = Attention.EMPTY,
)

/** A hub call's answer: what it gave, or why it didn't. */
public sealed interface Answer<out T> {
    public data class Ok<T>(val value: T) : Answer<T>

    /**
     * unsure: the call was cut off before the hub answered (a deadline, a
     * dropped connection), so the hub may have done it anyway.
     */
    public data class No(val why: String, val unsure: Boolean = false) : Answer<Nothing>
}

/**
 * A live connection to the hub: it subscribes and keeps the latest
 * snapshot. When the hub goes away it says so and reconnects with
 * backoff. The counterpart of the Mac's HubConnection.
 */
public class HubConnection(
    public val address: HubAddress,
    private val scope: CoroutineScope,
    base: OkHttpClient = OkHttpClient(),
) {
    private val _state = MutableStateFlow(HubState(if (address.isSet) HubStatus.Connecting else HubStatus.NotSet))
    public val state: StateFlow<HubState> = _state.asStateFlow()

    private val _alerts = MutableSharedFlow<Alert>(extraBufferCapacity = 64)

    /**
     * The hub's alerts as they happen, for notifications. Alerts sent while
     * the connection is down are missed: the hub doesn't keep them.
     */
    public val alerts: SharedFlow<Alert> = _alerts.asSharedFlow()

    private val hub: HubServiceClient

    /** The client for Dispatch alone, with its longer deadline. */
    private val dispatcher: HubServiceClient
    private val headers: Map<String, List<String>> =
        if (address.token.isEmpty()) emptyMap() else mapOf("Authorization" to listOf("Bearer ${address.token}"))
    private var job: Job? = null

    init {
        // Commands get a deadline; the subscription none, since an idle hub
        // sends nothing for minutes. Over HTTP/2 (the tailnet's HTTPS) pings
        // find a connection that died quietly, so it reconnects. connect-kotlin
        // gives every call, streams too, 10s unless told otherwise, and tells
        // the hub so, which cut the subscription every 10s: timeoutOracle
        // below says otherwise.
        //
        // Dispatch gets longer: the hub gives starting an agent 2 minutes
        // (reaching a remote machine, picking a checkout, launching herdr),
        // and a phone that gave up at 30s told the hub to give up too.
        // OkHttp's callTimeout is what ends a call (connect-kotlin only
        // labels it), and it is per client, so Dispatch has its own.
        val stream = base.newBuilder().readTimeout(0, TimeUnit.SECONDS).pingInterval(60, TimeUnit.SECONDS).build()
        fun client(deadline: Duration) = HubServiceClient(
            ProtocolClient(
                httpClient = ConnectOkHttpClient(
                    base.newBuilder().callTimeout(deadline.inWholeMilliseconds, TimeUnit.MILLISECONDS).build(),
                    stream,
                ),
                config = ProtocolClientConfig(
                    host = address.url.trimEnd('/'),
                    serializationStrategy = GoogleJavaLiteProtobufStrategy(),
                    networkProtocol = NetworkProtocol.CONNECT,
                    timeoutOracle = { if (it.streamType == StreamType.UNARY) deadline else null },
                ),
            ),
        )
        hub = client(COMMAND_DEADLINE)
        dispatcher = client(DISPATCH_DEADLINE)
    }

    /** Starts connecting (again, after stop). */
    public fun start() {
        if (job != null || !address.isSet) return
        job = scope.launch { run() }
    }

    public fun stop() {
        job?.cancel()
        job = null
    }

    private suspend fun run() {
        var wait = FIRST_RETRY
        while (true) {
            val why = try {
                subscribe { wait = FIRST_RETRY }
                "the hub closed the connection"
            } catch (e: CancellationException) {
                throw e
            } catch (e: ConnectException) {
                describe(e)
            } catch (e: Exception) {
                e.message ?: e.toString()
            }
            _state.update { it.copy(status = HubStatus.Lost(why)) }
            delay(wait)
            wait = (wait * 2).coerceAtMost(LONGEST_RETRY)
        }
    }

    /** Subscribes and applies snapshots until the stream ends. */
    private suspend fun subscribe(onSnapshot: () -> Unit) {
        val stream = hub.subscribe(headers)
        // Not native_notifications: the phone notifies as well as the Mac,
        // not instead of it, so the hub keeps posting its own there.
        stream.sendAndClose(subscribeRequest {}).getOrThrow()
        for (msg in stream.responseChannel()) {
            when (msg.msgCase) {
                SubscribeResponse.MsgCase.SNAPSHOT -> {
                    val s = msg.snapshot
                    _state.value = HubState(HubStatus.Connected, s, Attention.of(s))
                    onSnapshot()
                }
                SubscribeResponse.MsgCase.EVENT ->
                    if (msg.event.kind == EventKind.EVENT_KIND_ALERT) _alerts.tryEmit(msg.event.alert)
                else -> {}
            }
        }
    }

    /**
     * Asks the hub to refresh now: GitHub, or the agents, or the checkouts.
     * Returns why it failed, if it did.
     */
    public suspend fun refresh(what: RefreshTarget = RefreshTarget.REFRESH_TARGET_GITHUB): String? =
        why(hub.refresh(refreshRequest { target = what }, headers))

    /** Approves something waiting on you. Returns why it failed, if it did. */
    public suspend fun approve(a: Approval): String? = when (a) {
        is Approval.Proposal -> why(hub.approveProposal(approveProposalRequest { id = a.id }, headers))
        is Approval.Epic -> why(
            hub.approveEpic(
                approveEpicRequest {
                    ref = a.ref
                    planId = a.planId
                },
                headers,
            ),
        )
        is Approval.Gate -> why(
            hub.clearEpicGate(
                clearEpicGateRequest {
                    ref = a.ref
                    piece = a.piece
                },
                headers,
            ),
        )
        is Approval.Release -> why(
            hub.toolkitRollout(
                toolkitRolloutRequest {
                    tool = a.tool
                    version = a.version
                },
                headers,
            ),
        )
    }

    /**
     * Rejects a supervisor proposal, or declines a tool release. Returns why
     * it failed, if it did.
     */
    public suspend fun reject(a: Approval): String? = when (a) {
        is Approval.Proposal -> why(hub.rejectProposal(rejectProposalRequest { id = a.id }, headers))
        is Approval.Release -> why(
            hub.toolkitControl(
                toolkitControlRequest {
                    action = ToolkitAction.TOOLKIT_ACTION_DISMISS
                    id = a.id
                },
                headers,
            ),
        )
        else -> "only a proposal or a release can be turned down"
    }

    /** Starts rolling a tool out at version ("" for its latest release). */
    public suspend fun rollOut(tool: String, version: String = ""): Answer<Unit> =
        answer(
            hub.toolkitRollout(
                toolkitRolloutRequest {
                    this.tool = tool
                    this.version = version
                },
                headers,
            ),
        ) { }

    /** Cancels, retries or dismisses a rollout, or checks the tools now (id ""). */
    public suspend fun toolkit(action: ToolkitAction, id: String = ""): Answer<Unit> =
        answer(
            hub.toolkitControl(
                toolkitControlRequest {
                    this.action = action
                    this.id = id
                },
                headers,
            ),
        ) { }

    /** Opens the scaffold PR that brings a drifted repo back to the pins. */
    public suspend fun fixDrift(target: String): Answer<Unit> =
        answer(hub.toolkitFixDrift(toolkitFixDriftRequest { this.target = target }, headers)) { }

    /** Where an agent task could start on an item now, or why none can. */
    public suspend fun menuChoices(url: String): Answer<MenuChoicesResponse> =
        answer(hub.menuChoices(menuChoicesRequest { this.url = url }, headers)) { it }

    /**
     * Looks up an issue or PR you name (a GitHub URL or owner/repo#N), on
     * the dashboard or else on GitHub, and every task that can start on it.
     */
    public suspend fun itemChoices(ref: String): Answer<ItemChoicesResponse> =
        answer(hub.itemChoices(itemChoicesRequest { this.ref = ref }, headers)) { it }

    /** Starts an agent task on an item. */
    public suspend fun dispatch(task: String, url: String): Answer<DispatchResponse> =
        answer(
            dispatcher.dispatch(
                dispatchRequest {
                    this.task = task
                    this.url = url
                },
                headers,
            ),
        ) { it }

    /** The tasks that can join your queue for an item. */
    public suspend fun queueChoices(url: String): Answer<List<String>> =
        answer(hub.queueChoices(queueChoicesRequest { this.url = url }, headers)) { it.tasksList }

    /** Adds a task to your queue; the answer is its place (1 is first). */
    public suspend fun enqueue(task: String, url: String): Answer<Long> =
        answer(
            hub.enqueue(
                enqueueRequest {
                    this.task = task
                    this.url = url
                },
                headers,
            ),
        ) { it.position }

    /** Reads a finished run's outcome again. */
    public suspend fun reinspect(runId: String): Answer<Run> =
        answer(hub.reinspect(reinspectRequest { this.runId = runId }, headers)) { it.run }

    /** Takes a task off your queue. */
    public suspend fun unqueue(task: String, ref: String): Answer<Unit> =
        answer(
            hub.unqueue(
                unqueueRequest {
                    this.task = task
                    this.ref = ref
                },
                headers,
            ),
        ) { }

    /** Moves a queued task by delta places (negative is earlier); the answer is its new index. */
    public suspend fun moveQueued(task: String, ref: String, delta: Long): Answer<Long> =
        answer(
            hub.moveQueued(
                moveQueuedRequest {
                    this.task = task
                    this.ref = ref
                    this.delta = delta
                },
                headers,
            ),
        ) { it.index }

    /** Enrolls an epic (an issue URL or owner/repo#N); the answer is its ref. */
    public suspend fun enrollEpic(target: String): Answer<String> =
        answer(hub.enrollEpic(enrollEpicRequest { this.target = target }, headers)) { it.ref }

    /** Stops following an epic. */
    public suspend fun dropEpic(ref: String): Answer<Unit> =
        answer(hub.dropEpic(dropEpicRequest { this.ref = ref }, headers)) { }

    /** Reads an epic again. */
    public suspend fun rereadEpic(ref: String): Answer<Unit> =
        answer(hub.rereadEpic(rereadEpicRequest { this.ref = ref }, headers)) { }

    /** Posts a finished epic's summary, as you, and closes it: the plan seen finished. */
    public suspend fun finishEpic(ref: String, planID: String): Answer<Unit> =
        answer(hub.finishEpic(finishEpicRequest { this.ref = ref; this.planId = planID }, headers)) { }

    /** Drives the fleet rollout: pause, resume, apply now, skip the drain wait, retry. */
    public suspend fun fleet(action: FleetAction): Answer<Unit> =
        answer(hub.fleetControl(fleetControlRequest { this.action = action }, headers)) { }

    /**
     * Starts, or cancels, a Dewey reindex pass on a fleet machine, or on
     * every one (all).
     */
    public suspend fun deweyReindex(
        machine: String,
        all: Boolean = false,
        cancel: Boolean = false,
        force: Boolean = false,
    ): Answer<DeweyReindexResponse> =
        answer(
            hub.deweyReindex(
                deweyReindexRequest {
                    this.machine = machine
                    this.all = all
                    this.cancel = cancel
                    this.force = force
                },
                headers,
            ),
        ) { it }

    /**
     * Starts one action on one fleet checkout (machine "" is the hub's);
     * the answer is what started.
     */
    public suspend fun checkoutAction(machine: String, dir: String, action: CheckoutActionKind): Answer<String> =
        answer(
            hub.checkoutAction(
                checkoutActionRequest {
                    this.machine = machine
                    this.dir = dir
                    this.action = action
                },
                headers,
            ),
        ) { it.started }

    /** The end of a live agent's pane: the run's when runId is set, else the session's. */
    public suspend fun readPane(runId: String, sessionId: String, lines: Int = 200): Answer<String> =
        answer(
            hub.readPane(
                readPaneRequest {
                    this.runId = runId
                    this.sessionId = sessionId
                    this.lines = lines
                },
                headers,
            ),
        ) { it.text }

    /** A run's transcript: its last messages, oldest first. */
    public suspend fun transcript(runId: String, last: Int = 100): Answer<TranscriptResponse> =
        answer(
            hub.transcript(
                transcriptRequest {
                    this.runId = runId
                    this.last = last
                },
                headers,
            ),
        ) { it }

    private companion object {
        fun <T, R> answer(r: ResponseMessage<T>, value: (T) -> R): Answer<R> = when (r) {
            is ResponseMessage.Success -> Answer.Ok(value(r.message))
            is ResponseMessage.Failure -> Answer.No(describe(r.cause), unsure = cutOff(r.cause))
        }

        val COMMAND_DEADLINE: Duration = 30.seconds

        /** Over the hub's 2 minutes for starting an agent (dispatchTimeout). */
        val DISPATCH_DEADLINE: Duration = 150.seconds
        val FIRST_RETRY: Duration = 250.milliseconds
        val LONGEST_RETRY: Duration = 5.seconds

        fun <T> why(r: ResponseMessage<T>): String? = when (r) {
            is ResponseMessage.Success -> null
            is ResponseMessage.Failure -> describe(r.cause)
        }

        /** Ended by a deadline or a dropped call, not by the hub saying no. */
        fun cutOff(e: ConnectException): Boolean =
            e.code == Code.DEADLINE_EXCEEDED || e.code == Code.CANCELED || raw(e).equals("timeout", ignoreCase = true) ||
                raw(e).equals("canceled", ignoreCase = true)

        /**
         * Why a call failed, in words: the hub's own reason, or what became
         * of the call when the hub never answered (OkHttp says only
         * "timeout" or "Canceled").
         */
        fun describe(e: ConnectException): String = when {
            e.code == Code.DEADLINE_EXCEEDED || raw(e).equals("timeout", ignoreCase = true) ->
                "The hub didn't answer in time"
            e.code == Code.CANCELED || raw(e).equals("canceled", ignoreCase = true) ->
                "The connection to the hub dropped before it answered"
            else -> raw(e)
        }

        private fun raw(e: ConnectException): String =
            e.message?.takeIf { it.isNotBlank() } ?: e.exception?.message ?: e.code.toString()
    }
}
