package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.ItemKind
import io.github.jflowers.ghhud.api.v1.Run

// The commands on an item: start an agent, add one to your queue, re-check
// its last run. The hub does the work; these say how it went, in the Mac's
// words (macos/Sources/GHHud/ItemCommands.swift).

/**
 * An agent task you can pick for an item. suggested: the item's state
 * calls for it (only told apart for an item you named).
 */
public data class AgentChoice(val task: String, val target: String, val cmd: String = "", val suggested: Boolean = false) {
    val id: String get() = "$task|$target"
}

/** An issue or PR you named, as the hub found it. */
public data class NamedItem(val url: String, val ref: String, val title: String, val isPR: Boolean) {
    /** "PR owner/repo#N · title". */
    val line: String get() = (if (isPR) "PR " else "Issue ") + "$ref · $title"
}

public object ItemCommands {
    /** Why commands are off, if they are: no hub, or the hub says so. */
    public fun off(state: HubState): String? =
        if (state.status != HubStatus.Connected) "Not connected to the hub" else ItemList.commandsOff(state.snapshot)

    /** The tasks to offer: where one can start now, or what can be queued. */
    public suspend fun choices(hub: HubConnection, row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> {
        if (queue) {
            return when (val a = hub.queueChoices(row.id)) {
                is Answer.No -> Answer.No("Can't queue: ${a.why}")
                is Answer.Ok ->
                    if (a.value.isEmpty()) {
                        Answer.No("No agent tasks to queue for this item.")
                    } else {
                        Answer.Ok(a.value.map { AgentChoice(it, "your queue") })
                    }
            }
        }
        return when (val a = hub.menuChoices(row.id)) {
            is Answer.No -> Answer.No("The agent menu failed: ${a.why}")
            is Answer.Ok ->
                if (a.value.isEmpty()) {
                    Answer.No("Nothing can start on this item now: no agent task fits it, or no checkout of ${row.repo} is free.")
                } else {
                    Answer.Ok(a.value.map { AgentChoice(it.task, it.target, it.cmd) })
                }
        }
    }

    /** Starts or queues a choice; the answer is what to say. */
    public suspend fun run(hub: HubConnection, row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> {
        if (queue) {
            return when (val a = hub.enqueue(c.task, row.id)) {
                is Answer.No -> a
                is Answer.Ok -> Answer.Ok("Queued ${c.task} for ${row.ref}, number ${a.value} in your queue")
            }
        }
        return when (val a = hub.dispatch(c.task, row.id)) {
            is Answer.No -> a
            is Answer.Ok -> {
                val rec = a.value
                val at = if (rec.workspace.isEmpty()) "" else " in ${rec.machine.ifEmpty { "local" }}:${rec.workspace}"
                Answer.Ok("Started ${rec.task.ifEmpty { c.task }} on ${row.ref}$at")
            }
        }
    }

    /**
     * Asks the hub about an issue or PR you name (a GitHub URL or
     * owner/repo#N): what it is, and every task that can start on it.
     */
    public suspend fun lookUp(hub: HubConnection, text: String): Answer<Pair<NamedItem, List<AgentChoice>>> {
        val ref = text.trim()
        if (ref.isEmpty()) return Answer.No("Name an issue or PR")
        return when (val a = hub.itemChoices(ref)) {
            is Answer.No -> a
            is Answer.Ok -> {
                val it = a.value.item
                val named = NamedItem(it.url, "${it.repo}#${it.num}", it.title, it.kind == ItemKind.ITEM_KIND_PR)
                if (a.value.choicesCount == 0) {
                    Answer.No(
                        "Nothing can start on ${named.ref} now: no checkout of ${it.repo} is free, " +
                            "or a person handles it (human-gate).",
                    )
                } else {
                    Answer.Ok(named to a.value.choicesList.map { c -> AgentChoice(c.task, c.target, c.cmd, c.suggested) })
                }
            }
        }
    }

    /** Starts a choice on an item you named; the answer is what to say. */
    public suspend fun run(hub: HubConnection, item: NamedItem, c: AgentChoice): Answer<String> =
        when (val a = hub.dispatch(c.task, item.url)) {
            is Answer.No -> a
            is Answer.Ok -> {
                val rec = a.value
                val at = if (rec.workspace.isEmpty()) "" else " in ${rec.machine.ifEmpty { "local" }}:${rec.workspace}"
                Answer.Ok("Started ${rec.task.ifEmpty { c.task }} on ${item.ref}$at")
            }
        }

    /**
     * The first GitHub issue or PR link in shared text (a browser shares
     * "title https://…"), or null.
     */
    public fun sharedLink(text: String): String? = itemLink.find(text)?.value

    private val itemLink = Regex("""https?://(www\.)?github\.com/[\w.-]+/[\w.-]+/(pull|issues)/[1-9]\d*\S*""")

    /** Reads the item's last run's outcome again; the answer is what to say. */
    public suspend fun recheck(hub: HubConnection, row: ItemRow): Answer<String> {
        val run = row.latestRun ?: return Answer.No("No run to re-check")
        return when (val a = hub.reinspect(run.id)) {
            is Answer.No -> Answer.No("Can't re-check ${run.task} on ${row.ref}: ${a.why}")
            is Answer.Ok -> Answer.Ok("Re-checked ${run.task} on ${row.ref}: ${outcome(a.value)}")
        }
    }

    /** A run's outcome in words, with its summary. */
    public fun outcome(r: Run): String {
        val words = RunTag.words(r.status)
        return if (r.summary.isEmpty()) words else "$words — ${r.summary}"
    }
}
