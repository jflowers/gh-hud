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

    /// Whether text (say, the clipboard) is one GitHub issue or PR link,
    /// for Start Agent on URL to fill in. The hub reads it properly.
    public static func isItemLink(_ s: String) -> Bool {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.contains(where: \.isWhitespace), let u = URL(string: t.contains("://") ? t : "https://" + t),
              let host = u.host?.lowercased(), host == "github.com" || host == "www.github.com"
        else { return false }
        let p = u.pathComponents.filter { $0 != "/" }
        return p.count >= 4 && (p[2] == "pull" || p[2] == "issues") && Int(p[3]).map { $0 > 0 } == true
    }
}
