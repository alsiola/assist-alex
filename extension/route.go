package assist

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
)

// Route is the `assist` route handler. assist.start gives an unowned
// thread key its Work, whether Alex started it (an action) or the agent
// did (assist.spawn). After that only Alex's own input reaches a thread:
// his messages, answers and decisions (source core.action), and the
// generation that applies its changes. Nothing else the agent emits is
// taken, so its own words never wake it.
type Route struct{}

var (
	_ extension.RouteHandler = Route{}
	_ extension.RouteSampler = Route{}
)

func (Route) Match(_ context.Context, in extension.RouteInput) (extension.RouteDecision, error) {
	e := in.Event.Event
	if e.CorrelationKey == nil {
		return extension.Skip("the event names no assist thread"), nil
	}
	id, ok := threadID(in.Scope.ID, *e.CorrelationKey)
	if !ok {
		return extension.Skip("not an assist thread of this scope"), nil
	}
	if in.Work == nil {
		if e.Type != TypeStart {
			return extension.Skip("no thread owns this key"), nil
		}
		var s Start
		if e.Data.Schema != SchemaStart || json.Unmarshal(e.Data.Data, &s) != nil || strings.TrimSpace(s.Text) == "" {
			return extension.Skip("the start is unreadable"), nil
		}
		// Alex starts threads through the start action; the agent opens
		// them through assist.spawn, which runs under an execution.
		byAgent := strings.HasPrefix(e.Source, "execution/")
		if e.Source != extension.ActionSource && !byAgent {
			return extension.Skip("only Alex or the assistant opens a thread"), nil
		}
		title := s.Title
		if title == "" {
			title = titleOf(s.Text)
		}
		reason := "Alex started a thread"
		if byAgent {
			reason = "the assistant opened a thread"
		}
		return extension.RouteDecision{Match: true, Kind: WorkKind, CreateID: WorkID(in.Scope.ID, id),
			Context: payload(SchemaContext, s), Display: &domain.WorkDisplay{Title: title}, Reason: reason}, nil
	}
	if in.Work.Kind != WorkKind {
		return extension.Skip("the key is owned by other Work"), nil
	}
	applied := e.Source == Type && e.Type == TypeApplied
	if e.Source != extension.ActionSource && !applied {
		return extension.Skip("only Alex's own input, and the generation that applies a thread's changes, reach a thread"), nil
	}
	switch e.Type {
	case TypeMessage:
		return extension.RouteDecision{Match: true, Reason: "Alex's message"}, nil
	case TypeAnswer:
		return extension.RouteDecision{Match: true, Reason: "Alex's answer"}, nil
	case TypeDecision:
		return extension.RouteDecision{Match: true, Reason: "Alex's decision on the thread's changes"}, nil
	case TypeChecks:
		return extension.RouteDecision{Match: true, Reason: "Alex validated the thread's changes"}, nil
	case TypeApplied:
		return extension.RouteDecision{Match: true, Reason: "Alex applied the thread's changes"}, nil
	}
	return extension.Skip("the thread has already started"), nil
}

// Samples offers starting a thread.
func (Route) Samples(_ context.Context, in extension.SampleInput) []extension.RouteSample {
	key := ThreadKey(in.Scope.ID, "example")
	return []extension.RouteSample{{ID: "thread-started", Title: "Alex starts a thread",
		Event: domain.Event{Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &key,
			Data: payload(SchemaStart, Start{Text: "Find me a dentist near the office", Role: RoleOperator})}}}
}

// reachesThread is whether the route takes e to the thread its key names,
// by Match's rule: a start Alex or the assistant opened it with, Alex's own
// input, and the generation that applies its changes. A thread read
// (thread.go) takes an Event not routed yet to the thread its key names by
// this rule.
func reachesThread(e domain.Event) bool {
	switch e.Type {
	case TypeStart:
		return e.Source == extension.ActionSource || strings.HasPrefix(e.Source, "execution/")
	case TypeMessage, TypeAnswer, TypeDecision, TypeChecks:
		return e.Source == extension.ActionSource
	case TypeApplied:
		return e.Source == extension.ActionSource || e.Source == Type
	}
	return false
}
