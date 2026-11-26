package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.CheckoutActionKind
import io.github.jflowers.ghhud.api.v1.CheckoutHealth
import java.time.Instant

/**
 * What a checkout's screen can start on it (#172), and whether it can now:
 * the hub's own checkout actions, as the Mac's Workspaces menu offers them.
 */
public object WorkspaceActions {
    public data class Action(
        val kind: CheckoutActionKind,
        val title: String,
        val enabled: Boolean,
        /** Pushes, opens a PR or throws work away: the screen asks first. */
        val confirm: Boolean,
    ) {
        /** Throws work away: the confirmation's button says so. */
        val destructive: Boolean get() = kind == CheckoutActionKind.CHECKOUT_ACTION_KIND_DISCARD
    }

    /**
     * The actions for a checkout, in menu order. A busy checkout (an agent,
     * a reindex or an upkeep in it) offers them all disabled; a plain ghws
     * dir only fast-forwards and keeps up.
     */
    public fun actions(h: CheckoutHealth): List<Action> {
        if (h.unmanaged) return emptyList()
        val idle = h.busy.isEmpty() && !h.reindexing && !h.upkeeping && h.error.isEmpty()
        val onDefault = h.defaultBranch.isEmpty() || h.branch == h.defaultBranch
        val work = h.dirtyCount > 0 || WorkspaceRow.localOnlyCommits(h) > 0 || h.ahead > 0
        val vault = h.index.isNotEmpty() && h.index != DeweyIndex.NONE
        val branch = h.defaultBranch.ifEmpty { "default branch" }
        return buildList {
            add(
                Action(
                    CheckoutActionKind.CHECKOUT_ACTION_KIND_PULL,
                    "Fast-forward $branch" + if (h.behind > 0) " (${h.behind} behind)" else "",
                    enabled = idle && !h.noUpstream && !(h.ahead > 0 && h.behind > 0),
                    confirm = false,
                ),
            )
            if (!h.upkeepOnly) {
                add(
                    Action(
                        CheckoutActionKind.CHECKOUT_ACTION_KIND_LEARNINGS,
                        "Open learnings PR" + if (h.learnings > 0) " (${h.learnings})" else "",
                        enabled = idle && h.learnings > 0 && h.dirtyCount == 0 && onDefault,
                        confirm = true,
                    ),
                )
                add(Action(CheckoutActionKind.CHECKOUT_ACTION_KIND_SAVE, "Save work to a draft PR…", idle && work && !h.noUpstream, true))
                add(Action(CheckoutActionKind.CHECKOUT_ACTION_KIND_DISCARD, "Discard uncommitted changes…", idle && h.dirtyCount > 0, true))
            }
            add(Action(CheckoutActionKind.CHECKOUT_ACTION_KIND_UPKEEP, "Keep up now", idle, false))
            if (!h.upkeepOnly) add(Action(CheckoutActionKind.CHECKOUT_ACTION_KIND_REINDEX, "Reindex Dewey", idle && vault, false))
        }
    }

    /** What the confirmation says an action will do; "" when it doesn't ask. */
    public fun confirmation(kind: CheckoutActionKind, h: CheckoutHealth): String {
        val where = "${h.workspace}/${h.repo.split("/").last()}"
        return when (kind) {
            CheckoutActionKind.CHECKOUT_ACTION_KIND_SAVE ->
                "Commits everything in $where (but untracked learnings) on a wip branch, pushes it to your fork, " +
                    "opens a draft PR from it, and puts the checkout back on ${h.defaultBranch}."
            CheckoutActionKind.CHECKOUT_ACTION_KIND_DISCARD ->
                "Throws away the ${h.dirtyCount} uncommitted change(s) in $where: edits are reset and untracked " +
                    "files deleted. Commits and untracked learnings stay. This can't be undone."
            CheckoutActionKind.CHECKOUT_ACTION_KIND_LEARNINGS ->
                "Commits the ${h.learnings} untracked learning(s) on the day's learnings branch, pushes it to " +
                    "your fork and opens the PR."
            else -> ""
        }
    }

    /**
     * The maintenance running in the checkout, as the hub's Workspaces list
     * names it: the action asked for by hand ("saving work to a draft PR"),
     * else the sweep's own "reindexing" or "upkeep"; "" when none runs.
     */
    public fun running(h: CheckoutHealth): String {
        if (!h.reindexing && !h.upkeeping) return ""
        return when (h.action) {
            CheckoutActionKind.CHECKOUT_ACTION_KIND_PULL -> "fast-forwarding"
            CheckoutActionKind.CHECKOUT_ACTION_KIND_LEARNINGS -> "opening learnings PRs"
            CheckoutActionKind.CHECKOUT_ACTION_KIND_SAVE -> "saving work to a draft PR"
            CheckoutActionKind.CHECKOUT_ACTION_KIND_UPKEEP -> "keeping up (asked for)"
            CheckoutActionKind.CHECKOUT_ACTION_KIND_REINDEX -> "reindexing (asked for)"
            CheckoutActionKind.CHECKOUT_ACTION_KIND_DISCARD -> "discarding changes"
            else -> if (h.reindexing) "reindexing" else "upkeep"
        }
    }

    /** running with how long it has run: "saving work to a draft PR · 2m". */
    public fun runningText(h: CheckoutHealth, now: Instant = Instant.now()): String {
        val s = running(h)
        return if (h.hasActionStarted()) s + " · " + ItemList.age(h.actionStarted.instant, now) else s
    }
}

/** Starting a checkout action from its screen. */
public object WorkspaceCommands {
    /** Asks the hub to start the action; the answer is what to say. */
    public suspend fun start(hub: HubConnection, h: CheckoutHealth, kind: CheckoutActionKind): Answer<String> {
        val where = if (h.machine.isEmpty()) h.workspace else "${h.machine}:${h.workspace}"
        return when (val a = hub.checkoutAction(h.machine, h.dir, kind)) {
            is Answer.No -> Answer.No("$where: ${a.why}")
            is Answer.Ok -> Answer.Ok(a.value.replaceFirstChar { it.uppercase() } + ". The outcome shows on the checkout.")
        }
    }
}
