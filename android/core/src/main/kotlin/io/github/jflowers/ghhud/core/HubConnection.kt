package io.github.jflowers.ghhud.core

import com.connectrpc.ConnectException
import com.connectrpc.ProtocolClientConfig
import com.connectrpc.ResponseMessage
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
import io.github.jflowers.ghhud.api.v1.MenuChoice
import io.github.jflowers.ghhud.api.v1.Run
import io.github.jflowers.ghhud.api.v1.approveEpicRequest
import io.github.jflowers.ghhud.api.v1.dispatchRequest
import io.github.jflowers.ghhud.api.v1.dropEpicRequest
import io.github.jflowers.ghhud.api.v1.enrollEpicRequest
import io.github.jflowers.ghhud.api.v1.moveQueuedRequest
import io.github.jflowers.ghhud.api.v1.rereadEpicRequest
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

    public data class No(val why: String) : Answer<Nothing>
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
    private val headers: Map<String, List<String>> =
        if (address.token.isEmpty()) emptyMap() else mapOf("Authorization" to listOf("Bearer ${address.token}"))
    private var job: Job? = null

    init {
        // Commands get a deadline; the subscription none, since an idle hub
        // sends nothing for minutes. Over HTTP/2 (the tailnet's HTTPS) pings
        // find a connection that died quietly, so it reconnects.
        val unary = base.newBuilder().callTimeout(30, TimeUnit.SECONDS).build()
        val stream = base.newBuilder().readTimeout(0, TimeUnit.SECONDS).pingInterval(60, TimeUnit.SECONDS).build()
        hub = HubServiceClient(
            ProtocolClient(
                httpClient = ConnectOkHttpClient(unary, stream),
                config = ProtocolClientConfig(
                    host = address.url.trimEnd('/'),
                    serializationStrategy = GoogleJavaLiteProtobufStrategy(),
                    networkProtocol = NetworkProtocol.CONNECT,
                ),
            ),
        )
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

    /** Asks the hub to refresh GitHub now. Returns why it failed, if it did. */
    public suspend fun refresh(): String? =
        why(hub.refresh(refreshRequest { target = RefreshTarget.REFRESH_TARGET_GITHUB }, headers))

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
    }

    /** Rejects a supervisor proposal. Returns why it failed, if it did. */
    public suspend fun reject(a: Approval): String? {
        if (a !is Approval.Proposal) return "only a proposal can be rejected"
        return why(hub.rejectProposal(rejectProposalRequest { id = a.id }, headers))
    }

    /** Where an agent task could start on an item now. */
    public suspend fun menuChoices(url: String): Answer<List<MenuChoice>> =
        answer(hub.menuChoices(menuChoicesRequest { this.url = url }, headers)) { it.choicesList }

    /** Starts an agent task on an item. */
    public suspend fun dispatch(task: String, url: String): Answer<DispatchResponse> =
        answer(
            hub.dispatch(
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

    private companion object {
        fun <T, R> answer(r: ResponseMessage<T>, value: (T) -> R): Answer<R> = when (r) {
            is ResponseMessage.Success -> Answer.Ok(value(r.message))
            is ResponseMessage.Failure -> Answer.No(describe(r.cause))
        }

        val FIRST_RETRY: Duration = 250.milliseconds
        val LONGEST_RETRY: Duration = 5.seconds

        fun <T> why(r: ResponseMessage<T>): String? = when (r) {
            is ResponseMessage.Success -> null
            is ResponseMessage.Failure -> describe(r.cause)
        }

        fun describe(e: ConnectException): String =
            e.message?.takeIf { it.isNotBlank() } ?: e.exception?.message ?: e.code.toString()
    }
}
