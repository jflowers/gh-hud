package io.github.jflowers.ghhud

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import androidx.core.net.toUri
import io.github.jflowers.ghhud.api.v1.Alert
import io.github.jflowers.ghhud.api.v1.AlertType
import io.github.jflowers.ghhud.core.ApprovalWatch
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import io.github.jflowers.ghhud.core.Notices
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.launch

/**
 * Keeps the subscription to the hub while the app is closed, and
 * notifies you of the hub's alerts and of approvals that start waiting on
 * you. Android shows it as an ongoing notification, which says whether the
 * hub is there. It holds one idle connection over the tailnet.
 */
class HubService : Service() {
    private val scope = MainScope()
    private val manager by lazy { NotificationManagerCompat.from(this) }
    private var nextAlert = ALERT_IDS

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        channels()
        ServiceCompat.startForeground(
            this,
            ONGOING,
            ongoing("Connecting…"),
            if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0,
        )
        hub.hold(HOLDER)
        scope.launch { watch() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (!hub.notify.value || !hub.connection.value.address.isSet) stopSelf()
        return START_STICKY
    }

    override fun onDestroy() {
        scope.cancel()
        hub.release(HOLDER)
        super.onDestroy()
    }

    /** Follows the connection (a new one when the address changes). */
    private suspend fun watch(): Unit = hub.connection.collectLatest { c ->
        coroutineScope {
            // A tool release also waits as an approval, whose notification
            // goes when it's answered: skip the alert's copy.
            launch { c.alerts.filter { it.type != AlertType.ALERT_TYPE_TOOL_RELEASE }.collect { post(it) } }
            val approvals = ApprovalWatch()
            c.state.collect { st ->
                show(st)
                if (st.status == HubStatus.Connected) {
                    val change = approvals.update(st.attention)
                    change.added.forEach { post(it) }
                    change.gone.forEach { manager.cancel(it, APPROVAL) }
                }
            }
        }
    }

    private fun show(st: HubState) {
        val text = when (val s = st.status) {
            HubStatus.Connected -> st.attention.count.let { if (it == 0) "Nothing needs you" else "$it need you" }
            HubStatus.Connecting, HubStatus.NotSet -> "Connecting…"
            is HubStatus.Lost -> "Lost the hub: ${s.why}. Retrying…"
        }
        notify(null, ONGOING, ongoing(text))
    }

    private fun post(a: Alert) {
        val link = Notices.link(a)
        val n = NotificationCompat.Builder(this, ALERTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(Notices.title(a.type))
            .setContentText(a.text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(a.text))
            .setContentIntent(if (link != null) view(link) else openApp())
            .setAutoCancel(true)
            .build()
        notify(null, nextAlert++, n)
    }

    private fun post(row: AttentionRow) {
        val a = row.approval ?: return
        val n = NotificationCompat.Builder(this, APPROVALS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(Notices.title(a))
            .setContentText(row.title)
            .setSubText(row.detail)
            .setContentIntent(openApp())
            .setAutoCancel(true)
            .build()
        notify(row.id, APPROVAL, n)
    }

    private fun notify(tag: String?, id: Int, n: Notification) {
        // Without the permission Android drops them; the screen asks for it.
        if (manager.areNotificationsEnabled()) {
            try {
                manager.notify(tag, id, n)
            } catch (_: SecurityException) {
            }
        }
    }

    private fun ongoing(text: String): Notification =
        NotificationCompat.Builder(this, CONNECTION)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("gh-hud")
            .setContentText(text)
            .setContentIntent(openApp())
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .build()

    private fun openApp(): PendingIntent = PendingIntent.getActivity(
        this,
        0,
        Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun view(url: String): PendingIntent = PendingIntent.getActivity(
        this,
        url.hashCode(),
        Intent(Intent.ACTION_VIEW, url.toUri()),
        PendingIntent.FLAG_IMMUTABLE,
    )

    private fun channels() {
        val m = getSystemService(NotificationManager::class.java)
        m.createNotificationChannel(
            NotificationChannel(CONNECTION, "Connection to the hub", NotificationManager.IMPORTANCE_MIN).apply {
                description = "Shown while gh-hud stays connected to notify you"
            },
        )
        m.createNotificationChannel(
            NotificationChannel(APPROVALS, "Waiting for your approval", NotificationManager.IMPORTANCE_HIGH).apply {
                description = "Proposals, epic plans and gates that wait on you"
            },
        )
        m.createNotificationChannel(
            NotificationChannel(ALERTS, "Hub alerts", NotificationManager.IMPORTANCE_HIGH).apply {
                description = "Agents waiting on you, PRs ready for review, finished runs"
            },
        )
    }

    companion object {
        private const val HOLDER = "service"
        private const val CONNECTION = "connection"
        private const val APPROVALS = "approvals"
        private const val ALERTS = "alerts"
        private const val ONGOING = 1
        private const val APPROVAL = 2
        private const val ALERT_IDS = 1000

        fun intent(context: Context) = Intent(context, HubService::class.java)
    }
}
