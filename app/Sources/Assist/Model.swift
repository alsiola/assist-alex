import Foundation

// The assist extension's Event payloads, as the app reads them.

struct StartBody: Decodable { var text: String; var title: String?; var role: String?; var notify: String? }
struct MessageBody: Decodable { var role: String; var text: String; var notify: String? }
struct StatusBody: Decodable { var title: String?; var status: String? }
struct AnswerBody: Decodable { var question_id: String; var answer: String }
struct QuestionBody: Decodable, Hashable {
    var question_id: String
    var kind: String?
    var text: String
    var context: String?
    var choices: [String]?
    var secret_name: String?
    var url: String?
    var deadline: String?

    var askKind: AskKind { AskKind(rawValue: kind ?? "question") ?? .question }
}
struct ProposalBody: Decodable { var change: String; var op: String; var pointer: String; var why: String?; var diagnostics: [JSONValue]? }
struct DecisionBody: Decodable { var changes: [String]; var decision: String; var reason: String? }
struct ChecksBody: Decodable { var source: String?; var valid: Bool; var approved: Int?; var diagnostics: [JSONValue]? }
struct AppliedBody: Decodable { var generation: String; var changes: [String]? }

enum AskKind: String { case question, approval, secret, action }

enum Notify: String { case quiet, fyi, now }

/// One line of a thread's transcript.
struct TranscriptItem: Identifiable, Hashable {
    enum Kind: Hashable {
        case alex(String)
        case assistant(String, Notify)
        case question(QuestionBody, answer: String?)
        case note(String)
    }
    var id: String
    var seq: Int
    var date: Date
    var kind: Kind
}

/// A thread as the app shows it: its Work, its Events in order, and what
/// Alex can do on it now.
struct ThreadState: Identifiable, Hashable {
    var work: Work
    var events: [ConduitEvent] = []
    var actions: [Action] = []
    var candidate: ConfigCandidate?
    /// What the agent is doing right now, from its live run.
    var activity: String?

    var id: String { work.id }

    var latestStatus: StatusBody? {
        var title: String?, line: String?
        for e in events where e.type == "assist.status" {
            guard let s = e.body(StatusBody.self) else { continue }
            if let t = s.title, !t.isEmpty { title = t }
            if let l = s.status, !l.isEmpty { line = l }
        }
        return (title == nil && line == nil) ? nil : StatusBody(title: title, status: line)
    }

    var title: String {
        latestStatus?.title ?? work.display?.title ?? "New thread"
    }

    var statusLine: String? { latestStatus?.status }

    var openedByAgent: Bool {
        events.first(where: { $0.type == "assist.start" })?.body(StartBody.self)?.role == "agent"
    }

    /// The question Alex can answer now: offered while the thread waits on it.
    var openQuestion: QuestionBody? {
        guard actions.contains(where: { $0.id == "answer" }) else { return nil }
        return events.last(where: { $0.type == "assist.question" })?.body(QuestionBody.self)
    }

    var openChanges: [CandidateChange] { candidate?.open ?? [] }
    var readyToApply: Bool { candidate?.readyToApply ?? false }

    var needsYou: Bool { work.isOpen && (openQuestion != nil || !openChanges.isEmpty || readyToApply) }
    var isRunning: Bool { work.state == "running" }

    var lastSeq: Int { events.last?.seq ?? 0 }
    var lastDate: Date { events.last?.date ?? ISO8601.parse(work.updated_at) ?? .distantPast }

    /// Why the thread wants Alex, in a few words.
    var needLabel: String? {
        guard work.isOpen else { return nil }
        if let q = openQuestion {
            switch q.askKind {
            case .approval: return "Needs your approval"
            case .secret: return "Needs a credential"
            case .action: return "Needs you to do something"
            case .question: return "Has a question"
            }
        }
        if !openChanges.isEmpty { return "Wants a config change" }
        if readyToApply { return "Changes ready to apply" }
        return nil
    }

    /// The last thing said, for the thread list.
    var preview: String {
        for item in transcript.reversed() {
            switch item.kind {
            case .assistant(let t, _): return t
            case .alex(let t): return "You: " + t
            case .question(let q, _): return q.text
            case .note: continue
            }
        }
        return ""
    }

    /// Whether anything the agent said loudly enough is newer than seen.
    func unread(seen: Int) -> Bool {
        events.contains { e in
            guard e.seq > seen else { return false }
            switch e.type {
            case "assist.question": return true
            case "assist.message":
                guard let m = e.body(MessageBody.self), m.role == "agent" else { return false }
                return (m.notify ?? "quiet") != "quiet"
            case "assist.start":
                return e.body(StartBody.self)?.role == "agent"
            default: return false
            }
        }
    }

    var transcript: [TranscriptItem] {
        var out: [TranscriptItem] = []
        var answers: [String: String] = [:]
        for e in events where e.type == "assist.answer" {
            if let a = e.body(AnswerBody.self) { answers[a.question_id] = a.answer }
        }
        for e in events {
            func add(_ k: TranscriptItem.Kind) { out.append(TranscriptItem(id: e.id, seq: e.seq, date: e.date, kind: k)) }
            switch e.type {
            case "assist.start":
                guard let s = e.body(StartBody.self) else { continue }
                if s.role == "agent" { add(.assistant(s.text, Notify(rawValue: s.notify ?? "") ?? .quiet)) } else { add(.alex(s.text)) }
            case "assist.message":
                guard let m = e.body(MessageBody.self) else { continue }
                if m.role == "operator" { add(.alex(m.text)) } else { add(.assistant(m.text, Notify(rawValue: m.notify ?? "") ?? .quiet)) }
            case "assist.question":
                guard let q = e.body(QuestionBody.self) else { continue }
                add(.question(q, answer: answers[q.question_id]))
            case "assist.answer":
                guard let a = e.body(AnswerBody.self) else { continue }
                add(.alex(a.answer))
            case "assist.status":
                if let s = e.body(StatusBody.self), let line = s.status, !line.isEmpty { add(.note(line)) }
            case "assist.proposal":
                if let p = e.body(ProposalBody.self) { add(.note("Proposed a config change: \(p.op) \(p.pointer)")) }
            case "assist.decision":
                if let d = e.body(DecisionBody.self) {
                    let who = d.decision == "withdrawn" ? "Withdrew" : "You \(d.decision)"
                    add(.note("\(who) \(d.changes.joined(separator: ", "))" + (d.reason.map { ": \($0)" } ?? "")))
                }
            case "assist.checks":
                if let c = e.body(ChecksBody.self) {
                    add(.note(c.valid ? "The approved changes validate" : "The approved changes have \(c.diagnostics?.count ?? 0) problems"))
                }
            case "assist.applied":
                if let a = e.body(AppliedBody.self) { add(.note("Applied as generation \(a.generation)")) }
            default: continue
            }
        }
        return out
    }
}

/// Reads what a running Claude Code turn is doing from its stream-json
/// output: the last tool it called or the last thing it wrote.
enum Activity {
    static func describe(_ live: LiveRun) -> String? {
        for out in live.outputs.reversed() {
            for line in out.content.split(separator: "\n").reversed() {
                guard line.hasPrefix("{"), let data = line.data(using: .utf8),
                      let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                      obj["type"] as? String == "assistant",
                      let msg = obj["message"] as? [String: Any],
                      let content = msg["content"] as? [[String: Any]] else { continue }
                for block in content.reversed() {
                    if block["type"] as? String == "tool_use", let name = block["name"] as? String {
                        return phrase(tool: name, input: block["input"] as? [String: Any] ?? [:])
                    }
                    if block["type"] as? String == "thinking" { return "Thinking" }
                }
            }
        }
        return nil
    }

    static func phrase(tool: String, input: [String: Any]) -> String {
        func file(_ k: String) -> String { ((input[k] as? String) ?? "").split(separator: "/").last.map(String.init) ?? "a file" }
        if tool.hasPrefix("mcp__open-conduit__") {
            let op = (input["operation"] as? String) ?? String(tool.dropFirst("mcp__open-conduit__".count))
            switch op {
            case "assist.say": return "Writing to you"
            case "assist.ask": return "Asking you something"
            case "assist.propose", "assist.read", "assist.validate": return "Working out a config change"
            case "wait": return "Setting a reminder"
            default: return "Using Open Conduit"
            }
        }
        if tool.hasPrefix("mcp__") {
            let parts = tool.components(separatedBy: "__")
            var server = parts.count > 1 ? parts[1] : ""
            if server.hasPrefix("claude_ai_") { server.removeFirst("claude_ai_".count) }
            server = server.replacingOccurrences(of: "_", with: " ").replacingOccurrences(of: "-", with: " ")
            return server.isEmpty ? "Using a connector" : "Using \(server)"
        }
        switch tool {
        case "Bash": return (input["description"] as? String).map { $0.prefix(1).uppercased() + $0.dropFirst() } ?? "Running a command"
        case "Read": return "Reading \(file("file_path"))"
        case "Write", "Edit", "MultiEdit": return "Writing \(file("file_path"))"
        case "Glob", "Grep": return "Searching files"
        case "WebSearch": return "Searching the web"
        case "WebFetch": return "Reading a web page"
        case "Task", "Agent": return "Handing part of it to a helper"
        case "TodoWrite": return "Planning"
        default: return "Working"
        }
    }
}
