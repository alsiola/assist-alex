import SwiftUI

struct ContentView: View {
    @Environment(Store.self) private var store

    var body: some View {
        @Bindable var store = store
        NavigationSplitView {
            ThreadList()
                .navigationSplitViewColumnWidth(min: 240, ideal: 290, max: 380)
        } detail: {
            switch store.connection {
            case .notConfigured:
                NotConfiguredView()
            case .unreachable(let message) where store.threads.isEmpty:
                UnreachableView(message: message)
            default:
                if let id = store.selection, let thread = store.threads[id] {
                    ThreadView(thread: thread)
                        .id(id)
                } else {
                    NewThreadView()
                }
            }
        }
        .navigationTitle("")
    }
}

// MARK: - Thread list

struct ThreadList: View {
    @Environment(Store.self) private var store
    @State private var showClosed = false

    var body: some View {
        @Bindable var store = store
        List(selection: $store.selection) {
            if !store.needsYou.isEmpty {
                Section("Needs you") {
                    ForEach(store.needsYou) { ThreadRow(thread: $0).tag(Optional($0.id)) }
                }
            }
            Section(store.needsYou.isEmpty ? "Threads" : "Everything else") {
                if store.active.isEmpty && store.needsYou.isEmpty {
                    Text("Nothing on the go")
                        .foregroundStyle(.secondary)
                }
                ForEach(store.active) { ThreadRow(thread: $0).tag(Optional($0.id)) }
            }
            if !store.closed.isEmpty {
                Section(isExpanded: $showClosed) {
                    ForEach(store.closed.prefix(50)) { ThreadRow(thread: $0).tag(Optional($0.id)) }
                } header: {
                    Text("Closed")
                }
            }
        }
        .listStyle(.sidebar)
        .safeAreaInset(edge: .bottom) { ConnectionFooter() }
        .toolbar {
            ToolbarItem {
                Button { store.selection = nil } label: { Label("New Thread", systemImage: "square.and.pencil") }
                    .help("New thread (⌘N)")
            }
        }
    }
}

struct ThreadRow: View {
    @Environment(Store.self) private var store
    let thread: ThreadState

    var body: some View {
        let unread = store.isUnread(thread)
        HStack(alignment: .top, spacing: 8) {
            Circle()
                .fill(unread ? Color.accentColor : .clear)
                .frame(width: 7, height: 7)
                .padding(.top, 6)
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline) {
                    Text(thread.title)
                        .fontWeight(unread || thread.needsYou ? .semibold : .regular)
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    if thread.isRunning {
                        ProgressView().controlSize(.mini)
                    } else {
                        Text(thread.lastDate, format: .relative(presentation: .numeric, unitsStyle: .narrow))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                if let need = thread.needLabel {
                    Text(need)
                        .font(.callout)
                        .foregroundStyle(.orange)
                        .lineLimit(1)
                } else if let line = thread.isRunning ? (thread.activity ?? thread.statusLine) : (thread.statusLine ?? thread.preview) {
                    Text(line)
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
        }
        .padding(.vertical, 3)
        .contextMenu {
            if thread.work.isOpen {
                Button("Archive") { Task { await store.archive(thread.id) } }
            }
        }
    }
}

struct ConnectionFooter: View {
    @Environment(Store.self) private var store

    var body: some View {
        HStack(spacing: 6) {
            switch store.connection {
            case .connected:
                Circle().fill(.green).frame(width: 6, height: 6)
                Text("Connected").foregroundStyle(.secondary)
            case .connecting:
                ProgressView().controlSize(.mini)
                Text("Connecting").foregroundStyle(.secondary)
            case .notConfigured:
                Circle().fill(.orange).frame(width: 6, height: 6)
                Text("Not set up").foregroundStyle(.secondary)
            case .unreachable:
                Circle().fill(.red).frame(width: 6, height: 6)
                Text("Open Conduit isn't running").foregroundStyle(.secondary)
            }
            Spacer()
        }
        .font(.caption)
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
    }
}

// MARK: - A thread

struct ThreadView: View {
    @Environment(Store.self) private var store
    let thread: ThreadState

    var body: some View {
        VStack(spacing: 0) {
            ThreadHeader(thread: thread)
            Divider()
            Transcript(thread: thread)
            if thread.work.isOpen {
                if let q = thread.openQuestion {
                    Divider()
                    QuestionCard(thread: thread, question: q)
                }
                if !thread.openChanges.isEmpty || thread.readyToApply, let c = thread.candidate {
                    Divider()
                    ChangeSetCard(thread: thread, candidate: c)
                }
                if let err = store.lastError {
                    ErrorLine(message: err)
                }
                Divider()
                Composer(placeholder: thread.openQuestion == nil ? "Message" : "Or write something else", key: "send:\(thread.id)") { text, files in
                    await store.send(text, to: thread.id, attachments: files)
                }
            } else {
                Divider()
                Text(thread.work.state == "completed" ? "This thread is finished." : "This thread was \(thread.work.state).")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity)
                    .padding(12)
            }
        }
        .onAppear { store.markSeen(thread.id) }
        .onChange(of: thread.lastSeq) { store.markSeen(thread.id) }
    }
}

struct ThreadHeader: View {
    @Environment(Store.self) private var store
    let thread: ThreadState

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            VStack(alignment: .leading, spacing: 3) {
                Text(thread.title)
                    .font(.title3.weight(.semibold))
                    .lineLimit(1)
                HStack(spacing: 6) {
                    if thread.isRunning {
                        ProgressView().controlSize(.mini)
                        Text(thread.activity ?? "Working")
                    } else if let line = thread.statusLine {
                        Text(line)
                    } else {
                        Text(stateText)
                    }
                }
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            }
            Spacer()
            if thread.work.isOpen {
                Button("Archive") { Task { await store.archive(thread.id) } }
                    .help("Close this thread; the assistant stops working on it")
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
    }

    private var stateText: String {
        switch thread.work.state {
        case "waiting": return "Waiting"
        case "ready": return "Idle"
        case "running": return "Working"
        default: return thread.work.state.capitalized
        }
    }
}

struct Transcript: View {
    let thread: ThreadState

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 10) {
                    let items = thread.transcript
                    ForEach(Array(items.enumerated()), id: \.element.id) { i, item in
                        if i == 0 || item.date.timeIntervalSince(items[i - 1].date) > 3600 {
                            Text(item.date, format: .dateTime.weekday(.wide).day().month().hour().minute())
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .frame(maxWidth: .infinity)
                                .padding(.top, 8)
                        }
                        TranscriptRow(item: item)
                    }
                    if thread.isRunning {
                        TypingRow(activity: thread.activity)
                    }
                    Color.clear.frame(height: 1).id("bottom")
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 14)
            }
            .onAppear { proxy.scrollTo("bottom", anchor: .bottom) }
            .onChange(of: thread.lastSeq) { withAnimation(.easeOut(duration: 0.2)) { proxy.scrollTo("bottom", anchor: .bottom) } }
            .onChange(of: thread.isRunning) { withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } }
        }
    }
}

struct TranscriptRow: View {
    let item: TranscriptItem

    var body: some View {
        switch item.kind {
        case .alex(let text):
            let (body, files) = Attachments.split(text)
            HStack {
                Spacer(minLength: 80)
                VStack(alignment: .trailing, spacing: 6) {
                    ForEach(files, id: \.self) { AttachmentView(url: $0) }
                    if !body.isEmpty { Bubble(text: body, mine: true) }
                }
            }
        case .assistant(let text, _):
            HStack {
                Bubble(text: text, mine: false)
                Spacer(minLength: 80)
            }
        case .question(let q, let answer):
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text(label(q))
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.secondary)
                    Bubble(text: q.text + (q.context.map { "\n\n" + $0 } ?? "") + (q.url.map { "\n\n" + $0 } ?? ""), mine: false)
                    if answer == nil, q.askKind == .secret {
                        Text("Waiting for a credential named \(q.secret_name ?? "")")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 80)
            }
        case .note(let text):
            Text(text)
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity)
                .multilineTextAlignment(.center)
        }
    }

    private func label(_ q: QuestionBody) -> String {
        switch q.askKind {
        case .approval: return "Asks for your approval"
        case .secret: return "Needs a credential"
        case .action: return "Needs you to do something"
        case .question: return "Asks"
        }
    }
}

struct Bubble: View {
    let text: String
    let mine: Bool

    var body: some View {
        Text(Markdown.render(text))
            .textSelection(.enabled)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .foregroundStyle(mine ? Color.white : Color.primary)
            .background(
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .fill(mine ? Color.accentColor : Color(nsColor: .controlBackgroundColor))
            )
            .overlay(
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .strokeBorder(mine ? Color.clear : Color(nsColor: .separatorColor), lineWidth: 0.5)
            )
    }
}

struct TypingRow: View {
    let activity: String?
    @State private var phase = 0.0

    var body: some View {
        HStack(spacing: 8) {
            HStack(spacing: 4) {
                ForEach(0..<3) { i in
                    Circle()
                        .fill(Color.secondary)
                        .frame(width: 6, height: 6)
                        .opacity(0.35 + 0.65 * max(0, sin(phase + Double(i) * 0.9)))
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(RoundedRectangle(cornerRadius: 14, style: .continuous).fill(Color(nsColor: .controlBackgroundColor)))
            if let activity {
                Text(activity)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .onAppear {
            withAnimation(.linear(duration: 1.2).repeatForever(autoreverses: false)) { phase = .pi * 2 }
        }
    }
}

struct ErrorLine: View {
    @Environment(Store.self) private var store
    let message: String

    var body: some View {
        HStack {
            Image(systemName: "exclamationmark.triangle")
            Text(message).lineLimit(3)
            Spacer()
            Button("Dismiss") { store.lastError = nil }
                .buttonStyle(.borderless)
        }
        .font(.callout)
        .foregroundStyle(.red)
        .padding(.horizontal, 20)
        .padding(.vertical, 8)
    }
}

// MARK: - Composer

struct Composer: View {
    @Environment(Store.self) private var store
    let placeholder: String
    let key: String
    let send: (String, [URL]) async -> Void
    @State private var text = ""
    @State private var files: [URL] = []
    @State private var picking = false
    @State private var dropping = false
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !files.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(files, id: \.self) { f in AttachmentChip(url: f) { files.removeAll { $0 == f } } }
                    }
                }
            }
            HStack(alignment: .bottom, spacing: 10) {
                Button { picking = true } label: { Image(systemName: "paperclip") }
                    .buttonStyle(.borderless)
                    .help("Attach pictures or files")
                    .padding(.bottom, 8)
                TextField(placeholder, text: $text, axis: .vertical)
                    .textFieldStyle(.plain)
                    .lineLimit(1...10)
                    .focused($focused)
                    .onSubmit(submit)
                    .onPasteCommand(of: [.fileURL, .png, .tiff, .jpeg], perform: paste)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(RoundedRectangle(cornerRadius: 18, style: .continuous)
                        .strokeBorder(dropping ? Color.accentColor : Color(nsColor: .separatorColor), lineWidth: dropping ? 2 : 1))
                Button(action: submit) {
                    if store.busy.contains(key) { ProgressView().controlSize(.small) } else { Image(systemName: "arrow.up") }
                }
                .buttonStyle(.borderedProminent)
                .clipShape(Circle())
                .disabled(empty || store.busy.contains(key))
                .keyboardShortcut(.return, modifiers: .command)
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
        .onAppear { focused = true }
        .onDrop(of: [.fileURL], isTargeted: $dropping) { providers in
            for p in providers {
                _ = p.loadObject(ofClass: URL.self) { url, _ in
                    if let url { DispatchQueue.main.async { files.append(url) } }
                }
            }
            return true
        }
        .fileImporter(isPresented: $picking, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            if case .success(let urls) = result { files += urls }
        }
    }

    private var empty: Bool { text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && files.isEmpty }

    /// A pasted image becomes a PNG file to attach; a pasted file is attached.
    private func paste(_ providers: [NSItemProvider]) {
        for p in providers {
            if p.hasItemConformingToTypeIdentifier("public.file-url") {
                _ = p.loadObject(ofClass: URL.self) { url, _ in if let url { DispatchQueue.main.async { files.append(url) } } }
            } else if p.canLoadObject(ofClass: NSImage.self) {
                _ = p.loadObject(ofClass: NSImage.self) { obj, _ in
                    guard let img = obj as? NSImage, let tiff = img.tiffRepresentation,
                          let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]) else { return }
                    let url = FileManager.default.temporaryDirectory.appendingPathComponent("Pasted \(Int(Date().timeIntervalSince1970)).png")
                    try? png.write(to: url)
                    DispatchQueue.main.async { files.append(url) }
                }
            }
        }
    }

    private func submit() {
        guard !empty else { return }
        let t = text, f = files
        text = ""
        files = []
        Task { await send(t, f) }
    }
}

struct AttachmentChip: View {
    let url: URL
    let remove: () -> Void

    var body: some View {
        HStack(spacing: 6) {
            if let img = NSImage(contentsOf: url), ["png", "jpg", "jpeg", "heic", "gif", "tiff", "webp"].contains(url.pathExtension.lowercased()) {
                Image(nsImage: img).resizable().scaledToFill().frame(width: 36, height: 36).clipShape(RoundedRectangle(cornerRadius: 6))
            } else {
                Image(systemName: "doc").frame(width: 36, height: 36)
            }
            Text(url.lastPathComponent).lineLimit(1).frame(maxWidth: 160, alignment: .leading)
            Button(action: remove) { Image(systemName: "xmark") }.buttonStyle(.borderless)
        }
        .font(.callout)
        .padding(6)
        .background(RoundedRectangle(cornerRadius: 8).strokeBorder(Color(nsColor: .separatorColor)))
    }
}

// MARK: - New thread and setup

struct NewThreadView: View {
    @Environment(Store.self) private var store

    var body: some View {
        VStack(spacing: 0) {
            Spacer()
            VStack(spacing: 8) {
                Text("What do you need?")
                    .font(.largeTitle.weight(.semibold))
                Text("Describe the job. It works on it in the background and gets back to you only when it needs you or it's done.")
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 440)
            }
            .padding(.bottom, 24)
            if let err = store.lastError { ErrorLine(message: err) }
            Composer(placeholder: "Find me a dentist near the office who can see me this week…", key: "send:new") { text, files in
                await store.send(text, to: nil, attachments: files)
            }
            .frame(maxWidth: 640)
            Spacer()
            Spacer()
        }
        .padding()
    }
}

struct NotConfiguredView: View {
    @Environment(Store.self) private var store

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Assist isn't set up in Open Conduit yet")
                .font(.title2.weight(.semibold))
            Text("The daemon is running, but it has no `\(store.scope)` scope. Install the extension and its scope, then restart Open Conduit:")
            Text("cd ~/code/assist-alex && make install")
                .font(.system(.body, design: .monospaced))
                .textSelection(.enabled)
                .padding(10)
                .background(RoundedRectangle(cornerRadius: 8).fill(Color(nsColor: .textBackgroundColor)))
        }
        .frame(maxWidth: 520)
        .padding(40)
    }
}

struct UnreachableView: View {
    let message: String

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Can't reach Open Conduit")
                .font(.title2.weight(.semibold))
            Text(message).foregroundStyle(.secondary)
            Text("Start it with `open-conduit service start`, or run `open-conduit serve`.")
        }
        .frame(maxWidth: 520)
        .padding(40)
    }
}

/// A message's attached files: the lines the app appends when it stages
/// them in the assistant's inbox.
enum Attachments {
    static let marker = "\n\nAttached:\n"

    static func split(_ text: String) -> (String, [URL]) {
        guard let r = text.range(of: marker) else { return (text, []) }
        let files = text[r.upperBound...].split(separator: "\n").compactMap { line -> URL? in
            guard line.hasPrefix("- /") else { return nil }
            return URL(fileURLWithPath: String(line.dropFirst(2)))
        }
        return (String(text[..<r.lowerBound]), files)
    }
}

struct AttachmentView: View {
    let url: URL

    var body: some View {
        if let img = NSImage(contentsOf: url), img.size.width > 0, ["png", "jpg", "jpeg", "heic", "gif", "tiff", "webp"].contains(url.pathExtension.lowercased()) {
            Image(nsImage: img)
                .resizable()
                .scaledToFit()
                .frame(maxWidth: 280, maxHeight: 280)
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                .onTapGesture(count: 2) { NSWorkspace.shared.open(url) }
                .help(url.lastPathComponent)
        } else {
            Button { NSWorkspace.shared.activateFileViewerSelecting([url]) } label: {
                Label(url.lastPathComponent, systemImage: "doc")
            }
            .buttonStyle(.bordered)
        }
    }
}

enum Markdown {
    static func render(_ s: String) -> AttributedString {
        (try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(s)
    }
}
