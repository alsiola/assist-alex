import AppKit
import UserNotifications

/// Posts the assistant's interruptions as notifications Alex can act on
/// without opening the app: reply in the banner, or approve and decline.
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    private var center: UNUserNotificationCenter? {
        // Notifications need a bundled app; `swift run` has no bundle.
        Bundle.main.bundleIdentifier == nil ? nil : UNUserNotificationCenter.current()
    }

    enum Category: String {
        case reply = "ASSIST_REPLY", approval = "ASSIST_APPROVAL", open = "ASSIST_OPEN"
    }

    func setUp() {
        guard let center else { return }
        center.delegate = self
        let reply = UNTextInputNotificationAction(identifier: "reply", title: "Reply", options: [], textInputButtonTitle: "Send", textInputPlaceholder: "Reply")
        let approve = UNNotificationAction(identifier: "approve", title: "Approve", options: [])
        let decline = UNNotificationAction(identifier: "decline", title: "Decline", options: [])
        center.setNotificationCategories([
            UNNotificationCategory(identifier: Category.reply.rawValue, actions: [reply], intentIdentifiers: []),
            UNNotificationCategory(identifier: Category.approval.rawValue, actions: [approve, decline, reply], intentIdentifiers: []),
            UNNotificationCategory(identifier: Category.open.rawValue, actions: [], intentIdentifiers: []),
        ])
        center.requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in }
    }

    func post(thread: String, title: String, body: String, kind: AskKind?) {
        guard let center else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = String(body.prefix(400))
        content.sound = .default
        content.threadIdentifier = thread
        content.userInfo = ["thread": thread]
        switch kind {
        case .approval: content.categoryIdentifier = Category.approval.rawValue
        case .secret, .action: content.categoryIdentifier = Category.open.rawValue
        default: content.categoryIdentifier = Category.reply.rawValue
        }
        center.add(UNNotificationRequest(identifier: thread + "/" + UUID().uuidString, content: content, trigger: nil))
    }

    func withdraw(thread: String) {
        guard let center else { return }
        center.getDeliveredNotifications { delivered in
            let ids = delivered.filter { ($0.request.content.userInfo["thread"] as? String) == thread }.map(\.request.identifier)
            center.removeDeliveredNotifications(withIdentifiers: ids)
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound, .list]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        guard let thread = response.notification.request.content.userInfo["thread"] as? String else { return }
        let text = (response as? UNTextInputNotificationResponse)?.userText
        await MainActor.run {
            let store = Store.shared
            Task { @MainActor in
                switch response.actionIdentifier {
                case "approve": await store.answer(thread, "Approve")
                case "decline": await store.answer(thread, "Decline")
                case "reply":
                    guard let text, !text.isEmpty else { return }
                    if store.threads[thread]?.openQuestion != nil { await store.answer(thread, text) } else { await store.send(text, to: thread) }
                default:
                    store.selection = thread
                    NSApp.activate(ignoringOtherApps: true)
                    for w in NSApp.windows where w.identifier?.rawValue.contains("main") == true || w.title == "Assist" { w.makeKeyAndOrderFront(nil) }
                }
            }
        }
    }
}
