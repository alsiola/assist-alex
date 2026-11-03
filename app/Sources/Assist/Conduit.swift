import Foundation

// Wire types of Open Conduit's local management API (the subset Assist reads).

struct Payload: Codable, Hashable, Sendable {
    var schema: String?
    var data: JSONValue?
}

struct CorrelationKey: Codable, Hashable, Sendable {
    var source: String
    var external_id: String
}

struct WorkDisplay: Codable, Hashable, Sendable {
    var title: String?
}

struct Work: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var kind: String
    var scope_id: String
    var state: String
    var display: WorkDisplay?
    var revision: String
    var created_at: String
    var updated_at: String
    var context: Payload?

    var isOpen: Bool { !["completed", "failed", "cancelled"].contains(state) }
}

struct ConduitEvent: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var type: String
    var source: String
    var occurred_at: String
    var correlation_key: CorrelationKey?
    var work_id: String?
    var routed_work_id: String?
    var data: Payload?
    var sequence: String
    var actor: String?

    var seq: Int { Int(sequence) ?? 0 }
    /// The thread the Event belongs to: the Work it was recorded on, or the
    /// Work it routed to.
    var threadID: String? { work_id ?? routed_work_id }
    var date: Date { ISO8601.parse(occurred_at) ?? .distantPast }
    func body<T: Decodable>(_ t: T.Type) -> T? { data?.data?.decode(t) }
}

struct Action: Codable, Hashable, Sendable {
    var provider: String
    var id: String
    var label: String
    var prompt: String?
    var choices: [String]?
    var detail: String?
}

struct ActionResult: Codable, Sendable {
    var event: ConduitEvent
    var message: String?
}

struct CandidateChange: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var op: String
    var pointer: String
    var state: String
    var why: String
    var before: JSONValue?
    var after: JSONValue?
    var problems: [String]?
    var approve: String?
    var reject: String?

    /// An add onto something already there replaces it (a member of an
    /// object; an add onto an array element is refused when proposed).
    var overwrites: Bool { op == "add" && before != nil && before != .null }
}

struct ConfigCandidate: Codable, Hashable, Sendable {
    var config_text: String?
    var base_generation: String?
    var candidate_hash: String?
    var changes: [CandidateChange]?
    var approve_all: String?
    var validate: String?

    var open: [CandidateChange] { (changes ?? []).filter { $0.state == "proposed" } }
    var approved: [CandidateChange] { (changes ?? []).filter { $0.state == "approved" } }
    /// Approved changes that validate together, ready for Alex to apply.
    var readyToApply: Bool { !(config_text ?? "").isEmpty && !approved.isEmpty }
}

struct Health: Codable, Sendable {
    var state: String
    var version: String
    var config_generation: String
}

struct Page<T: Codable & Sendable>: Codable, Sendable {
    var items: [T]
    var next_cursor: String?
}

struct RunSummary: Codable, Sendable {
    var id: String
    var state: String
}

struct LiveOutput: Codable, Sendable {
    var name: String
    var content: String
}

struct LiveRun: Codable, Sendable {
    var outputs: [LiveOutput]
}

struct ConduitError: Error, LocalizedError, Sendable {
    var status: Int
    var code: String
    var message: String
    var errorDescription: String? { message.isEmpty ? code : message }
}

enum ISO8601 {
    nonisolated(unsafe) static let frac: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()
    nonisolated(unsafe) static let plain = ISO8601DateFormatter()
    static func parse(_ s: String) -> Date? { frac.date(from: s) ?? plain.date(from: s) }
}

/// The management API client. It reads the operator token from Open
/// Conduit's state directory on every call, so a rotated token just works.
struct Conduit: Sendable {
    var stateDir: URL

    static var defaultStateDir: URL {
        if let s = ProcessInfo.processInfo.environment["OPEN_CONDUIT_STATE_DIR"], !s.isEmpty { return URL(fileURLWithPath: s) }
        let base = ProcessInfo.processInfo.environment["XDG_STATE_HOME"].map { URL(fileURLWithPath: $0) }
            ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/state")
        return base.appendingPathComponent("open-conduit")
    }

    var socket: String { stateDir.appendingPathComponent("management.sock").path }

    private func token() throws -> String {
        let url = stateDir.appendingPathComponent("private/operator.token")
        guard let data = try? Data(contentsOf: url),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let t = obj["token"] as? String, !t.isEmpty else {
            throw ConduitError(status: 0, code: "no_token", message: "Can't read Open Conduit's operator token at \(url.path).")
        }
        return t
    }

    private func call<T: Decodable>(_ method: String, _ path: String, query: [String: String] = [:], body: Encodable? = nil, as: T.Type) async throws -> T {
        var headers = ["Authorization": "Bearer \(try token())"]
        var payload: Data?
        if let body {
            payload = try JSONEncoder().encode(AnyEncodable(body))
            headers["Content-Type"] = "application/json"
            headers["Idempotency-Key"] = "assist-" + UUID().uuidString.lowercased()
        }
        let http = UnixHTTP(socketPath: socket)
        let res = try await http.send(method: method, path: Self.requestPath(path, query: query), headers: headers, body: payload)
        if res.status >= 400 {
            let e = (try? JSONDecoder().decode(ErrorEnvelope.self, from: res.body))?.error
            throw ConduitError(status: res.status, code: e?.code ?? "http_\(res.status)", message: e?.message ?? "")
        }
        do {
            return try JSONDecoder().decode(DataEnvelope<T>.self, from: res.body).data
        } catch {
            throw UnixHTTP.Failure.malformed("\(path): \(error)")
        }
    }

    /// The request target: path segments already escaped with esc, so an
    /// ID's "/" stays %2F and is never escaped twice.
    static func requestPath(_ path: String, query: [String: String] = [:]) -> String {
        var comps = URLComponents()
        comps.percentEncodedPath = "/v1/" + path
        if !query.isEmpty { comps.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) } }
        return comps.percentEncodedPath + (comps.percentEncodedQuery.map { "?" + $0 } ?? "")
    }

    static func esc(_ id: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/")
        return id.addingPercentEncoding(withAllowedCharacters: allowed) ?? id
    }

    func health() async throws -> Health { try await call("GET", "health", as: Health.self) }

    func threads(scope: String) async throws -> [Work] {
        var out: [Work] = []
        var cursor: String?
        repeat {
            var q = ["scope_id": scope, "kind": "assist.thread", "limit": "200"]
            if let cursor { q["cursor"] = cursor }
            let page = try await call("GET", "work", query: q, as: Page<Work>.self)
            out += page.items
            cursor = (page.next_cursor?.isEmpty == false) ? page.next_cursor : nil
        } while cursor != nil
        return out
    }

    func events(scope: String, after: Int) async throws -> [ConduitEvent] {
        var out: [ConduitEvent] = []
        var after = after
        while true {
            let page = try await call("GET", "events", query: ["scope_id": scope, "after_sequence": String(after), "limit": "200"], as: Page<ConduitEvent>.self)
            if page.items.isEmpty { break }
            out += page.items
            after = page.items.map(\.seq).max() ?? after
            if page.items.count < 200 { break }
        }
        return out
    }

    func actions(work: String) async throws -> [Action] {
        struct Out: Decodable { var actions: [Action] }
        return try await call("GET", "work/\(Self.esc(work))/actions", as: Out.self).actions
    }

    func scopeActions(scope: String) async throws -> [Action] {
        struct Out: Decodable { var actions: [Action] }
        return try await call("GET", "scopes/\(Self.esc(scope))/actions", as: Out.self).actions
    }

    @discardableResult
    func invoke(work: String, provider: String = "assist", action: String, input: String = "") async throws -> ActionResult {
        try await call("POST", "work/\(Self.esc(work))/actions/\(Self.esc(provider))/\(Self.esc(action))", body: ["input": input], as: ActionResult.self)
    }

    func start(scope: String, text: String) async throws -> ActionResult {
        try await call("POST", "scopes/\(Self.esc(scope))/actions/assist/start", body: ["input": text], as: ActionResult.self)
    }

    func candidate(work: String) async throws -> ConfigCandidate {
        try await call("GET", "work/\(Self.esc(work))/config-candidate", as: ConfigCandidate.self)
    }

    func apply(_ c: ConfigCandidate) async throws {
        struct Body: Encodable { var config_text: String; var candidate_hash: String; var expected_generation: String }
        _ = try await call("POST", "config/apply", body: Body(config_text: c.config_text ?? "", candidate_hash: c.candidate_hash ?? "", expected_generation: c.base_generation ?? ""), as: JSONValue.self)
    }

    func cancel(work: Work, reason: String) async throws {
        struct Body: Encodable { var expected_revision: String; var reason: String }
        _ = try await call("POST", "work/\(Self.esc(work.id))/cancel", body: Body(expected_revision: work.revision, reason: reason), as: JSONValue.self)
    }

    func runningRun(work: String) async throws -> RunSummary? {
        try await call("GET", "runs", query: ["work_id": work, "state": "running", "limit": "1"], as: Page<RunSummary>.self).items.first
    }

    func live(run: String, tail: Int = 16000) async throws -> LiveRun {
        try await call("GET", "runs/\(Self.esc(run))/live", query: ["tail": String(tail)], as: LiveRun.self)
    }
}

private struct ErrorEnvelope: Decodable {
    struct E: Decodable { var code: String?; var message: String? }
    var error: E?
}

private struct DataEnvelope<D: Decodable>: Decodable { var data: D }

private struct AnyEncodable: Encodable {
    let value: Encodable
    init(_ v: Encodable) { value = v }
    func encode(to encoder: Encoder) throws { try value.encode(to: encoder) }
}
