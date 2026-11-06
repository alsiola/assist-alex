package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/protocol"
	"github.com/open-conduit/open-conduit/protocol/sdk"
	"github.com/open-conduit/open-conduit/storage"
)

// The thread index. A package can only read the event log whole (state.events
// pages through every Event of its scopes), and a busy install has tens of
// thousands of them, so reading a thread's Events from the log on every panel
// call took seconds. The index task tails the log instead, a page at a time,
// and keeps each thread's own Events in assist's KV under the thread's Work,
// with the cursor it has read to. A thread's Events are then the ones indexed
// under it, plus any after the cursor the task hasn't reached yet.

// IndexTask is the task that tails the log into the index.
const IndexTask = "assist-index"

// nsIndex is the index's KV namespace: the cursor, and one entry per Event
// of a thread under "w/<work>/<sequence>".
const nsIndex = Type + ".index"

// keyCursor is the last sequence the index has read.
const keyCursor = "cursor"

// indexPage is how many Events the index reads, and commits, at a time.
const indexPage = 1000

// indexRun is the longest one run of the task keeps reading for: it stops at
// the end of the log or after this, and the next run carries on.
const indexRun = 5 * time.Second

// tailLimit is the most Events past the cursor a read looks through. The task
// keeps the tail short; it is only long while the index first catches up,
// and a read then shows what is indexed so far rather than reading the log.
const tailLimit = indexPage

// threadsOf is the threads e belongs to, as their Work IDs: the Work an
// assist Event was emitted on, and the thread whose key it carries when the
// route takes it to that thread. Routing happens after an Event is accepted,
// so the index can't wait for it: it reads the thread from the key, by the
// same rule the route applies.
func threadsOf(e domain.AcceptedEvent) []domain.ID {
	if !slices.Contains(families, e.Event.Type) {
		return nil
	}
	var out []domain.ID
	if w := e.Event.WorkID; w != "" {
		out = append(out, w)
	}
	if k := e.Event.CorrelationKey; k != nil && reachesThread(e.Event) {
		if w, ok := keyWork(*k); ok && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// keyWork is the Work ID of the thread a key names: ThreadKey and WorkID
// both carry the scope and the thread's ID.
func keyWork(k domain.CorrelationKey) (domain.ID, bool) {
	rest, ok := strings.CutPrefix(k.ExternalID, "thread/")
	if !ok || k.Source != Type || !strings.Contains(rest, "/") {
		return "", false
	}
	return domain.ID(Type + "/" + rest), true
}

func workPrefix(work domain.ID) string { return "w/" + string(work) + "/" }

func indexKey(work domain.ID, seq uint64) string {
	return workPrefix(work) + fmt.Sprintf("%020d", seq)
}

func readCursor(tx storage.Tx) (uint64, error) {
	b, err := tx.Get(nsIndex, keyCursor)
	if errors.Is(err, storage.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(string(b), 10, 64)
}

// IndexOnce reads the next page of the log after the cursor and indexes it,
// committing the entries and the new cursor together. It reports whether the
// page was full, so there may be more to read.
func IndexOnce(ctx context.Context, store storage.Store, now func() time.Time) (bool, error) {
	more := false
	err := store.Transact(ctx, storage.Guard{Now: now()}, func(tx storage.Tx) error {
		cursor, err := readCursor(tx)
		if err != nil {
			return err
		}
		page, err := tx.Events(cursor, indexPage)
		if err != nil || len(page) == 0 {
			return err
		}
		more = len(page) == indexPage
		for _, e := range page {
			works := threadsOf(e)
			if len(works) == 0 {
				continue
			}
			b, err := json.Marshal(protocol.FromAccepted(e))
			if err != nil {
				return err
			}
			for _, w := range works {
				if err := tx.Put(nsIndex, indexKey(w, e.Sequence), b); err != nil {
					return err
				}
			}
		}
		return tx.Put(nsIndex, keyCursor, []byte(strconv.FormatUint(page[len(page)-1].Sequence, 10)))
	})
	return more, err
}

// index is the index task: it reads page after page until the log ends or
// the run's time is up.
func (x *Extension) index(ctx context.Context, h *sdk.Host) error {
	x.bind(h)
	ctx, cancel := context.WithTimeout(ctx, indexRun)
	defer cancel()
	for {
		more, err := IndexOnce(ctx, x.store, x.now)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !more {
			return nil
		}
	}
}

// threadEvents is a Work's Events for its panel: those indexed under it and
// those after the cursor, up to tailLimit of them, in sequence order. While
// the index first catches up it shows what is indexed so far.
func threadEvents(tx storage.Tx, work domain.ID) ([]domain.AcceptedEvent, error) {
	return indexedEvents(tx, work, 0, tailLimit)
}

// threadEventsAfter is a Work's Events after sequence after, for reads that
// decide something (whether a question is open, the state of each change):
// those indexed under it and every one past the cursor, so a read never
// misses an Event the index task hasn't reached. The task keeps that tail a
// few Events long, so this reads the thread rather than the log.
func threadEventsAfter(tx storage.Tx, work domain.ID, after uint64) ([]domain.AcceptedEvent, error) {
	return indexedEvents(tx, work, after, 0)
}

// indexedEvents is a Work's Events after sequence after: its index entries,
// then the log past the cursor (up to limit Events of it, 0 for all), in
// sequence order.
func indexedEvents(tx storage.Tx, work domain.ID, after uint64, limit int) ([]domain.AcceptedEvent, error) {
	cursor, err := readCursor(tx)
	if err != nil {
		return nil, err
	}
	entries, err := tx.List(nsIndex, workPrefix(work))
	if err != nil {
		return nil, err
	}
	var out []domain.AcceptedEvent
	seen := map[uint64]bool{}
	for _, en := range entries {
		var a protocol.AcceptedEvent
		if json.Unmarshal(en.Value, &a) != nil {
			continue
		}
		e := a.Domain()
		if e.Sequence <= after {
			continue
		}
		seen[e.Sequence] = true
		out = append(out, e)
	}
	tail, err := tx.Events(max(cursor, after), limit)
	if err != nil {
		return nil, err
	}
	for _, e := range tail {
		if slices.Contains(threadsOf(e), work) && !seen[e.Sequence] {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b domain.AcceptedEvent) int {
		switch {
		case a.Sequence < b.Sequence:
			return -1
		case a.Sequence > b.Sequence:
			return 1
		}
		return 0
	})
	return out, nil
}
