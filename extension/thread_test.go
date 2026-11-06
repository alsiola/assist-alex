package assist

import (
	"fmt"
	"strings"
	"testing"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/storage"
)

// logEvents is the Events of log after a sequence that keep holds, up to
// limit (0 for all): a store's read, in sequence order.
func logEvents(log []domain.AcceptedEvent, after uint64, limit int, keep func(domain.AcceptedEvent) bool) ([]domain.AcceptedEvent, error) {
	out := []domain.AcceptedEvent{}
	for _, e := range log {
		if e.Sequence > after && keep(e) {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// onWork is whether the host's read of work's Events holds e: it names the
// Work or was routed to it.
func onWork(e domain.AcceptedEvent, work domain.ID) bool {
	return e.Event.WorkID == work || e.RoutedWorkID == work
}

// logTx is a log read the way the host reads it, counting the Events each
// read hands back, so a test can bound what a thread read costs.
type logTx struct {
	storage.Tx
	log  []domain.AcceptedEvent
	read int
}

func (t *logTx) Events(after uint64, limit int) ([]domain.AcceptedEvent, error) {
	out, err := logEvents(t.log, after, limit, func(domain.AcceptedEvent) bool { return true })
	t.read += len(out)
	return out, err
}

func (t *logTx) WorkEvents(work domain.ID, after uint64, limit int) ([]domain.AcceptedEvent, error) {
	out, err := logEvents(t.log, after, limit, func(e domain.AcceptedEvent) bool { return onWork(e, work) })
	t.read += len(out)
	return out, err
}

// threadLog builds a log a sequence at a time.
type threadLog struct{ tx logTx }

func (l *threadLog) add(e domain.Event, routed domain.ID) uint64 {
	seq := uint64(len(l.tx.log) + 1)
	e.ID = domain.ID(fmt.Sprint("e", seq))
	l.tx.log = append(l.tx.log, domain.AcceptedEvent{Event: e, Sequence: seq, Routed: routed != "", RoutedWorkID: routed})
	return seq
}

func (l *threadLog) filler(n int) {
	for range n {
		l.add(domain.Event{Type: "github.issue.comment", Source: "github", WorkID: "github/other"}, "github/other")
	}
}

// TestThreadEvents: a thread's Events are those on its Work (emitted on it
// or routed to it) and Alex's input the route hasn't reached yet; another
// thread's, an Event the route wouldn't take to the thread, and other
// families' Events stay out. The read costs the thread's own Events and the
// short tail past its last routed one, not the log before it.
func TestThreadEvents(t *testing.T) {
	key := ThreadKey("assist", "abc")
	other := ThreadKey("assist", "def")
	w := WorkID("assist", "abc")
	if got, ok := keyWork(key); !ok || got != w {
		t.Fatalf("keyWork %q %v, want %q", got, ok, w)
	}
	l := &threadLog{}
	l.add(domain.Event{Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaStart, Start{Text: "Sell the bike", Role: RoleOperator})}, w)
	l.filler(1500)
	l.add(domain.Event{Type: TypeMessage, Source: Type, WorkID: w, Data: payload(SchemaMessage, Message{Role: RoleAgent, Text: "Listed it."})}, "")
	l.add(domain.Event{Type: "core.wait", Source: "core", WorkID: w}, "")
	l.add(domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &other, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "not this one"})}, WorkID("assist", "def"))
	l.add(domain.Event{Type: TypeMessage, Source: Type, CorrelationKey: &key, Data: payload(SchemaMessage, Message{Role: RoleAgent, Text: "stray"})}, "")
	l.add(domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "Offers?"})}, w)
	l.filler(700)
	// Not routed yet when the thread is read.
	l.add(domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "Take £200"})}, "")
	l.add(domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &other, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "nor this"})}, "")

	texts := func(after uint64) string {
		l.tx.read = 0
		got, err := threadEvents(&l.tx, w, after)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range Conversation(got) {
			out = append(out, m.Text)
		}
		return strings.Join(out, "|")
	}
	if got, want := texts(0), "Sell the bike|Listed it.|Offers?|Take £200"; got != want {
		t.Fatalf("thread: %q, want %q", got, want)
	}
	// Its own four, then the 702 after "Offers?": none of the 1,500 before.
	if l.tx.read > 4+702 {
		t.Errorf("read %d Events of a %d-Event log", l.tx.read, len(l.tx.log))
	}
	if got, want := texts(1), "Listed it.|Offers?|Take £200"; got != want {
		t.Fatalf("after the start: %q, want %q", got, want)
	}

	// Once routed, the input comes from the thread's Work and isn't read twice.
	l.tx.log[len(l.tx.log)-2].Routed, l.tx.log[len(l.tx.log)-2].RoutedWorkID = true, w
	if got, want := texts(0), "Sell the bike|Listed it.|Offers?|Take £200"; got != want {
		t.Fatalf("routed: %q, want %q", got, want)
	}
	if l.tx.read > 5+1 {
		t.Errorf("read %d Events once routed", l.tx.read)
	}
}

// TestThreadStates: each change's state counts the thread's decisions after
// its Since, routed or not yet, and leaves out other threads'.
func TestThreadStates(t *testing.T) {
	key := ThreadKey("assist", "abc")
	other := ThreadKey("assist", "def")
	w := domain.Work{ID: WorkID("assist", "abc"), Kind: WorkKind, ScopeID: "assist", CorrelationKeys: []domain.CorrelationKey{key}}
	l := &threadLog{}
	decide := func(k *domain.CorrelationKey, routed domain.ID, decision string, ids ...string) uint64 {
		return l.add(domain.Event{Type: TypeDecision, Source: extension.ActionSource, CorrelationKey: k,
			Data: payload(SchemaDecision, Decision{Decision: decision, Changes: ids})}, routed)
	}
	proposed := l.add(domain.Event{Type: TypeProposal, Source: Type, WorkID: w.ID}, "")
	since2 := decide(&key, w.ID, StateRejected, "c2") // not after c2's Since: doesn't count
	l.filler(1200)
	decide(&key, w.ID, StateApproved, "c1")
	l.filler(200)
	decide(&other, WorkID("assist", "def"), StateApproved, "c2") // another thread's
	decide(&key, "", StateRejected, "c3")                        // not routed yet

	cs := changeSet{Changes: []change{{ID: "c1", Since: proposed}, {ID: "c2", Since: since2}, {ID: "c3", Since: proposed}}}
	got, err := threadEvents(&l.tx, w.ID, proposed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("%d Events after the proposal, want 3: %v", len(got), got)
	}
	st, err := states(&l.tx, w, cs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"c1": StateApproved, "c2": StateProposed, "c3": StateRejected}
	for id, s := range want {
		if st[id] != s {
			t.Errorf("%s: %q, want %q", id, st[id], s)
		}
	}
}
