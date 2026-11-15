import GHHudKit
import Security
import SwiftUI

/// Which hub the app uses, kept across launches: this Mac's, or one on
/// another Mac on the local network. The address is in UserDefaults, the
/// LAN token in the Keychain.
enum HubSettings {
    private static let modeKey = "hub.mode" // "this-mac" or "other-mac"
    private static let urlKey = "hub.url"
    private static let service = "io.github.jflowers.gh-hud"
    private static let account = "hub-lan-token"

    static var useOtherMac: Bool {
        UserDefaults.standard.string(forKey: modeKey) == "other-mac"
    }

    static var url: String {
        UserDefaults.standard.string(forKey: urlKey) ?? ""
    }

    /// The source the saved settings name.
    static var source: HubSource {
        useOtherMac ? .remote(url: url, token: token) : .thisMac
    }

    static func save(otherMac: Bool, url: String, token: String) {
        UserDefaults.standard.set(otherMac ? "other-mac" : "this-mac", forKey: modeKey)
        UserDefaults.standard.set(url, forKey: urlKey)
        setToken(token)
    }

    static var token: String {
        var q = baseQuery
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data else {
            return ""
        }
        return String(decoding: data, as: UTF8.self)
    }

    private static func setToken(_ t: String) {
        SecItemDelete(baseQuery as CFDictionary)
        guard !t.isEmpty else { return }
        var q = baseQuery
        q[kSecValueData as String] = Data(t.utf8)
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        SecItemAdd(q as CFDictionary, nil)
    }

    private static var baseQuery: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }
}

/// The settings form's fields. Not @State (a macro only Xcode ships): see
/// GHHudApp.
@MainActor
final class HubSettingsForm: ObservableObject {
    @Published var otherMac = HubSettings.useOtherMac { didSet { saved = false } }
    @Published var url = HubSettings.url { didSet { saved = false } }
    @Published var token = HubSettings.token { didSet { saved = false } }
    @Published var saved = false
}

/// Settings (⌘,): which hub to use.
struct HubSettingsView: View {
    let conn: HubConnection
    @StateObject private var form = HubSettingsForm()

    var body: some View {
        Form {
            Picker("Hub:", selection: $form.otherMac) {
                Text("On this Mac").tag(false)
                Text("On another Mac on the network").tag(true)
            }
            .pickerStyle(.radioGroup)
            if form.otherMac {
                TextField("Address:", text: $form.url, prompt: Text("macbook.local:7377"))
                SecureField("LAN token:", text: $form.token)
                Text("""
                    On the hub's Mac, set [hub] lan in gh-hud's config (e.g. "0.0.0.0:7377"). \
                    The hub logs its address when it starts, and the token is in lan-token \
                    in its state directory (~/.local/state/gh-hud). Watch and Open Transcript \
                    only work on the hub's Mac.
                    """)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if !form.url.isEmpty, HubSource.normalize(form.url) == nil {
                    Text("That isn't an address like macbook.local:7377")
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            }
            HStack {
                Spacer()
                if form.saved { Text("Saved").foregroundStyle(.secondary) }
                Button("Connect") { connect() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(form.otherMac && (HubSource.normalize(form.url) == nil || form.token.isEmpty))
            }
            if case .noHub(let why) = conn.status {
                Text(why).font(.caption).foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            } else if conn.status == .connected {
                Text(conn.isRemote ? "Connected to the hub on the other Mac" : "Connected to this Mac's hub")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(20)
        .frame(width: 460)
    }

    private func connect() {
        HubSettings.save(otherMac: form.otherMac, url: form.url, token: form.token)
        conn.setSource(HubSettings.source)
        form.saved = true
    }
}
