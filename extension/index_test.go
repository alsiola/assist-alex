package assist

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/storage"
)

// memStore is a log and a KV, enough for the index: each transaction sees
// the log as it is and commits its KV writes at the end.
type memStore struct {
	log []domain.AcceptedEvent
	kv  map[string][]byte
}

type memTx struct {
	storage.Tx
	s      *memStore
	writes map[string][]byte
}

func (s *memStore) Transact(_ context.Context, _ storage.Guard, fn func(storage.Tx) error) error {
	tx := &memTx{s: s, writes: map[string][]byte{}}
	if err := fn(tx); err != nil {
		return err
	}
	for k, v := range tx.writes {
		s.kv[k] = v
	}
	return nil
}

func (t *memTx) Get(ns, key string) ([]byte, error) {
	if v, ok := t.writes[ns+"\x00"+key]; ok {
		return v, nil
	}
	if v, ok := t.s.kv[ns+"\x00"+key]; ok {
		return v, nil
	}
	return nil, storage.ErrNotFound
}

func (t *memTx) Put(ns, key string, v []byte) error {
	t.writes[ns+"\x00"+key] = v
	return nil
}

func (t *memTx) List(ns, prefix string) ([]storage.Entry, error) {
	var out []storage.Entry
	for k, v := range t.s.kv {
		if n, key, _ := strings.Cut(k, "\x00"); n == ns && strings.HasPrefix(key, prefix) {
			out = append(out, storage.Entry{Key: key, Value: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (t *memTx) Events(after uint64, limit int) ([]domain.AcceptedEvent, error) {
	var out []domain.AcceptedEvent
	for _, e := range t.s.log {
		if e.Sequence > after {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// TestThreadIndex: a thread's Events come back from the index the same as
// from a scan of the whole log by Work and route, whether the index has
// caught up or not, including Alex's input the index reads before it is
// routed; another thread's and other Events stay out.
func TestThreadIndex(t *testing.T) {
	key := ThreadKey("assist", "abc")
	other := ThreadKey("assist", "def")
	w := WorkID("assist", "abc")
	if got, ok := keyWork(key); !ok || got != w {
		t.Fatalf("keyWork %q %v, want %q", got, ok, w)
	}
	s := &memStore{kv: map[string][]byte{}}
	seq := uint64(0)
	add := func(e domain.Event, routed domain.ID) {
		seq++
		e.ID = domain.ID(fmt.Sprint("e", seq))
		e.OccurredAt = time.Unix(int64(seq), 0).UTC()
		s.log = append(s.log, domain.AcceptedEvent{Event: e, Sequence: seq, RoutedWorkID: routed})
	}
	filler := func(n int) {
		for range n {
			add(domain.Event{Type: "github.issue.comment", Source: "github"}, "")
		}
	}
	add(domain.Event{Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaStart, Start{Text: "Sell the bike", Role: RoleOperator})}, w)
	filler(1500)
	add(domain.Event{Type: TypeMessage, Source: Type, WorkID: w, Data: payload(SchemaMessage, Message{Role: RoleAgent, Text: "Listed it."})}, "")
	add(domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &other, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "not this one"})}, WorkID("assist", "def"))
	add(domain.Event{Type: TypeMessage, Source: Type, CorrelationKey: &key, Data: payload(SchemaMessage, Message{Role: RoleAgent, Text: "stray"})}, "")
	filler(700)
	// Not routed yet when the index reads it.
	add(domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "Take £200"})}, "")

	texts := func() []string {
		var got []domain.AcceptedEvent
		_ = s.Transact(context.Background(), storage.Guard{}, func(tx storage.Tx) error {
			var err error
			got, err = threadEvents(tx, w)
			return err
		})
		var out []string
		for _, m := range Conversation(got) {
			out = append(out, m.Text)
		}
		return out
	}
	want := "Sell the bike|Listed it.|Take £200"
	now := func() time.Time { return time.Unix(0, 0) }
	for pass := 0; ; pass++ {
		more, err := IndexOnce(context.Background(), s, now)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(texts(), "|"); pass > 0 && got != want {
			// The first page holds the start only; the rest is past the
			// tail's reach until the index catches up.
			t.Errorf("after pass %d: %q, want %q", pass, got, want)
		}
		if !more {
			break
		}
	}
	if c, _ := readCursorFrom(s); c != seq {
		t.Fatalf("cursor %d, want %d", c, seq)
	}
	// Caught up, the tail carries what the task hasn't read yet.
	add(domain.Event{Type: TypeAnswer, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaAnswer, Answer{Question: "q", Answer: "Yes"})}, w)
	if got := strings.Join(texts(), "|"); got != want+"|Yes" {
		t.Fatalf("with a tail: %q", got)
	}
	if more, err := IndexOnce(context.Background(), s, now); err != nil || more {
		t.Fatal(more, err)
	}
	if got := strings.Join(texts(), "|"); got != want+"|Yes" {
		t.Fatalf("indexed again: %q", got)
	}
}

func readCursorFrom(s *memStore) (uint64, error) {
	var c uint64
	err := s.Transact(context.Background(), storage.Guard{}, func(tx storage.Tx) error {
		var err error
		c, err = readCursor(tx)
		return err
	})
	return c, err
}

// TestThreadEventsAfter: reads that decide something (the open question,
// each change's state) take a thread's Events after a sequence from the
// index and from the whole tail past the cursor, however long it is, and
// leave out other threads' Events.
func TestThreadEventsAfter(t *testing.T) {
	key := ThreadKey("assist", "abc")
	other := ThreadKey("assist", "def")
	w := domain.Work{ID: WorkID("assist", "abc"), Kind: WorkKind, ScopeID: "assist", CorrelationKeys: []domain.CorrelationKey{key}}
	s := &memStore{kv: map[string][]byte{}}
	seq := uint64(0)
	add := func(e domain.Event) uint64 {
		seq++
		e.ID = domain.ID(fmt.Sprint("e", seq))
		s.log = append(s.log, domain.AcceptedEvent{Event: e, Sequence: seq})
		return seq
	}
	filler := func(n int) {
		for range n {
			add(domain.Event{Type: "github.issue.comment", Source: "github"})
		}
	}
	decide := func(k *domain.CorrelationKey, decision string, ids ...string) uint64 {
		return add(domain.Event{Type: TypeDecision, Source: extension.ActionSource, CorrelationKey: k,
			Data: payload(SchemaDecision, Decision{Decision: decision, Changes: ids})})
	}
	proposed := add(domain.Event{Type: TypeProposal, Source: Type, WorkID: w.ID})
	decide(&key, StateRejected, "c2") // before c2's Since: doesn't count
	since2 := seq
	filler(1200)
	decide(&key, StateApproved, "c1")
	// Indexed up to here; the rest is the tail, longer than tailLimit.
	for more := true; more; {
		var err error
		if more, err = IndexOnce(context.Background(), s, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	filler(tailLimit + 200)
	decide(&other, StateApproved, "c2") // another thread's
	decide(&key, StateRejected, "c3")

	var got []domain.AcceptedEvent
	var st map[string]string
	cs := changeSet{Changes: []change{{ID: "c1", Since: proposed}, {ID: "c2", Since: since2}, {ID: "c3", Since: proposed}}}
	if err := s.Transact(context.Background(), storage.Guard{}, func(tx storage.Tx) error {
		var err error
		if got, err = threadEventsAfter(tx, w.ID, proposed); err != nil {
			return err
		}
		st, err = states(tx, w, cs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("%d Events after the proposal, want 3: %v", len(got), got)
	}
	for _, e := range got {
		if e.Sequence <= proposed {
			t.Errorf("Event %d is not after %d", e.Sequence, proposed)
		}
	}
	want := map[string]string{"c1": StateApproved, "c2": StateProposed, "c3": StateRejected}
	for id, s := range want {
		if st[id] != s {
			t.Errorf("%s: %q, want %q", id, st[id], s)
		}
	}
}
