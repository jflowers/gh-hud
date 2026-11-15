package io.github.jflowers.ghhud.core

import com.connectrpc.ConnectException
import com.connectrpc.ProtocolClientConfig
import com.connectrpc.ResponseMessage
import com.connectrpc.extensions.GoogleJavaLiteProtobufStrategy
import com.connectrpc.impl.ProtocolClient
import com.connectrpc.okhttp.ConnectOkHttpClient
import com.connectrpc.protocols.NetworkProtocol
import io.github.jflowers.ghhud.api.v1.HubServiceClient
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.SubscribeResponse
import io.github.jflowers.ghhud.api.v1.approveEpicRequest
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
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
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

    private val hub: HubServiceClient
    private val headers: Map<String, List<String>> =
        if (address.token.isEmpty()) emptyMap() else mapOf("Authorization" to listOf("Bearer ${address.token}"))
    private var job: Job? = null

    init {
        // Commands get a deadline; the subscription none, since an idle hub
        // sends nothing for minutes.
        val unary = base.newBuilder().callTimeout(30, TimeUnit.SECONDS).build()
        val stream = base.newBuilder().readTimeout(0, TimeUnit.SECONDS).build()
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
        stream.sendAndClose(subscribeRequest {}).getOrThrow()
        for (msg in stream.responseChannel()) {
            if (msg.msgCase == SubscribeResponse.MsgCase.SNAPSHOT) {
                val s = msg.snapshot
                _state.value = HubState(HubStatus.Connected, s, Attention.of(s))
                onSnapshot()
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

    private companion object {
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
