package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Alert
import io.github.jflowers.ghhud.api.v1.AlertType

// What the phone notifies you of: the hub's alerts, as the Mac app shows
// them (macos/Sources/GHHud/Notifier.swift), and approvals that start
// waiting on you, which only the phone can act on away from the desk.

public object Notices {
    /** A notification's title for a hub alert. */
    public fun title(t: AlertType): String = when (t) {
        AlertType.ALERT_TYPE_READY -> "Ready for review"
        AlertType.ALERT_TYPE_BLOCKED -> "An agent is waiting on you"
        AlertType.ALERT_TYPE_RUN_FINISHED -> "Agent run finished"
        AlertType.ALERT_TYPE_TOOL_RELEASE -> "A tool released"
        AlertType.ALERT_TYPE_TOOLKIT_TARGET -> "A tool rollout needs you"
        else -> "gh-hud"
    }

    /** The PR or issue an alert is about, if it names one. */
    public fun link(a: Alert): String? =
        Links.forRef(a.ref) ?: Links.forRef(a.run.ref) ?: Links.forRef(a.session.ref)

    /** A notification's title for an approval that waits on you. */
    public fun title(a: Approval): String = when (a) {
        is Approval.Proposal -> "A proposal awaits your approval"
        is Approval.Epic -> "An epic plan awaits your approval"
        is Approval.Gate -> "An epic gate only you can clear"
        is Approval.Release -> "A tool release awaits your go-ahead"
    }
}

/**
 * Which approvals to announce, snapshot by snapshot: those that weren't
 * waiting in the last one. What waits when it first looks was waiting
 * before we connected, so it isn't news. The ones that stop waiting are
 * reported too, so their notifications can go.
 */
public class ApprovalWatch {
    private var seen: Map<String, AttentionRow>? = null

    public data class Change(val added: List<AttentionRow>, val gone: Set<String>)

    public fun update(a: Attention): Change {
        val now = a.sections.flatMap { it.rows }.filter { it.approval != null }.associateBy { it.id }
        val before = seen
        seen = now
        if (before == null) return Change(emptyList(), emptySet())
        return Change(now.values.filter { it.id !in before }, before.keys - now.keys)
    }
}
