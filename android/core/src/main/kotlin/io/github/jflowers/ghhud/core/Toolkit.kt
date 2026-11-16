package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.api.v1.ToolkitRolloutState
import io.github.jflowers.ghhud.api.v1.ToolkitTarget
import io.github.jflowers.ghhud.api.v1.ToolkitTool

// The Toolkit screen, worked out from a hub snapshot: the TUI's T view of
// the tools the fleet pins and their rollouts. A port of the Mac's
// Toolkit.swift.

/** One tool the fleet pins. */
public data class ToolRow(
    val name: String,
    /** On the dotfiles branch ("—" when it has none). */
    val pin: String,
    /** The newest release ("—" when unknown). */
    val latest: String,
    /**
     * "◆" when a release waits for your go-ahead, "↑" when one is out that
     * nobody proposed (declined, say), "" when the pin is the latest.
     */
    val mark: String,
    val tone: Tone,
    /** The rollout under way for it, if any. */
    val rolling: String?,
    /** The proposed rollout waiting on you, if any. */
    val proposal: String?,
    /** Whether the latest release can be rolled out now. */
    val canRollOut: Boolean,
    /** "4/6 repos on 2.4.0": targets whose scaffold matches the pin. */
    val repos: String,
    val error: String,
) {
    public companion object {
        public fun of(t: ToolkitTool, rollouts: List<ToolkitRolloutState>): ToolRow {
            val mine = rollouts.filter { it.tool == t.name }
            val proposal = mine.firstOrNull { it.stage == "proposed" }?.id
            val rolling = mine.firstOrNull { ToolkitList.active(it.stage) }?.id
            val newer = t.latest.isNotEmpty() && t.pin.isNotEmpty() && t.latest != t.pin
            val repos = if (t.reposList.isEmpty() || t.pin.isEmpty()) "" else
                "${t.reposList.count { it.version == t.pin }}/${t.reposCount} repos on ${t.pin}"
            return ToolRow(
                name = t.name,
                pin = t.pin.ifEmpty { "—" },
                latest = t.latest.ifEmpty { "—" },
                mark = if (proposal != null) "◆" else if (newer) "↑" else "",
                tone = if (t.error.isNotEmpty()) Tone.BAD else if (proposal != null || newer) Tone.WARN else Tone.GOOD,
                rolling = rolling,
                proposal = proposal,
                canRollOut = rolling == null && newer,
                repos = repos,
                error = t.error,
            )
        }
    }
}

/** One tool release on its way out (or done, failed, or cancelled). */
public data class RolloutRow(
    val id: String,
    val tool: String,
    val version: String,
    val stage: String,
    val tone: Tone,
    val landed: Int,
    val open: Int,
    val held: Int,
    val refused: Int,
    val failed: Int,
    /** "3/8 landed · 2 open · 1 held": the counts that aren't zero. */
    val counts: String,
    /** The hub's one-line progress. */
    val summary: String,
    val error: String,
    val pinPr: String?,
    val canCancel: Boolean,
    val canRetry: Boolean,
    val canDismiss: Boolean,
    val targets: List<TargetRow>,
) {
    public companion object {
        public fun of(r: ToolkitRolloutState): RolloutRow {
            val targets = r.targetsList.map { TargetRow.of(it) }
            val landed = targets.count { it.landed }
            val open = targets.count { it.state == "open" }
            val held = targets.count { it.state == "held" }
            val refused = targets.count { it.state == "refused" }
            val failed = targets.count { it.state == "failed" }
            val parts = mutableListOf<String>()
            if (targets.isNotEmpty()) parts += "$landed/${targets.size} landed"
            for ((n, word) in listOf(open to "open", held to "held", refused to "refused", failed to "failed")) {
                if (n > 0) parts += "$n $word"
            }
            val active = ToolkitList.active(r.stage)
            return RolloutRow(
                id = r.id,
                tool = r.tool,
                version = r.version,
                stage = r.stage,
                tone = ToolkitList.stageTone(r.stage),
                landed = landed,
                open = open,
                held = held,
                refused = refused,
                failed = failed,
                counts = parts.joinToString(" · "),
                summary = r.summary,
                error = r.error,
                pinPr = r.pinPr.ifEmpty { null },
                canCancel = active,
                canRetry = r.stage == "failed" || r.stage == "cancelled",
                canDismiss = !active && r.stage != "proposed",
                targets = targets,
            )
        }
    }
}

/** One repo in a rollout. */
public data class TargetRow(
    val target: String,
    /**
     * held, refused, failed, CI failing, merged, closed, pushed, up to
     * date, open, or the hub's status.
     */
    val state: String,
    val tone: Tone,
    val landed: Boolean,
    /** The PR when there is one. */
    val url: String?,
    /** Where it is without a PR (a held branch) or why it stopped. */
    val note: String,
) {
    public companion object {
        public fun of(t: ToolkitTarget): TargetRow {
            val url = t.url.ifEmpty { null }
            val state = when {
                t.needsYou.isNotEmpty() -> t.needsYou
                t.prState == "MERGED" -> "merged"
                t.prState == "CLOSED" -> "closed"
                t.status == "pushed" || t.status == "up to date" -> t.status
                url != null -> "open"
                else -> t.status
            }
            return TargetRow(
                target = t.target,
                state = state,
                tone = when (state) {
                    "merged", "pushed", "up to date" -> Tone.GOOD
                    "held" -> Tone.WARN
                    "refused", "failed", "CI failing" -> Tone.BAD
                    "open" -> Tone.PENDING
                    else -> Tone.MUTED
                },
                landed = t.prState == "MERGED" || t.status == "pushed" || t.status == "up to date",
                url = url,
                note = if (url == null && t.branch.isNotEmpty()) t.branch else t.detail,
            )
        }
    }
}

public object ToolkitList {
    public fun tools(s: Snapshot): List<ToolRow> =
        if (!s.hasToolkit()) emptyList() else s.toolkit.toolsList.map { ToolRow.of(it, s.toolkit.rolloutsList) }

    /** Rollouts newest first, proposals left out: they're the tools' ◆. */
    public fun rollouts(s: Snapshot): List<RolloutRow> =
        s.toolkit.rolloutsList.filter { it.stage != "proposed" }.map { RolloutRow.of(it) }

    /** The proposal waiting for tool, for the release notes and Approve. */
    public fun proposal(s: Snapshot, tool: String): ToolkitRolloutState? =
        s.toolkit.rolloutsList.firstOrNull { it.tool == tool && it.stage == "proposed" }

    /** The badge: releases to approve and repos waiting on you. */
    public fun waiting(s: Snapshot): Int = Attention.releases(s).size + Attention.rollouts(s).size

    /** The toolkit in a few words. */
    public fun state(s: Snapshot): String {
        val t = s.toolkit
        if (t.error.isNotEmpty()) return t.error
        val parts = mutableListOf<String>()
        if (t.checking) {
            parts += "checking…"
        } else if (t.hasCheckedAt()) {
            parts += "checked ${HubSummary.ago(t.checkedAt.instant, s.fetchedAt.instant)}"
        }
        val rolling = t.rolloutsList.count { active(it.stage) }
        if (rolling > 0) parts += "$rolling rolling out"
        return parts.joinToString(" · ")
    }

    public fun active(stage: String): Boolean = stage == "pin" || stage == "scaffold" || stage == "track"

    public fun stageTone(stage: String): Tone = when (stage) {
        "done" -> Tone.GOOD
        "failed" -> Tone.BAD
        "proposed" -> Tone.WARN
        "cancelled" -> Tone.MUTED
        else -> Tone.PENDING
    }
}

/** The Toolkit screen's commands, said as the snackbar says them. */
public object ToolkitCommands {
    /** Why the controls are off: no hub, or no toolkit. */
    public fun off(state: HubState): String? = when {
        state.status != HubStatus.Connected -> "Not connected to the hub"
        !state.snapshot.hasToolkit() -> "The toolkit is off on the hub"
        else -> null
    }

    public suspend fun check(hub: HubConnection): Answer<String> =
        said(hub.toolkit(ToolkitAction.TOOLKIT_ACTION_CHECK), "Checking the tools' releases")

    public suspend fun rollOut(hub: HubConnection, tool: String, version: String): Answer<String> =
        said(hub.rollOut(tool, version), "Rolling $tool $version out")

    /** Declines a waiting release: it isn't proposed again. */
    public suspend fun decline(hub: HubConnection, id: String): Answer<String> =
        said(hub.toolkit(ToolkitAction.TOOLKIT_ACTION_DISMISS, id), "Declined $id")

    public suspend fun control(hub: HubConnection, action: ToolkitAction, r: RolloutRow): Answer<String> {
        val said = when (action) {
            ToolkitAction.TOOLKIT_ACTION_CANCEL -> "Cancelled ${r.id}"
            ToolkitAction.TOOLKIT_ACTION_RETRY -> "Retrying ${r.id}"
            ToolkitAction.TOOLKIT_ACTION_DISMISS -> "Dismissed ${r.id}"
            else -> return Answer.No("Toolkit: no such control")
        }
        return said(hub.toolkit(action, r.id), said)
    }

    private fun said(a: Answer<Unit>, text: String): Answer<String> = when (a) {
        is Answer.Ok -> Answer.Ok(text)
        is Answer.No -> Answer.No("Toolkit: ${a.why}")
    }
}
