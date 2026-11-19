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
        content.title = Links.alertTitle(alert.type)
        content.body = alert.text
        content.sound = .default
        if let url = Links.forAlert(alert) {
            content.userInfo = ["url": url.absoluteString]
        } else if let pane = Links.paneForAlert(alert.type) {
            content.userInfo = ["pane": pane]
        }
        center.add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil))
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse
    ) async {
        let info = response.notification.request.content.userInfo
        if let link = info["url"] as? String, let url = URL(string: link) {
            await MainActor.run { _ = NSWorkspace.shared.open(url) }
        } else if let pane = info["pane"] as? String {
            await MainActor.run {
                switch pane {
                case "toolkit": DashboardOpener.shared.open(.toolkit)
                case "workspaces": DashboardOpener.shared.open(.workspaces)
                default: break
                }
            }
        }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }
}
