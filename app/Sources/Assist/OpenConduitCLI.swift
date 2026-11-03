import Foundation

/// The operator commands the app runs as Alex: storing a secret in his
/// keychain (the value goes to stdin, never an argument) and restarting the
/// daemon so a newly installed extension loads.
enum OpenConduitCLI {
    struct Failure: Error, LocalizedError {
        var message: String
        var errorDescription: String? { message }
    }

    static var binary: String? {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let candidates = ["/opt/homebrew/bin/open-conduit", "/usr/local/bin/open-conduit", "\(home)/.local/bin/open-conduit", "\(home)/go/bin/open-conduit"]
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    @discardableResult
    static func run(_ args: [String], stdin: String? = nil) async throws -> String {
        guard let bin = binary else { throw Failure(message: "Can't find the open-conduit command.") }
        return try await withCheckedThrowingContinuation { cont in
            let p = Process()
            p.executableURL = URL(fileURLWithPath: bin)
            p.arguments = args
            let out = Pipe(), input = Pipe()
            p.standardOutput = out
            p.standardError = out
            p.standardInput = input
            p.terminationHandler = { proc in
                let text = String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
                if proc.terminationStatus == 0 { cont.resume(returning: text) }
                else { cont.resume(throwing: Failure(message: text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "open-conduit \(args.first ?? "") failed" : text)) }
            }
            do {
                try p.run()
                if let stdin { input.fileHandleForWriting.write(Data(stdin.utf8)) }
                try? input.fileHandleForWriting.close()
            } catch {
                cont.resume(throwing: error)
            }
        }
    }

    static func setSecret(name: String, value: String) async throws {
        try await run(["secret", "set", name], stdin: value)
    }

    static func restart() async throws {
        try await run(["service", "stop"])
        try await run(["service", "start"])
    }
}
