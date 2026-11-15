package io.github.jflowers.ghhud

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.os.SystemClock
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.jflowers.ghhud.api.v1.AlertType
import io.github.jflowers.ghhud.api.v1.EventKind
import io.github.jflowers.ghhud.api.v1.SubscribeResponse
import io.github.jflowers.ghhud.api.v1.alert
import io.github.jflowers.ghhud.api.v1.case
import io.github.jflowers.ghhud.api.v1.decision
import io.github.jflowers.ghhud.api.v1.event
import io.github.jflowers.ghhud.api.v1.proposal
import io.github.jflowers.ghhud.api.v1.snapshot
import io.github.jflowers.ghhud.api.v1.subscribeResponse
import io.github.jflowers.ghhud.core.HubAddress
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okio.Buffer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.TimeUnit

/**
 * Runs on a device: the service against a stand-in hub. A proposal that
 * starts waiting and a hub alert each become a notification.
 */
@RunWith(AndroidJUnit4::class)
class HubServiceTest {
    private val instr = InstrumentationRegistry.getInstrumentation()
    private val context = instr.targetContext
    private val server = MockWebServer()
    private val manager = context.getSystemService(NotificationManager::class.java)

    @Before
    fun setUp() {
        instr.uiAutomation.grantRuntimePermission(context.packageName, Manifest.permission.POST_NOTIFICATIONS)
        server.start()
    }

    @After
    fun tearDown() {
        instr.runOnMainSync { context.hub.setNotify(false) }
        server.close()
    }

    private fun body(vararg msgs: SubscribeResponse): Buffer {
        val b = Buffer()
        for (m in msgs) {
            val bytes = m.toByteArray()
            b.writeByte(0).writeInt(bytes.size).write(bytes)
        }
        return b
    }

    /** The messages, then (after a pause, so each is seen) the end. */
    private fun stream(vararg msgs: SubscribeResponse, holdFor: Long): MockResponse {
        val b = body(*msgs)
        val size = b.size
        val end = "{}".toByteArray()
        b.writeByte(2).writeInt(end.size).write(end)
        return MockResponse.Builder()
            .setHeader("Content-Type", "application/connect+proto")
            .body(b)
            .throttleBody(size, holdFor, TimeUnit.MILLISECONDS)
            .build()
    }

    private fun waitFor(what: String, find: (Array<android.service.notification.StatusBarNotification>) -> Notification?): Notification {
        val until = SystemClock.uptimeMillis() + 15_000
        while (SystemClock.uptimeMillis() < until) {
            find(manager.activeNotifications)?.let { return it }
            SystemClock.sleep(200)
        }
        throw AssertionError("no notification: $what")
    }

    @Test
    fun notifiesOfNewApprovalsAndAlerts() {
        val waiting = snapshot {
            cases += case {
                repo = "o/r"
                proposal = proposal {
                    id = "p1"
                    status = "pending"
                    decision = decision { kind = "fix_pr"; summary = "Retry the flaky build" }
                }
            }
        }
        val blocked = event {
            kind = EventKind.EVENT_KIND_ALERT
            alert = alert { type = AlertType.ALERT_TYPE_BLOCKED; ref = "o/r#2"; text = "fix-ci has waited 10m" }
        }
        // First nothing waits; then, on the next connection, a proposal and an alert.
        server.enqueue(stream(subscribeResponse { snapshot = snapshot {} }, holdFor = 1500))
        server.enqueue(stream(subscribeResponse { event = blocked }, subscribeResponse { snapshot = waiting }, holdFor = 60_000))

        val screen = ActivityScenario.launch(MainActivity::class.java)
        instr.runOnMainSync {
            context.hub.setAddress(HubAddress(server.url("/").toString()))
            context.hub.setNotify(true)
        }

        val approval = waitFor("the proposal") { all -> all.firstOrNull { it.tag == "proposal:p1" }?.notification }
        assertEquals("A proposal awaits your approval", approval.extras.getString(Notification.EXTRA_TITLE))
        assertEquals("Retry the flaky build", approval.extras.getCharSequence(Notification.EXTRA_TEXT).toString())

        val alert = waitFor("the alert") { all ->
            all.map { it.notification }.firstOrNull { it.channelId == "alerts" }
        }
        assertEquals("An agent is waiting on you", alert.extras.getString(Notification.EXTRA_TITLE))
        assertNotNull(alert.contentIntent)
        screen.close()
    }
}
