import XCTest
@testable import Assist

final class UnixHTTPTests: XCTestCase {
    func testContentLength() throws {
        let raw = Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n{\"data\":{}}".utf8)
        let r = try UnixHTTP.parse(raw)
        XCTAssertEqual(r.status, 200)
        XCTAssertEqual(String(decoding: r.body, as: UTF8.self), "{\"data\":{}}")
    }

    func testChunked() throws {
        let raw = Data("HTTP/1.1 404 Not Found\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n".utf8)
        let r = try UnixHTTP.parse(raw)
        XCTAssertEqual(r.status, 404)
        XCTAssertEqual(String(decoding: r.body, as: UTF8.self), "hello world")
    }

    func testTruncatedChunkFails() {
        XCTAssertThrowsError(try UnixHTTP.parse(Data("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nff\r\nshort".utf8)))
    }
}

final class ThreadTests: XCTestCase {
    func event(_ seq: Int, _ type: String, _ data: [String: Any]) -> ConduitEvent {
        let json = try! JSONSerialization.data(withJSONObject: data)
        return ConduitEvent(id: "e\(seq)", type: type, source: "x", occurred_at: "2026-11-03T09:00:0\(seq % 10)Z", work_id: "w",
                            data: Payload(schema: type + ".v1", data: try! JSONDecoder().decode(JSONValue.self, from: json)), sequence: String(seq))
    }

    var thread: ThreadState {
        let w = Work(id: "w", kind: "assist.thread", scope_id: "assist", state: "waiting", display: WorkDisplay(title: "Renew insurance"), revision: "3", created_at: "", updated_at: "")
        return ThreadState(work: w, events: [
            event(1, "assist.start", ["text": "Renew the car insurance", "role": "operator"]),
            event(2, "assist.status", ["title": "Car insurance", "status": "Comparing quotes"]),
            event(3, "assist.message", ["role": "agent", "text": "Three quotes in.", "notify": "quiet"]),
            event(4, "assist.question", ["question_id": "q1", "kind": "approval", "text": "Go with Admiral at £412?", "choices": ["Approve", "Decline"]]),
        ], actions: [Action(provider: "assist", id: "answer", label: "Answer")])
    }

    func testTitleStatusAndNeed() {
        let t = thread
        XCTAssertEqual(t.title, "Car insurance")
        XCTAssertEqual(t.statusLine, "Comparing quotes")
        XCTAssertEqual(t.openQuestion?.askKind, .approval)
        XCTAssertTrue(t.needsYou)
        XCTAssertEqual(t.needLabel, "Needs your approval")
    }

    func testNoAnswerActionNoQuestion() {
        var t = thread
        t.actions = []
        XCTAssertNil(t.openQuestion)
        XCTAssertFalse(t.needsYou)
    }

    func testQuietIsNotUnreadButAQuestionIs() {
        let t = thread
        XCTAssertTrue(t.unread(seen: 3), "a question is unread")
        XCTAssertFalse(t.unread(seen: 4))
        var quiet = t
        quiet.events.removeLast()
        XCTAssertFalse(quiet.unread(seen: 1), "a quiet message is not unread")
    }

    func testTranscriptPairsAnswers() {
        var t = thread
        t.events.append(event(5, "assist.answer", ["question_id": "q1", "answer": "Approve"]))
        let kinds = t.transcript.map(\.kind)
        XCTAssertEqual(kinds.count, 4, "a status is the header, not a transcript line")
        if case .question(_, let answer) = kinds[2] { XCTAssertEqual(answer, "Approve") } else { XCTFail("no question") }
        if case .alex(let text) = kinds[3] { XCTAssertEqual(text, "Approve") } else { XCTFail("no answer") }
    }
}

final class ActivityTests: XCTestCase {
    func testLastToolUse() {
        let lines = [
            #"{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x/notes.md"}}]}}"#,
            #"{"type":"user","message":{"content":[]}}"#,
            #"{"type":"assistant","message":{"content":[{"type":"text","text":"hm"},{"type":"tool_use","name":"mcp__claude_ai_Gmail__search_threads","input":{}}]}}"#,
        ]
        let live = LiveRun(outputs: [LiveOutput(name: "stdout", content: lines.joined(separator: "\n"))])
        XCTAssertEqual(Activity.describe(live), "Using Gmail")
        XCTAssertEqual(Activity.phrase(tool: "Read", input: ["file_path": "/a/b.txt"]), "Reading b.txt")
        XCTAssertEqual(Activity.phrase(tool: "mcp__open-conduit__call", input: ["operation": "assist.ask"]), "Asking you something")
    }
}

final class PathTests: XCTestCase {
    func testWorkIDsWithSlashesStayEscapedOnce() {
        XCTAssertEqual(Conduit.requestPath("work/\(Conduit.esc("assist/assist/abc"))/actions"), "/v1/work/assist%2Fassist%2Fabc/actions")
        XCTAssertEqual(Conduit.requestPath("events", query: ["scope_id": "assist", "after_sequence": "5"]), "/v1/events?after_sequence=5&scope_id=assist")
    }
}
