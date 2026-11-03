import AppKit
import SwiftUI

@main
struct AssistApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var store = Store.shared
    @Environment(\.openWindow) private var openWindow

    var body: some Scene {
        Window("Assist", id: "main") {
            ContentView()
                .environment(store)
                .frame(minWidth: 760, minHeight: 480)
        }
        .defaultSize(width: 1080, height: 720)
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("New Thread") { store.selection = nil }
                    .keyboardShortcut("n")
            }
            CommandMenu("Open Conduit") {
                Button("Restart Open Conduit") { Task { await store.restartDaemon() } }
            }
        }

        MenuBarExtra {
            MenuBarContent(openMain: { openWindow(id: "main") })
                .environment(store)
        } label: {
            let n = store.needsYouCount
            Image(systemName: n > 0 ? "bubble.left.and.exclamationmark.bubble.right.fill" : "bubble.left.and.bubble.right")
            if n > 0 { Text("\(n)") }
        }

        Settings {
            SettingsView().environment(store)
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        Task { @MainActor in Store.shared.start() }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    func applicationDidBecomeActive(_ notification: Notification) {
        Task { @MainActor in
            if let id = Store.shared.selection { Store.shared.markSeen(id) }
        }
    }
}

struct MenuBarContent: View {
    @Environment(Store.self) private var store
    let openMain: () -> Void

    var body: some View {
        if store.needsYou.isEmpty {
            Text("Nothing needs you")
        } else {
            ForEach(store.needsYou) { t in
                Button("\(t.title) — \(t.needLabel ?? "")") { show(t.id) }
            }
        }
        if !store.active.isEmpty {
            Divider()
            ForEach(store.active.prefix(8)) { t in
                Button(t.isRunning ? "\(t.title) (working)" : t.title) { show(t.id) }
            }
        }
        Divider()
        Button("New Thread…") { show(nil) }
            .keyboardShortcut("n")
        Button("Open Assist") { show(store.selection) }
        Divider()
        Button("Quit Assist") { NSApp.terminate(nil) }
            .keyboardShortcut("q")
    }

    private func show(_ id: String?) {
        store.selection = id
        openMain()
        NSApp.activate(ignoringOtherApps: true)
    }
}

struct SettingsView: View {
    @Environment(Store.self) private var store
    @State private var scope = ""

    var body: some View {
        Form {
            TextField("Scope", text: $scope)
                .onSubmit { store.scope = scope }
            LabeledContent("State directory", value: store.conduit.stateDir.path)
            LabeledContent("Daemon") {
                switch store.connection {
                case .connected(let v): Text("Open Conduit \(v)")
                case .connecting: Text("Connecting…")
                case .notConfigured: Text("No \(store.scope) scope")
                case .unreachable(let m): Text(m).foregroundStyle(.secondary)
                }
            }
        }
        .formStyle(.grouped)
        .frame(width: 460)
        .onAppear { scope = store.scope }
    }
}
