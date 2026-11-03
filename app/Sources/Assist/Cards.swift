import AppKit
import SwiftUI

/// What the assistant is waiting on Alex for, pinned above the composer.
struct QuestionCard: View {
    @Environment(Store.self) private var store
    let thread: ThreadState
    let question: QuestionBody
    @State private var answer = ""
    @State private var secret = ""

    private var busy: Bool { store.busy.contains("answer:\(thread.id)") }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(question.text)
                .font(.body.weight(.medium))
                .textSelection(.enabled)
            if let ctx = question.context, !ctx.isEmpty {
                Text(Markdown.render(ctx))
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
            switch question.askKind {
            case .secret: secretField
            case .action: actionButtons
            default: choiceButtons
            }
            if let deadline = question.deadline.flatMap(ISO8601.parse) {
                Text("Waits until \(deadline.formatted(.dateTime.weekday().day().month().hour().minute()))")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(nsColor: .underPageBackgroundColor).opacity(0.5))
        .disabled(busy)
    }

    @ViewBuilder private var choiceButtons: some View {
        let choices = question.choices ?? []
        if !choices.isEmpty {
            FlowRow(spacing: 8) {
                ForEach(Array(choices.enumerated()), id: \.offset) { i, c in
                    if i == 0 {
                        Button(c) { submit(c) }.buttonStyle(.borderedProminent)
                    } else {
                        Button(c) { submit(c) }.buttonStyle(.bordered)
                    }
                }
            }
        }
        HStack {
            TextField(choices.isEmpty ? "Your answer" : "Or answer in your own words", text: $answer, axis: .vertical)
                .lineLimit(1...5)
                .textFieldStyle(.roundedBorder)
                .onSubmit { submit(answer) }
            Button("Answer") { submit(answer) }
                .disabled(answer.trimmingCharacters(in: .whitespaces).isEmpty)
            if busy { ProgressView().controlSize(.small) }
        }
    }

    @ViewBuilder private var secretField: some View {
        HStack {
            SecureField("Paste it here; it goes straight to your keychain", text: $secret)
                .textFieldStyle(.roundedBorder)
                .onSubmit(storeSecret)
            Button("Store as \(question.secret_name ?? "")", action: storeSecret)
                .buttonStyle(.borderedProminent)
                .disabled(secret.isEmpty)
            if busy { ProgressView().controlSize(.small) }
        }
        Text("Stored with `open-conduit secret set \(question.secret_name ?? "")`. The assistant gets only the reference, never the value.")
            .font(.caption)
            .foregroundStyle(.secondary)
        HStack {
            TextField("Or reply instead", text: $answer)
                .textFieldStyle(.roundedBorder)
                .onSubmit { submit(answer) }
            Button("Reply") { submit(answer) }.disabled(answer.isEmpty)
        }
    }

    @ViewBuilder private var actionButtons: some View {
        HStack(spacing: 8) {
            if let s = question.url, let url = URL(string: s) {
                Button("Open link") { NSWorkspace.shared.open(url) }
            }
            Button("Done") { submit("Done") }.buttonStyle(.borderedProminent)
            Button("I can't") { submit("I can't do that right now") }
        }
        HStack {
            TextField("Add a note", text: $answer)
                .textFieldStyle(.roundedBorder)
                .onSubmit { submit(answer) }
            Button("Reply") { submit(answer) }.disabled(answer.isEmpty)
            if busy { ProgressView().controlSize(.small) }
        }
    }

    private func submit(_ text: String) {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        answer = ""
        Task { await store.answer(thread.id, t) }
    }

    private func storeSecret() {
        guard let name = question.secret_name, !secret.isEmpty else { return }
        let value = secret
        secret = ""
        Task { await store.storeSecret(thread.id, name: name, value: value) }
    }
}

/// The config changes the assistant proposed, for Alex to decide and apply.
struct ChangeSetCard: View {
    @Environment(Store.self) private var store
    let thread: ThreadState
    let candidate: ConfigCandidate
    @State private var expanded: Set<String> = []

    private var busy: Bool { store.busy.contains("decide:\(thread.id)") || store.busy.contains("apply:\(thread.id)") }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(candidate.open.isEmpty ? "Approved config changes" : "Config changes to review")
                    .font(.body.weight(.medium))
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                if let all = candidate.approve_all, candidate.open.count > 1 {
                    Button("Approve all \(candidate.open.count)") { Task { await store.decide(thread.id, action: all) } }
                }
                if candidate.readyToApply {
                    Button("Apply \(candidate.approved.count)") { Task { await store.apply(thread.id) } }
                        .buttonStyle(.borderedProminent)
                        .help("Writes the approved changes to Open Conduit's config and activates them")
                }
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(candidate.changes ?? []) { c in
                        if c.state == "proposed" || c.state == "approved" { changeRow(c) }
                    }
                }
            }
            .frame(maxHeight: 260)
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 14)
        .background(Color(nsColor: .underPageBackgroundColor).opacity(0.5))
        .disabled(busy)
    }

    private func changeRow(_ c: CandidateChange) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text("\(c.op) ").foregroundStyle(.secondary) + Text(c.pointer).font(.system(.body, design: .monospaced))
                Spacer()
                if c.state == "approved" {
                    Text("Approved").foregroundStyle(.secondary)
                } else {
                    if let r = c.reject { Button("Reject") { Task { await store.decide(thread.id, action: r) } } }
                    if let a = c.approve { Button("Approve") { Task { await store.decide(thread.id, action: a) } } }
                }
            }
            Text(c.why).foregroundStyle(.secondary)
            ForEach(c.problems ?? [], id: \.self) { p in
                Text(p).font(.caption).foregroundStyle(.red)
            }
            DisclosureGroup(isExpanded: Binding(get: { expanded.contains(c.id) }, set: { if $0 { expanded.insert(c.id) } else { expanded.remove(c.id) } })) {
                HStack(alignment: .top, spacing: 12) {
                    if let b = c.before { jsonBlock("Before", b) }
                    if let a = c.after { jsonBlock(c.before == nil ? "Value" : "After", a) }
                }
            } label: {
                Text("Show the change").font(.caption)
            }
        }
        .padding(10)
        .background(RoundedRectangle(cornerRadius: 8).strokeBorder(Color(nsColor: .separatorColor)))
    }

    private func jsonBlock(_ title: String, _ v: JSONValue) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            Text(v.pretty)
                .font(.system(.caption, design: .monospaced))
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// Lays buttons out left to right, wrapping when they run out of room.
struct FlowRow: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, row: CGFloat = 0, widest: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > 0 && x + size.width > width { y += row + spacing; x = 0; row = 0 }
            x += size.width + spacing
            row = max(row, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: min(widest, width), height: y + row)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, row: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > bounds.minX && x + size.width > bounds.maxX { y += row + spacing; x = bounds.minX; row = 0 }
            s.place(at: CGPoint(x: x, y: y), proposal: .unspecified)
            x += size.width + spacing
            row = max(row, size.height)
        }
    }
}
