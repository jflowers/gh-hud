import Foundation

/// What a ci run found (#180), in the TUI's words: the finding, the
/// supervisor still reading the run, or why there is none.
public enum RunFinding {
    /// The run view's Finding section, or nil when there's nothing to say;
    /// the tone is warn while a finding holds the PR.
    public static func text(_ r: Ghhud_V1_Run) -> (String, Tone)? {
        if r.hasFinding {
            let f = r.finding
            var s: String
            switch f.kind {
            case "fixed_by": s = "fixed by #\(f.pr)"
            case "blocked_by_base": s = "blocked by the base branch: \(f.check) fails there"
            default: s = f.kind
            }
            s += " (\(f.by))"
            if !f.why.isEmpty { s += ": \(f.why)" }
            return (s, .warn)
        }
        if r.findingPending { return ("the supervisor is reading the run", .pending) }
        if !r.findingNote.isEmpty { return (r.findingNote, .muted) }
        return nil
    }
}
