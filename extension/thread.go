package assist

import (
	"slices"
	"strings"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/storage"
)

// A thread's Events come from the host's read of one Work's Events
// (state.work_events), which costs the thread's own Events rather than the
// log: those emitted on the thread and those routed to it. Routing happens
// after an Event is accepted, so an Event Alex has just sent (a message, an
// answer, a decision) may not have reached its thread yet. A read takes
// those from the tail of the log past the last Event routed to the thread,
// by the rule the route applies to their key, so a decision is never read
// without the input that settles it. The host serves the tail from its scope
// index, so it costs assist's own Events, and the route keeps it short.

// threadEvents is a Work's assist Events after sequence after, in sequence
// order: those the host holds under it, then those whose key names it and
// that the route hasn't reached yet.
func threadEvents(tx storage.Tx, work domain.ID, after uint64) ([]domain.AcceptedEvent, error) {
	own, err := tx.WorkEvents(work, after, 0)
	if err != nil {
		return nil, err
	}
	var out []domain.AcceptedEvent
	seen := map[uint64]bool{}
	routed := after
	for _, e := range own {
		if e.RoutedWorkID == work {
			routed = max(routed, e.Sequence)
		}
		if slices.Contains(families, e.Event.Type) {
			seen[e.Sequence] = true
			out = append(out, e)
		}
	}
	tail, err := tx.Events(routed, 0)
	if err != nil {
		return nil, err
	}
	for _, e := range tail {
		if !e.Routed && !seen[e.Sequence] && slices.Contains(threadsOf(e), work) {
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

// threadsOf is the threads e belongs to, as their Work IDs: the Work an
// assist Event was emitted on, and the thread whose key it carries when the
// route takes it to that thread.
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
