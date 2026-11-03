import AppKit
import Foundation
import Observation

/// The app's state: every thread in the assist scope, kept current by
/// polling the daemon's local API, and everything Alex can do to them.
/// Open Conduit holds the truth; the store only remembers which Events
/// Alex has seen.
@MainActor
@Observable
final class Store {
    static let shared = Store()

    enum Connection: Equatable {
        case connecting
        case connected(version: String)
        case unreachable(String)
        case notConfigured
    }

    var conduit = Conduit(stateDir: Conduit.defaultStateDir)
    var scope: String = UserDefaults.standard.string(forKey: "scope") ?? "assist" {
        didSet { UserDefaults.standard.set(scope, forKey: "scope"); reset() }
    }

    private(set) var threads: [String: ThreadState] = [:]
    private(set) var connection: Connection = .connecting
    /// The selected thread, or nil to write a new one.
    var selection: String? {
        didSet { if let id = selection { markSeen(id) } }
    }
    /// A request in flight, or the last one's error, for the UI.
    var busy: Set<String> = []
    var lastError: String?

    private var lastSeq = 0
    private var synced = false
    private var seen: [String: Int] = (UserDefaults.standard.dictionary(forKey: "seen") as? [String: Int]) ?? [:]
    private var pendingStart: String?
    private var lastProposalNotice: [String: Date] = [:]
    private var pollTask: Task<Void, Never>?
    private var activityAt: [String: Date] = [:]

    let notifier = Notifier()

    /// The assistant's home: its working directory, where attachments go.
    var home: URL {
        if let s = UserDefaults.standard.string(forKey: "home"), !s.isEmpty { return URL(fileURLWithPath: s) }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("code/assist-alex/home")
    }

    /// Copies attachments into the assistant's inbox and returns the lines
    /// that point it at them: it reads the files (images included) from
    /// there, so no file's bytes travel in an Event.
    func stage(_ files: [URL]) throws -> String {
        guard !files.isEmpty else { return "" }
        let stamp = Date().formatted(.iso8601.year().month().day().time(includingFractionalSeconds: false).dateTimeSeparator(.standard)).replacingOccurrences(of: ":", with: "")
        let dir = home.appendingPathComponent("inbox/\(stamp)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        var lines: [String] = []
        for f in files {
            let dest = dir.appendingPathComponent(f.lastPathComponent)
            try? FileManager.default.removeItem(at: dest)
            try FileManager.default.copyItem(at: f, to: dest)
            lines.append("- \(dest.path)")
        }
        return "\n\nAttached:\n" + lines.joined(separator: "\n")
    }

    private init() {}

    // MARK: Lists

    var open: [ThreadState] { threads.values.filter { $0.work.isOpen }.sorted { $0.lastDate > $1.lastDate } }
    var needsYou: [ThreadState] { open.filter(\.needsYou) }
    var active: [ThreadState] { open.filter { !$0.needsYou } }
    var closed: [ThreadState] { threads.values.filter { !$0.work.isOpen }.sorted { $0.lastDate > $1.lastDate } }
    var needsYouCount: Int { needsYou.count }

    func isUnread(_ t: ThreadState) -> Bool { t.unread(seen: seen[t.id] ?? 0) }

    // MARK: Polling

    func start() {
        guard pollTask == nil else { return }
        notifier.setUp()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.refresh()
                let fast = self.threads.values.contains { $0.isRunning } || self.pendingStart != nil
                try? await Task.sleep(for: .milliseconds(fast ? 1000 : 2500))
            }
        }
    }

    private func reset() {
        threads = [:]; lastSeq = 0; synced = false; connection = .connecting
    }

    func refresh() async {
        do {
            let health = try await conduit.health()
            if !synced {
                do { _ = try await conduit.scopeActions(scope: scope) } catch let e as ConduitError where e.status == 404 {
                    connection = .notConfigured
                    return
                }
            }
            let works = try await conduit.threads(scope: scope)
            let fresh = try await conduit.events(scope: scope, after: lastSeq)
            var changed = Set<String>()
            for w in works {
                if threads[w.id]?.work != w { changed.insert(w.id) }
                if threads[w.id] == nil { threads[w.id] = ThreadState(work: w) } else { threads[w.id]?.work = w }
            }
            for e in fresh.sorted(by: { $0.seq < $1.seq }) {
                lastSeq = max(lastSeq, e.seq)
                guard let id = e.threadID, threads[id] != nil else { continue }
                if threads[id]?.events.contains(where: { $0.id == e.id }) == false {
                    threads[id]?.events.append(e)
                    changed.insert(id)
                }
            }
            for id in changed { await reload(id) }
            await updateActivity()
            if let pending = pendingStart, let e = fresh.first(where: { $0.id == pending }) ?? threads.values.flatMap(\.events).first(where: { $0.id == pending }),
               let id = e.threadID, threads[id] != nil {
                pendingStart = nil
                selection = id
            }
            if synced { announce(fresh) }
            synced = true
            connection = .connected(version: health.version)
            if let id = selection, NSApp.isActive { markSeen(id) }
            updateBadge()
        } catch {
            connection = .unreachable(error.localizedDescription)
        }
    }

    /// Reloads what Alex can do on a thread, and its change set.
    private func reload(_ id: String) async {
        guard let t = threads[id] else { return }
        guard t.work.isOpen else {
            threads[id]?.actions = []
            return
        }
        threads[id]?.actions = (try? await conduit.actions(work: id)) ?? []
        if t.events.contains(where: { $0.type == "assist.proposal" }) {
            threads[id]?.candidate = try? await conduit.candidate(work: id)
        }
    }

    private func updateActivity() async {
        for t in threads.values {
            guard t.isRunning else {
                if t.activity != nil { threads[t.id]?.activity = nil }
                continue
            }
            if let at = activityAt[t.id], Date().timeIntervalSince(at) < 1.5 { continue }
            activityAt[t.id] = Date()
            guard let run = try? await conduit.runningRun(work: t.id), let live = try? await conduit.live(run: run.id) else { continue }
            threads[t.id]?.activity = Activity.describe(live)
        }
    }

    // MARK: Seen and notifications

    func markSeen(_ id: String) {
        guard let t = threads[id], t.lastSeq > (seen[id] ?? 0) else { return }
        seen[id] = t.lastSeq
        UserDefaults.standard.set(seen, forKey: "seen")
        notifier.withdraw(thread: id)
        updateBadge()
    }

    private func updateBadge() {
        let n = needsYouCount
        NSApp.dockTile.badgeLabel = n > 0 ? String(n) : nil
    }

    /// Posts a notification for what the agent said loudly, or asked.
    private func announce(_ events: [ConduitEvent]) {
        for e in events {
            guard let id = e.threadID, let t = threads[id] else { continue }
            let looking = NSApp.isActive && selection == id
            switch e.type {
            case "assist.question":
                guard !looking, let q = e.body(QuestionBody.self) else { continue }
                notifier.post(thread: id, title: t.title, body: q.text, kind: q.askKind)
            case "assist.message":
                guard !looking, let m = e.body(MessageBody.self), m.role == "agent", m.notify == "now" else { continue }
                notifier.post(thread: id, title: t.title, body: m.text, kind: nil)
            case "assist.start":
                guard let s = e.body(StartBody.self), s.role == "agent", s.notify == "now" else { continue }
                notifier.post(thread: id, title: s.title ?? t.title, body: s.text, kind: nil)
            case "assist.proposal":
                guard !looking, Date().timeIntervalSince(lastProposalNotice[id] ?? .distantPast) > 120 else { continue }
                lastProposalNotice[id] = Date()
                notifier.post(thread: id, title: t.title, body: "Wants to change Open Conduit's config. Review it when you have a minute.", kind: nil)
            default: continue
            }
        }
    }

    // MARK: Doing things

    private func perform(_ key: String, _ work: @escaping () async throws -> Void) async {
        busy.insert(key)
        defer { busy.remove(key) }
        do {
            try await work()
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
        await refresh()
    }

    /// Sends text: a new thread when none is selected, else a message.
    func send(_ text: String, to id: String?, attachments: [URL] = []) async {
        var text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !attachments.isEmpty {
            do {
                if text.isEmpty { text = attachments.count == 1 ? "Here's a file." : "Here are some files." }
                text += try stage(attachments)
            } catch {
                lastError = "Couldn't attach: \(error.localizedDescription)"
                return
            }
        }
        guard !text.isEmpty else { return }
        if let id {
            await perform("send:\(id)") { try await self.conduit.invoke(work: id, action: "message", input: text) }
        } else {
            await perform("send:new") {
                let r = try await self.conduit.start(scope: self.scope, text: text)
                self.pendingStart = r.event.id
            }
        }
    }

    func answer(_ id: String, _ text: String) async {
        await perform("answer:\(id)") { try await self.conduit.invoke(work: id, action: "answer", input: text) }
    }

    /// Stores a credential in Alex's keychain through Open Conduit, then
    /// answers the question with its reference: the value never reaches
    /// the agent or the daemon's Events.
    func storeSecret(_ id: String, name: String, value: String) async {
        await perform("answer:\(id)") {
            try await OpenConduitCLI.setSecret(name: name, value: value)
            try await self.conduit.invoke(work: id, action: "answer", input: "Stored as keyring:\(name)")
        }
    }

    func decide(_ id: String, action: String, input: String = "") async {
        await perform("decide:\(id)") { try await self.conduit.invoke(work: id, action: action, input: input) }
        await reload(id)
    }

    func apply(_ id: String) async {
        await perform("apply:\(id)") {
            let c = try await self.conduit.candidate(work: id)
            guard c.readyToApply else { throw ConduitError(status: 0, code: "nothing_to_apply", message: "No approved changes to apply.") }
            try await self.conduit.apply(c)
        }
        await reload(id)
    }

    func archive(_ id: String) async {
        guard let w = threads[id]?.work else { return }
        await perform("archive:\(id)") { try await self.conduit.cancel(work: w, reason: "Alex archived the thread") }
        if selection == id { selection = nil }
    }

    func restartDaemon() async {
        await perform("restart") { try await OpenConduitCLI.restart() }
    }
}
