package assist

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/extension/inproc"
	"github.com/open-conduit/open-conduit/storage"
)

// Agent-facing operations.
const (
	OpSay      = "assist.say"
	OpAsk      = "assist.ask"
	OpStatus   = "assist.status"
	OpSpawn    = "assist.spawn"
	OpPropose  = "assist.propose"
	OpWithdraw = "assist.withdraw"
	OpValidate = "assist.validate"
	OpRead     = "assist.read"
)

// Operations lists every tool operation (for binding operation lists).
func Operations() []string {
	return []string{OpSay, OpAsk, OpStatus, OpSpawn, OpRead, OpPropose, OpWithdraw, OpValidate}
}

// Ask timeouts. With no answer by the deadline the agent is resumed
// without one and decides whether to nudge, wait again or give up.
const (
	DefaultAskTimeout = 72 * time.Hour
	MaxAskTimeout     = 30 * 24 * time.Hour
)

// Input limits. An action's input is at most 4096 bytes, so Alex's
// messages are bounded by that; the agent may write more.
const (
	maxText    = 16 << 10
	maxContext = 4000
	maxChoices = 12
	maxChoice  = 200
	maxTitle   = 120
	maxStatus  = 300
)

// Options configure Tools.
type Options struct {
	Store  storage.Store
	Accept AcceptFunc
	// Enabled reports whether the scope has an `assist` entry in the
	// current configuration.
	Enabled func(scope domain.ID) bool
	Now     func() time.Time
	// Config reads, validates and edits config text for change sets.
	Config inproc.ConfigHost
}

// Tools implements the agent's operations.
type Tools struct{ o Options }

// NewTools validates options.
func NewTools(o Options) (*Tools, error) {
	if o.Store == nil || o.Accept == nil || o.Enabled == nil || o.Config == nil {
		return nil, storage.ErrInvalid
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Tools{o: o}, nil
}

type sayInput struct {
	Text string `json:"text" doc:"What to tell Alex, in Markdown. Lead with the point."`
	// Notify is how loudly it reaches him.
	Notify string `json:"notify,omitempty" doc:"quiet (default): in the thread when he looks. fyi: marks the thread unread. now: a notification that interrupts him."`
}

type askInput struct {
	Question   string   `json:"question" doc:"The question, request or thing to do, in one or two sentences."`
	Kind       string   `json:"kind,omitempty" doc:"question (default), approval (yes/no before you act), secret (a credential stored under secret_name), action (something he must do himself)."`
	Choices    []string `json:"choices,omitempty" doc:"Likely answers as buttons (2-12), most likely first. He can always write his own."`
	Context    string   `json:"context,omitempty" doc:"What he needs to decide well: options, costs, what you recommend and why."`
	SecretName string   `json:"secret_name,omitempty" doc:"For kind secret: the name to store it under, matching [a-z][a-z0-9_-]*."`
	URL        string   `json:"url,omitempty" doc:"A link he needs (a sign-in page, a form)."`
	Hours      int      `json:"timeout_hours,omitempty" doc:"How long to wait for him (default 72, at most 720)."`
}

type statusInput struct {
	Title  string `json:"title,omitempty" doc:"The thread's name, a few words."`
	Status string `json:"status,omitempty" doc:"Where the job stands, one line (\"Waiting on the garage to confirm Thursday\")."`
}

type spawnInput struct {
	Text   string `json:"text" doc:"Why you are opening the thread: what you found or need, in Markdown."`
	Title  string `json:"title,omitempty" doc:"The new thread's name."`
	Notify string `json:"notify,omitempty"`
}

// Registration is the toolbelt registration.
func (t *Tools) Registration() extension.Registration {
	return extension.Registration{Version: extension.ContractVersion, Name: Type, EventTypes: []string{TypeMessage, TypeStatus, TypeStart},
		Operations: []extension.Operation{
			{Name: OpSay, Emit: t.say, Input: extension.InputSchema[sayInput](),
				Summary:  "Tell Alex something in this thread, choosing how loudly.",
				Guidance: "Alex sees only what you send with `assist.say` and `assist.ask`; nothing you print reaches him. Default to `quiet`. Use `fyi` for a result he'll want to see today, and `now` only when waiting would cost him (a deadline, money, something broken). Never send progress chatter: update `assist.status` instead."},
			{Name: OpAsk, Wait: t.ask, Input: extension.InputSchema[askInput](),
				Summary:  "Ask Alex for a decision, approval, secret or action, and park the thread until he answers.",
				Guidance: "Batch what you need into one ask with likely answers as `choices`, and say what you recommend in `context`. Ask for an `approval` before anything irreversible or outward-facing (sending as him, paying, deleting, booking). Ask for a `secret` by name: his app stores it in the keychain and you get back the reference (`keyring:NAME`) to use in config; never ask him to paste a credential into a message. Make the ask your last toolbelt call: it parks the thread at once, and every toolbelt call after it this turn (`say`, `status`, `spawn`) is refused. Send those first, then ask and end your turn. His answer, or any message he sends, resumes you. A message closes the question too, so if it doesn't answer what you asked, ask again."},
			{Name: OpStatus, Emit: t.status, Input: extension.InputSchema[statusInput](),
				Summary:  "Name the thread and say in one line where the job stands.",
				Guidance: "Set a title early, and update the status whenever the job moves, so Alex can see where everything is without reading the thread."},
			{Name: OpSpawn, Emit: t.spawn, Input: extension.InputSchema[spawnInput](),
				Summary:  "Open a new thread with Alex, for something separate from this one.",
				Guidance: "Open a thread for a separate job or a proactive find (a scheduled check turned something up). The new thread gets its own thread, which starts from your text, so make it self-contained."},
			{Name: OpRead, Handler: t.read, Input: extension.InputSchema[readInput](),
				Summary: "Read Open Conduit's config outline, and in full what this thread's change set touches."},
			{Name: OpPropose, Handler: t.propose, Input: extension.InputSchema[proposeInput](),
				Summary:  "Propose one config change (a JSON-pointer edit) for Alex to approve and apply.",
				Guidance: "This is how you get new powers: an extension entry, a schedule, a binding grant, an operation, a connection. Propose small, explained steps; nothing changes until Alex applies, and `assist.applied` tells you when he has. Secrets go in `secret_refs` as references only."},
			{Name: OpWithdraw, Handler: t.withdraw, Input: extension.InputSchema[withdrawInput](),
				Summary: "Take back a change you proposed that Alex has not decided."},
			{Name: OpValidate, Handler: t.validate, Input: extension.InputSchema[struct{}](),
				Summary: "Validate the config with the approved changes applied."},
		}}
}

func refuse(code, format string, args ...any) error {
	return &extension.OperationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// thread checks that the call comes from a thread's Work in a scope with
// an `assist` entry.
func (t *Tools) thread(tx storage.Tx, a extension.Authority) (domain.Work, error) {
	w, err := tx.Work(a.WorkID)
	if err != nil || w.ScopeID != a.ScopeID {
		return w, extension.ErrUnauthorized
	}
	if !t.o.Enabled(a.ScopeID) {
		return w, refuse("not_enabled", "this scope has no assist entry")
	}
	if w.Kind != WorkKind {
		return w, refuse("not_a_thread", "this works only in an assist thread; open one with %s", OpSpawn)
	}
	return w, nil
}

func decode(data []byte, out any, shape string) error {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return refuse("invalid_input", "give %s", shape)
	}
	return nil
}

func text(s string, limit int, name string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", refuse("invalid_input", "%s is required", name)
	}
	if !utf8.ValidString(s) || len(s) > limit {
		return "", refuse("invalid_input", "%s must be text of at most %d bytes", name, limit)
	}
	return s, nil
}

func optional(s string, limit int, name string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	return text(s, limit, name)
}

func notifyLevel(s string) (string, error) {
	switch s {
	case "", NotifyQuiet:
		return NotifyQuiet, nil
	case NotifyFYI, NotifyNow:
		return s, nil
	}
	return "", refuse("invalid_input", "notify is quiet, fyi or now")
}

// requestID is a stable identity for the call: derived from the execution
// and the caller's request ID, so a retried call does nothing twice, else
// fresh.
func requestID(ctx context.Context, a extension.Authority) string {
	if req := extension.CallRequestID(ctx); req != "" {
		h := sha256.Sum256([]byte(string(a.ExecutionID) + "\x00" + req))
		return hex.EncodeToString(h[:16])
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (t *Tools) inThread(ctx context.Context, a extension.Authority) error {
	return t.o.Store.Transact(ctx, storage.Guard{Now: t.o.Now()}, func(tx storage.Tx) error {
		_, e := t.thread(tx, a)
		return e
	})
}

// say emits the agent's message on its own Work. It carries no key, so it
// is shown on the thread and never routes back to it.
func (t *Tools) say(ctx context.Context, a extension.Authority, in domain.Payload) (extension.EmitPlan, error) {
	var input sayInput
	if err := decode(in.Data, &input, "{text, notify?}"); err != nil {
		return extension.EmitPlan{}, err
	}
	msg, err := text(input.Text, maxText, "text")
	if err != nil {
		return extension.EmitPlan{}, err
	}
	level, err := notifyLevel(input.Notify)
	if err != nil {
		return extension.EmitPlan{}, err
	}
	if err = scanText(msg); err != nil {
		return extension.EmitPlan{}, err
	}
	if err = t.inThread(ctx, a); err != nil {
		return extension.EmitPlan{}, err
	}
	return extension.EmitPlan{
		Event: domain.Event{Type: TypeMessage, DeliveryID: "say/" + requestID(ctx, a), Data: payload(SchemaMessage, Message{Role: RoleAgent, Text: msg, Notify: level})},
		Commit: func(extension.Emitted) (extension.EmitCommit, error) {
			return extension.EmitCommit{Output: payload(SchemaTool, map[string]any{"said": true, "notify": level})}, nil
		},
	}, nil
}

// status records the thread's title and where it stands.
func (t *Tools) status(ctx context.Context, a extension.Authority, in domain.Payload) (extension.EmitPlan, error) {
	var input statusInput
	if err := decode(in.Data, &input, "{title?, status?}"); err != nil {
		return extension.EmitPlan{}, err
	}
	title, err := optional(input.Title, maxTitle, "title")
	if err != nil {
		return extension.EmitPlan{}, err
	}
	line, err := optional(input.Status, maxStatus, "status")
	if err != nil {
		return extension.EmitPlan{}, err
	}
	if title == "" && line == "" {
		return extension.EmitPlan{}, refuse("invalid_input", "give a title, a status or both")
	}
	if strings.ContainsAny(title+line, "\r\n") {
		return extension.EmitPlan{}, refuse("invalid_input", "title and status are one line each")
	}
	if err = scanText(title, line); err != nil {
		return extension.EmitPlan{}, err
	}
	if err = t.inThread(ctx, a); err != nil {
		return extension.EmitPlan{}, err
	}
	return extension.EmitPlan{
		Event: domain.Event{Type: TypeStatus, DeliveryID: "status/" + requestID(ctx, a), Data: payload(SchemaStatus, Status{Title: title, Status: line})},
		Commit: func(extension.Emitted) (extension.EmitCommit, error) {
			return extension.EmitCommit{Output: payload(SchemaTool, map[string]any{"updated": true})}, nil
		},
	}, nil
}

// spawn opens a new thread: an assist.start on a fresh key, which the
// route gives its own Work. It may be called from any Work in the scope,
// so a scheduled job can open a thread with what it found.
func (t *Tools) spawn(ctx context.Context, a extension.Authority, in domain.Payload) (extension.EmitPlan, error) {
	var input spawnInput
	if err := decode(in.Data, &input, "{text, title?, notify?}"); err != nil {
		return extension.EmitPlan{}, err
	}
	msg, err := text(input.Text, maxText, "text")
	if err != nil {
		return extension.EmitPlan{}, err
	}
	title, err := optional(input.Title, maxTitle, "title")
	if err != nil {
		return extension.EmitPlan{}, err
	}
	level, err := notifyLevel(input.Notify)
	if err != nil {
		return extension.EmitPlan{}, err
	}
	if err = scanText(msg, title); err != nil {
		return extension.EmitPlan{}, err
	}
	if !t.o.Enabled(a.ScopeID) {
		return extension.EmitPlan{}, refuse("not_enabled", "this scope has no assist entry")
	}
	id := requestID(ctx, a)[:16]
	key := ThreadKey(a.ScopeID, id)
	return extension.EmitPlan{
		Event: domain.Event{Type: TypeStart, DeliveryID: "spawn/" + id, CorrelationKey: &key,
			Data: payload(SchemaStart, Start{Text: msg, Title: title, Role: RoleAgent, Notify: level})},
		Commit: func(extension.Emitted) (extension.EmitCommit, error) {
			return extension.EmitCommit{Output: payload(SchemaTool, map[string]any{"opened": true, "work_id": string(WorkID(a.ScopeID, id))})}, nil
		},
	}, nil
}

func parseAsk(data []byte, now time.Time) (askInput, time.Time, error) {
	var in askInput
	if err := decode(data, &in, "{question, kind?, choices?, context?, secret_name?, url?, timeout_hours?}"); err != nil {
		return in, time.Time{}, err
	}
	var err error
	if in.Question, err = text(in.Question, maxText, "question"); err != nil {
		return in, time.Time{}, err
	}
	if in.Kind == "" {
		in.Kind = AskQuestion
	}
	if !slices.Contains(askKinds, in.Kind) {
		return in, time.Time{}, refuse("invalid_input", "kind is one of %s", strings.Join(askKinds, ", "))
	}
	in.Context = strings.TrimSpace(in.Context)
	if !utf8.ValidString(in.Context) || len(in.Context) > maxContext {
		return in, time.Time{}, refuse("invalid_input", "context must be text of at most %d bytes", maxContext)
	}
	if in.Kind == AskSecret {
		if !secretName.MatchString(in.SecretName) {
			return in, time.Time{}, refuse("invalid_input", "a secret ask needs secret_name matching [a-z][a-z0-9_-]*")
		}
		if len(in.Choices) > 0 {
			return in, time.Time{}, refuse("invalid_input", "a secret ask takes no choices")
		}
	} else if in.SecretName != "" {
		return in, time.Time{}, refuse("invalid_input", "secret_name is only for kind secret")
	}
	if in.URL != "" {
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(in.URL) > 2000 {
			return in, time.Time{}, refuse("invalid_input", "url must be an http(s) link")
		}
	}
	if in.Kind == AskApproval && len(in.Choices) == 0 {
		in.Choices = []string{"Approve", "Decline"}
	}
	if len(in.Choices) == 1 || len(in.Choices) > maxChoices {
		return in, time.Time{}, refuse("invalid_input", "give between 2 and %d choices, or none for a free answer", maxChoices)
	}
	seen := map[string]bool{}
	for i, c := range in.Choices {
		c = strings.TrimSpace(c)
		if c == "" || !utf8.ValidString(c) || len(c) > maxChoice || strings.ContainsAny(c, "\r\n") {
			return in, time.Time{}, refuse("invalid_input", "each choice must be one line of at most %d bytes", maxChoice)
		}
		if seen[strings.ToLower(c)] {
			return in, time.Time{}, refuse("invalid_input", "choice %q is given twice", c)
		}
		seen[strings.ToLower(c)] = true
		in.Choices[i] = c
	}
	timeout := DefaultAskTimeout
	if in.Hours < 0 || time.Duration(in.Hours)*time.Hour > MaxAskTimeout {
		return in, time.Time{}, refuse("invalid_input", "timeout_hours is 1 to %d", int(MaxAskTimeout/time.Hour))
	}
	if in.Hours > 0 {
		timeout = time.Duration(in.Hours) * time.Hour
	}
	return in, now.Add(timeout), nil
}

// ask records the question on the thread as assist.question and parks the
// thread until Alex answers it, writes to it, or the deadline passes.
func (t *Tools) ask(ctx context.Context, a extension.Authority, in domain.Payload) (extension.WaitPlan, error) {
	now := t.o.Now()
	input, deadline, err := parseAsk(in.Data, now)
	if err != nil {
		return extension.WaitPlan{}, err
	}
	if err = scanText(append([]string{input.Question, input.Context}, input.Choices...)...); err != nil {
		return extension.WaitPlan{}, err
	}
	id := requestID(ctx, a)
	err = t.o.Store.Transact(ctx, storage.Guard{Now: now}, func(tx storage.Tx) error {
		w, err := t.thread(tx, a)
		if err != nil {
			return err
		}
		if _, ok := threadKey(w); !ok {
			return refuse("not_a_thread", "this thread has no key to wait on")
		}
		if old, err := latestQuestion(tx, w.ID); err == nil && old.ID == id {
			return nil
		}
		seq, err := tx.LatestSequence()
		if err != nil {
			return err
		}
		q := Question{ID: id, Kind: input.Kind, Text: input.Question, Context: input.Context, Choices: input.Choices,
			SecretName: input.SecretName, URL: input.URL, Deadline: deadline.UTC().Format(time.RFC3339), Sequence: seq}
		if err = t.o.Accept(tx, a.ScopeID, a.TriggerID, domain.Event{Type: TypeQuestion, Source: Type, DeliveryID: "question/" + id,
			OccurredAt: now, ReceivedAt: now, WorkID: a.WorkID, Data: payload(SchemaQuestion, q)}); err != nil {
			return err
		}
		b, err := json.Marshal(q)
		if err != nil {
			return err
		}
		return tx.Put(Namespace, "question/"+string(w.ID), b)
	})
	if err != nil {
		return extension.WaitPlan{}, err
	}
	what := "Alex's answer"
	if input.Kind != AskQuestion {
		what = "Alex's " + input.Kind
	}
	return extension.WaitPlan{
		Wait: extension.WaitRequest{Match: domain.EventMatch{Types: []string{TypeAnswer, TypeMessage}}, Timeout: &deadline, Description: what},
		Output: payload(SchemaTool, map[string]any{"asked": true, "deadline": deadline.Format(time.RFC3339),
			"next": "end your turn now: the thread is parked, and every further toolbelt call this turn is refused. A message he sends before the turn ends can't be answered until you are resumed. You are resumed with his answer or message, or at the deadline without one"}),
	}, nil
}

// latestQuestion is the thread's latest question (storage.ErrNotFound
// when it has asked none).
func latestQuestion(tx storage.Tx, work domain.ID) (Question, error) {
	var q Question
	b, err := tx.Get(Namespace, "question/"+string(work))
	if err != nil {
		return q, err
	}
	return q, json.Unmarshal(b, &q)
}
