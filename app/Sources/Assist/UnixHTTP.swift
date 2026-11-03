import Foundation

/// A minimal HTTP/1.1 client over a Unix domain socket: Open Conduit's
/// management API listens only there. URLSession can't dial a Unix socket,
/// so this speaks just enough HTTP for JSON requests: one request per
/// connection, Content-Length or chunked bodies.
struct UnixHTTP: Sendable {
    let socketPath: String
    var timeout: TimeInterval = 30

    struct Response: Sendable {
        let status: Int
        let headers: [String: String]
        let body: Data
    }

    enum Failure: Error, LocalizedError {
        case unreachable(String)
        case io(String)
        case malformed(String)

        var errorDescription: String? {
            switch self {
            case .unreachable(let m): return "Open Conduit isn't reachable: \(m)"
            case .io(let m): return "Lost the connection to Open Conduit: \(m)"
            case .malformed(let m): return "Open Conduit sent something unreadable: \(m)"
            }
        }
    }

    func send(method: String, path: String, headers: [String: String] = [:], body: Data? = nil) async throws -> Response {
        try await withCheckedThrowingContinuation { cont in
            DispatchQueue.global(qos: .userInitiated).async {
                do { cont.resume(returning: try self.sendBlocking(method: method, path: path, headers: headers, body: body)) }
                catch { cont.resume(throwing: error) }
            }
        }
    }

    func sendBlocking(method: String, path: String, headers: [String: String], body: Data?) throws -> Response {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw Failure.unreachable(String(cString: strerror(errno))) }
        defer { close(fd) }

        var on: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        var tv = timeval(tv_sec: Int(timeout), tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let pathBytes = Array(socketPath.utf8)
        let capacity = MemoryLayout.size(ofValue: addr.sun_path)
        guard pathBytes.count < capacity else { throw Failure.unreachable("socket path too long") }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: pathBytes)
            raw[pathBytes.count] = 0
        }
        let len = socklen_t(MemoryLayout<sa_family_t>.size + pathBytes.count + 1)
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, len) }
        }
        guard rc == 0 else { throw Failure.unreachable(String(cString: strerror(errno))) }

        var head = "\(method) \(path) HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\nAccept: application/json\r\n"
        for (k, v) in headers { head += "\(k): \(v)\r\n" }
        if let body { head += "Content-Length: \(body.count)\r\n" }
        head += "\r\n"
        var out = Data(head.utf8)
        if let body { out.append(body) }
        try out.withUnsafeBytes { raw in
            var sent = 0
            while sent < raw.count {
                let n = write(fd, raw.baseAddress!.advanced(by: sent), raw.count - sent)
                if n <= 0 { throw Failure.io(String(cString: strerror(errno))) }
                sent += n
            }
        }

        var received = Data()
        var buf = [UInt8](repeating: 0, count: 64 * 1024)
        while true {
            let n = read(fd, &buf, buf.count)
            if n == 0 { break }
            if n < 0 {
                if errno == EINTR { continue }
                throw Failure.io(String(cString: strerror(errno)))
            }
            received.append(buf, count: n)
            if received.count > 64 << 20 { throw Failure.malformed("response too large") }
        }
        return try Self.parse(received)
    }

    static func parse(_ raw: Data) throws -> Response {
        let sep = Data("\r\n\r\n".utf8)
        guard let split = raw.range(of: sep) else { throw Failure.malformed("no header terminator") }
        guard let head = String(data: raw[raw.startIndex..<split.lowerBound], encoding: .utf8) else { throw Failure.malformed("header encoding") }
        var lines = head.components(separatedBy: "\r\n")
        let statusLine = lines.removeFirst().split(separator: " ")
        guard statusLine.count >= 2, let status = Int(statusLine[1]) else { throw Failure.malformed("status line") }
        var headers: [String: String] = [:]
        for line in lines {
            guard let colon = line.firstIndex(of: ":") else { continue }
            headers[line[..<colon].lowercased()] = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
        }
        var body = Data(raw[split.upperBound...])
        if headers["transfer-encoding"]?.lowercased().contains("chunked") == true {
            body = try dechunk(body)
        }
        return Response(status: status, headers: headers, body: body)
    }

    static func dechunk(_ data: Data) throws -> Data {
        var out = Data()
        var i = data.startIndex
        let crlf = Data("\r\n".utf8)
        while i < data.endIndex {
            guard let lineEnd = data.range(of: crlf, in: i..<data.endIndex) else { throw Failure.malformed("chunk size") }
            let sizeText = String(decoding: data[i..<lineEnd.lowerBound], as: UTF8.self).split(separator: ";").first ?? ""
            guard let size = Int(sizeText.trimmingCharacters(in: .whitespaces), radix: 16) else { throw Failure.malformed("chunk size") }
            i = lineEnd.upperBound
            if size == 0 { break }
            guard data.distance(from: i, to: data.endIndex) >= size else { throw Failure.malformed("short chunk") }
            let end = data.index(i, offsetBy: size)
            out.append(data[i..<end])
            i = data.index(end, offsetBy: 2, limitedBy: data.endIndex) ?? data.endIndex
        }
        return out
    }
}
