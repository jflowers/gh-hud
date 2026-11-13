import AppKit
import GHHudKit
import UserNotifications

/// Shows the hub's alerts as macOS notifications; clicking one opens its
/// PR. Only an app bundle can post notifications, so a bare `swift run`
/// skips them (and the hub keeps posting its own).
@MainActor
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    private var center: UNUserNotificationCenter? {
        Bundle.main.bundleIdentifier == nil ? nil : UNUserNotificationCenter.current()
    }

    /// Asks to show notifications; done gets whether we may.
    func authorize(_ done: @escaping @MainActor (Bool) -> Void) {
        guard let center else {
            done(false)
            return
        }
        center.delegate = self
        center.requestAuthorization(options: [.alert, .sound]) { granted, _ in
            Task { @MainActor in done(granted) }
        }
    }

    func post(_ alert: Ghhud_V1_Alert) {
        guard let center else { return }
        let content = UNMutableNotificationContent()
        content.title = Self.title(alert.type)
        content.body = alert.text
        content.sound = .default
        if let url = Links.forAlert(alert) {
            content.userInfo = ["url": url.absoluteString]
        }
        center.add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil))
    }

    static func title(_ t: Ghhud_V1_AlertType) -> String {
        switch t {
        case .ready: return "Ready for review"
        case .blocked: return "An agent is waiting on you"
        case .runFinished: return "Agent run finished"
        default: return "gh-hud"
        }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse
    ) async {
        let link = response.notification.request.content.userInfo["url"] as? String
        if let link, let url = URL(string: link) {
            await MainActor.run { _ = NSWorkspace.shared.open(url) }
        }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }
}
