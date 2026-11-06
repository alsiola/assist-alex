package assist

import (
	"encoding/json"
	"testing"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/storage"
)

// threadTx is a thread's record: its latest question, its Events and its
// attempts. Only what question reads is implemented.
type threadTx struct {
	storage.Tx
	q      Question
	events []domain.AcceptedEvent
	execs  []domain.Execution
}

func (t threadTx) Get(ns, _ string) ([]byte, error) {
	if ns == nsIndex {
		return nil, storage.ErrNotFound
	}
	return json.Marshal(t.q)
}

// List: nothing is indexed yet, so question reads the thread from the tail.
func (t threadTx) List(_, _ string) ([]storage.Entry, error) { return nil, nil }

func (t threadTx) Events(after uint64, _ int) ([]domain.AcceptedEvent, error) {
	var out []domain.AcceptedEvent
	for _, e := range t.events {
		if e.Sequence > after {
			out = append(out, e)
		}
	}
	return out, nil
}

func (t threadTx) Event(id domain.ID) (domain.AcceptedEvent, error) {
	for _, e := range t.events {
		if e.Event.ID == id {
			return e, nil
		}
	}
	return domain.AcceptedEvent{}, storage.ErrNotFound
}

func (t threadTx) ListExecutions(domain.ID) ([]domain.Execution, error) { return t.execs, nil }

// TestQuestionClosedByMessage: a message Alex writes after the question
// closes it, even when it reached the asking turn while it still ran and so
// started no attempt of its own. The agent's own messages and his messages
// from before the question don't.
func TestQuestionClosedByMessage(t *testing.T) {
	key := ThreadKey("assist", "abc")
	w := domain.Work{ID: WorkID("assist", "abc"), Kind: WorkKind, State: domain.WorkWaiting, ScopeID: "assist", CorrelationKeys: []domain.CorrelationKey{key}}
	q := Question{ID: "q1", Kind: AskQuestion, Text: "Send the photos?", Sequence: 1}
	ev := func(seq uint64, e domain.Event) domain.AcceptedEvent {
		e.ID = domain.ID("e" + string(rune('0'+seq)))
		return domain.AcceptedEvent{Event: e, Sequence: seq}
	}
	start := ev(1, domain.Event{Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaStart, Start{Text: "Sell it", Role: RoleOperator})})
	early := ev(2, domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "oh and"})})
	asked := ev(3, domain.Event{Type: TypeQuestion, WorkID: w.ID, Data: payload(SchemaQuestion, q)})
	said := ev(4, domain.Event{Type: TypeMessage, WorkID: w.ID, Data: payload(SchemaMessage, Message{Role: RoleAgent, Text: "Asked."})})
	wrote := ev(5, domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "These are the two I have"})})
	execs := []domain.Execution{{ID: "x1", WorkID: w.ID, Attempt: 1, TriggerID: start.Event.ID}}

	for name, c := range map[string]struct {
		events []domain.AcceptedEvent
		open   bool
	}{
		"just asked":              {[]domain.AcceptedEvent{start, early, asked, said}, true},
		"he wrote after it":       {[]domain.AcceptedEvent{start, early, asked, said, wrote}, false},
		"he wrote only before it": {[]domain.AcceptedEvent{start, early, asked}, true},
	} {
		_, open, err := question(threadTx{q: q, events: c.events, execs: execs}, w)
		if err != nil || open != c.open {
			t.Errorf("%s: open %v, want %v (%v)", name, open, c.open, err)
		}
	}
}
