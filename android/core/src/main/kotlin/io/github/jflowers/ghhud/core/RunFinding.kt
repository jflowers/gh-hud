package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Run

/**
 * What a ci run found (#180), in the TUI's words: the finding, the
 * supervisor still reading the run, or why there is none (RunFinding in
 * macos/Sources/GHHudKit/RunFinding.swift).
 */
public object RunFinding {
    /** The run's Finding section, or null when there's nothing to say; WARN while a finding holds the PR. */
    public fun text(r: Run): Pair<String, Tone>? {
        if (r.hasFinding()) {
            val f = r.finding
            var s = when (f.kind) {
                "fixed_by" -> "fixed by #${f.pr}"
                "blocked_by_base" -> "blocked by the base branch: ${f.check} fails there"
                else -> f.kind
            }
            s += " (${f.by})"
            if (f.why.isNotEmpty()) s += ": ${f.why}"
            return s to Tone.WARN
        }
        if (r.findingPending) return "the supervisor is reading the run" to Tone.PENDING
        if (r.findingNote.isNotEmpty()) return r.findingNote to Tone.MUTED
        return null
    }
}
