package assist

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/extension/inproc"
	"github.com/open-conduit/open-conduit/storage"
)

// Action IDs.
const (
	ActionStart   = "start"
	ActionMessage = "message"
	ActionAnswer  = "answer"
)

// maxInput is the most an action's input may hold (the host's limit).
const maxInput = 4096

// Actions is an `assist` entry's extension.ActionProvider. On the scope it
// offers "start". On an open thread's Work it offers "message", and
// "answer" while the thread waits on its question. Once the agent proposes
// changes it also offers approve and reject for each, Approve all and
// validate (decide.go). Archiving a thread is cancelling its Work.
type Actions struct {
	Store storage.Store
	Now   func() time.Time
	// Config validates the change set's candidate.
	Config inproc.ConfigHost
}

var (
	_ extension.ActionProvider = Actions{}
	_ extension.PanelProvider  = Actions{}
)

func (a Actions) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a Actions) transact(ctx context.Context, fn func(storage.Tx) error) error {
	return a.Store.Transact(ctx, storage.Guard{Now: a.now()}, fn)
}

// question is the thread's question when Alex can answer it now: the
// thread is parked and its latest question is the last thing that parked
// it. A package can't read Waits, so this is read from the record: the
// thread is waiting, its question was asked, no answer to it has been
// accepted, and no attempt has started since it was asked (the asking
// attempt ends on the wait; a later one was started by something else,
// such as his message).
func question(tx storage.Tx, w domain.Work) (Question, bool, error) {
	if w.Kind != WorkKind || w.State != domain.WorkWaiting {
		return Question{}, false, nil
	}
	q, err := latestQuestion(tx, w.ID)
	if errors.Is(err, storage.ErrNotFound) {
		return q, false, nil
	}
	if err != nil {
		return q, false, err
	}
	key, _ := threadKey(w)
	after, err := tx.Events(q.Sequence, 0)
	if err != nil {
		return q, false, err
	}
	var asked uint64
	for _, e := range after {
		var p Answer
		if e.Event.Type == TypeAnswer && e.Event.CorrelationKey != nil && *e.Event.CorrelationKey == key &&
			json.Unmarshal(e.Event.Data.Data, &p) == nil && p.Question == q.ID {
			return q, false, nil
		}
		var x Question
		if asked == 0 && e.Event.Type == TypeQuestion && e.Event.WorkID == w.ID && json.Unmarshal(e.Event.Data.Data, &x) == nil && x.ID == q.ID {
			asked = e.Sequence
		}
	}
	if asked == 0 {
		return q, false, nil
	}
	if trigger, err := lastTrigger(tx, w.ID); err == nil {
		if t, err := tx.Event(trigger); err == nil && t.Sequence > asked {
			return q, false, nil
		}
	}
	return q, true, nil
}

func (a Actions) Actions(ctx context.Context, t extension.ActionTarget) ([]extension.Action, error) {
	if t.Work == nil {
		return []extension.Action{{ID: ActionStart, Label: "Start a thread", Prompt: "what you want done"}}, nil
	}
	if t.Work.Kind != WorkKind || t.Work.State.Terminal() {
		return nil, nil
	}
	var out []extension.Action
	err := a.transact(ctx, func(tx storage.Tx) error {
		out = append(out, extension.Action{ID: ActionMessage, Label: "Send a message", Prompt: "your message"})
		cs, st, err := threadChanges(tx, *t.Work)
		if err != nil {
			return err
		}
		out = append(out, changeActions(cs, st)...)
		q, ok, err := question(tx, *t.Work)
		if err != nil || !ok {
			return err
		}
		prompt := "your answer"
		switch {
		case q.Kind == AskSecret:
			prompt = "the reference it is stored under"
		case len(q.Choices) > 0:
			prompt = "a choice (" + strings.Join(q.Choices, ", ") + ") or your own answer"
		}
		if len(prompt) > 200 {
			prompt = "a choice or your own answer"
		}
		detail := q.Text
		if q.Context != "" {
			detail += "\n\n" + q.Context
		}
		out = append(out, extension.Action{ID: ActionAnswer, Label: "Answer", Prompt: prompt, Choices: q.Choices, Detail: cut3000(detail)})
		return nil
	})
	return out, err
}

func refuseAction(code, message string) (extension.ActionPlan, error) {
	return extension.ActionPlan{}, &extension.OperationError{Code: code, Message: message}
}

func (a Actions) Invoke(ctx context.Context, c extension.ActionCall) (extension.ActionPlan, error) {
	if c.Caller.Kind != extension.CallerOperator {
		return refuseAction("not_allowed", "only Alex drives his threads")
	}
	switch {
	case c.Action == ActionStart && c.Target.Work == nil:
		return a.start(c)
	case c.Action == ActionMessage && c.Target.Work != nil:
		return a.message(c)
	case c.Action == ActionAnswer && c.Target.Work != nil:
		return a.answer(ctx, c)
	case c.Target.Work != nil && c.Target.Work.Kind == WorkKind && !c.Target.Work.State.Terminal():
		if plan, err, ok := a.decide(ctx, c); ok {
			return plan, err
		}
	}
	return refuseAction("unknown_action", "assist offers no action "+c.Action+" here")
}

func (a Actions) start(c extension.ActionCall) (extension.ActionPlan, error) {
	msg := strings.TrimSpace(c.Input)
	if msg == "" || len(msg) > maxInput {
		return refuseAction("invalid_input", "say what you want done, in at most 4096 bytes")
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	key := ThreadKey(c.Target.Scope.ID, hex.EncodeToString(b[:]))
	return extension.ActionPlan{Event: domain.Event{Type: TypeStart, CorrelationKey: &key,
		Data: payload(SchemaStart, Start{Text: msg, Role: RoleOperator})}, Message: "started"}, nil
}

func (a Actions) message(c extension.ActionCall) (extension.ActionPlan, error) {
	w := *c.Target.Work
	key, ok := threadKey(w)
	if w.Kind != WorkKind || w.State.Terminal() || !ok {
		return refuseAction("not_a_thread", "this Work is not an open assist thread")
	}
	msg := strings.TrimSpace(c.Input)
	if msg == "" || len(msg) > maxInput {
		return refuseAction("invalid_input", "give a message of at most 4096 bytes")
	}
	return extension.ActionPlan{Event: domain.Event{Type: TypeMessage, CorrelationKey: &key,
		Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: msg})}, Message: "sent"}, nil
}

func (a Actions) answer(ctx context.Context, c extension.ActionCall) (extension.ActionPlan, error) {
	w := *c.Target.Work
	var q Question
	var ok bool
	if err := a.transact(ctx, func(tx storage.Tx) error {
		var e error
		q, ok, e = question(tx, w)
		return e
	}); err != nil {
		return extension.ActionPlan{}, err
	}
	if !ok {
		return refuseAction("not_outstanding", "the thread has no open question to answer")
	}
	// Choices are suggestions: one picked by its text or number is given
	// as written, and anything else is Alex's own answer.
	answer := strings.TrimSpace(c.Input)
	if picked, ok := choose(q.Choices, answer); ok {
		answer = picked
	}
	if answer == "" || len(answer) > maxInput {
		return refuseAction("invalid_input", "give an answer of at most 4096 bytes")
	}
	if err := scanText(answer); err != nil {
		// A credential typed into an answer would reach the agent: the app
		// stores secrets itself and answers with the reference.
		return refuseAction("looks_like_secret", "that looks like a credential; store it as a secret and answer with its reference")
	}
	key, _ := threadKey(w)
	return extension.ActionPlan{Event: domain.Event{Type: TypeAnswer, CorrelationKey: &key,
		Data: payload(SchemaAnswer, Answer{Question: q.ID, Answer: answer})}, Message: "answered"}, nil
}

// choose matches input against choices: a choice's text (case-insensitive)
// or its number from 1.
func choose(choices []string, input string) (string, bool) {
	input = strings.TrimSpace(input)
	if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= len(choices) {
		return choices[n-1], true
	}
	i := slices.IndexFunc(choices, func(c string) bool { return strings.EqualFold(c, input) })
	if i < 0 {
		return "", false
	}
	return choices[i], true
}

// cut3000 keeps an action's detail within its limit.
func cut3000(s string) string {
	if len(s) <= 3000 {
		return s
	}
	keep := 2990
	for keep > 0 && !utf8Start(s[keep]) {
		keep--
	}
	return s[:keep] + " …"
}
