import Foundation

public enum Links {
    /// A GitHub link for "owner/repo#N". /issues/N redirects to the PR
    /// when N is one, so this works for both.
    public static func forRef(_ ref: String) -> URL? {
        let parts = ref.split(separator: "#", maxSplits: 1)
        guard parts.count == 2, parts[0].contains("/"), let n = Int(parts[1]), n > 0 else {
            return nil
        }
        return URL(string: "https://github.com/\(parts[0])/issues/\(n)")
    }

    /// Where clicking an alert's notification goes.
    public static func forAlert(_ a: Ghhud_V1_Alert) -> URL? {
        forRef(a.ref) ?? forRef(a.run.ref) ?? forRef(a.session.ref)
    }
}
