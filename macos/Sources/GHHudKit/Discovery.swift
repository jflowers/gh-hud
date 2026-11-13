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
