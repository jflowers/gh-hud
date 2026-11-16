import Foundation

/// Where the hub listens and with what token, from hub.json in the hub's
/// state directory. The hub makes a new token each time it starts, so a
/// client reads this again after losing the hub.
public struct HubDiscovery: Decodable, Equatable, Sendable {
    public let url: String
    public let token: String
    public let pid: Int

    public init(url: String, token: String, pid: Int) {
        self.url = url
        self.token = token
        self.pid = pid
    }

    /// The hub's state directory: GH_HUD_STATE_DIR when set (as the hub's
    /// --state-dir), else the hub's default.
    public static var defaultStateDir: URL {
        if let dir = ProcessInfo.processInfo.environment["GH_HUD_STATE_DIR"], !dir.isEmpty {
            return URL(fileURLWithPath: dir, isDirectory: true)
        }
        return FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".local/state/gh-hud", isDirectory: true)
    }

    public enum LoadError: Error, Equatable, CustomStringConvertible {
        case noHub // hub.json is missing: no hub is running
        case invalid(String)

        public var description: String {
            switch self {
            case .noHub: return "no hub is running (start gh-hud or gh-hud serve)"
            case .invalid(let why): return "hub.json: \(why)"
            }
        }
    }

    public static func load(stateDir: URL = defaultStateDir) throws(LoadError) -> HubDiscovery {
        let file = stateDir.appendingPathComponent("hub.json")
        guard let data = FileManager.default.contents(atPath: file.path) else {
            throw .noHub
        }
        return try parse(data)
    }

    public static func parse(_ data: Data) throws(LoadError) -> HubDiscovery {
        let d: HubDiscovery
        do {
            d = try JSONDecoder().decode(HubDiscovery.self, from: data)
        } catch {
            throw .invalid(String(describing: error))
        }
        guard !d.url.isEmpty, !d.token.isEmpty else {
            throw .invalid("url or token missing")
        }
        return d
    }

    /// The header every request carries.
    public var authHeaders: [String: [String]] {
        ["Authorization": ["Bearer \(token)"]]
    }
}

/// Where the app finds the hub: on this Mac, through hub.json, or on
/// another Mac on the local network, at its [hub] lan address with its LAN
/// token (lan-token in that Mac's state directory).
public enum HubSource: Equatable, Sendable {
    case local(stateDir: URL)
    case remote(url: String, token: String)

    /// The hub on this Mac, in the default state directory.
    public static var thisMac: HubSource { .local(stateDir: HubDiscovery.defaultStateDir) }

    public var isRemote: Bool {
        if case .remote = self { return true }
        return false
    }

    /// Where and how to reach the hub now: hub.json says for this Mac's
    /// (it changes each time the hub starts); a remote hub's address and
    /// token are what you entered.
    public func discover() throws(HubDiscovery.LoadError) -> HubDiscovery {
        switch self {
        case .local(let dir):
            return try HubDiscovery.load(stateDir: dir)
        case .remote(let url, let token):
            guard let u = Self.normalize(url) else {
                throw .invalid("enter the other Mac's hub address in Settings")
            }
            let t = token.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !t.isEmpty else { throw .invalid("enter the hub's LAN token in Settings") }
            return HubDiscovery(url: u, token: t, pid: 0)
        }
    }

    /// The address as a URL the app can use: "macbook.local:7377" becomes
    /// "http://macbook.local:7377", without a trailing slash, and a bare
    /// host gets the hub's usual port, 7377. Nil when it has no host or
    /// isn't http(s).
    public static func normalize(_ s: String) -> String? {
        var t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { return nil }
        let bare = !t.contains("://")
        if bare { t = "http://" + t }
        guard let c = URLComponents(string: t), let scheme = c.scheme?.lowercased(),
              scheme == "http" || scheme == "https", let host = c.host, !host.isEmpty,
              c.path.isEmpty || c.path == "/", c.query == nil, c.user == nil
        else { return nil }
        // URLComponents keeps IPv6 brackets on host; add them only if missing.
        let h = host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
        let port = c.port ?? (bare ? lanPort : nil)
        return "\(scheme)://\(h)" + (port.map { ":\($0)" } ?? "")
    }

    /// The port a bare address gets: the one the README and Settings suggest
    /// for [hub] lan.
    public static let lanPort = 7377
}
