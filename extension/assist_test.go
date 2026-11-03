package assist

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
)

func routeIn(e domain.Event, w *domain.Work) extension.RouteInput {
	return extension.RouteInput{Event: domain.AcceptedEvent{Event: e}, Scope: domain.Scope{ID: "assist"}, Work: w}
}

func TestRoute(t *testing.T) {
	key := ThreadKey("assist", "abc")
	start := domain.Event{Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaStart, Start{Text: "Renew the car insurance\nIt's due on the 14th", Role: RoleOperator})}
	d, err := Route{}.Match(context.Background(), routeIn(start, nil))
	if err != nil || !d.Match || d.Kind != WorkKind || d.CreateID != "assist/assist/abc" || d.Display == nil || d.Display.Title != "Renew the car insurance" || d.Context.Schema != SchemaContext {
		t.Fatalf("start: %+v %v", d, err)
	}
	spawned := start
	spawned.Source = "execution/assist/assist/xyz/attempt/1"
	if d, err := (Route{}).Match(context.Background(), routeIn(spawned, nil)); err != nil || !d.Match || !strings.Contains(d.Reason, "assistant opened") {
		t.Fatalf("a thread the agent opened: %+v %v", d, err)
	}
	thread := &domain.Work{ID: d.CreateID, Kind: WorkKind, ScopeID: "assist", CorrelationKeys: []domain.CorrelationKey{key}}
	other := &domain.Work{ID: "x", Kind: "github.issue", ScopeID: "assist"}
	elsewhere := ThreadKey("eng", "abc")
	for name, c := range map[string]struct {
		e     domain.Event
		w     *domain.Work
		match bool
	}{
		"message":               {domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key}, thread, true},
		"answer":                {domain.Event{Type: TypeAnswer, Source: extension.ActionSource, CorrelationKey: &key}, thread, true},
		"decision":              {domain.Event{Type: TypeDecision, Source: extension.ActionSource, CorrelationKey: &key}, thread, true},
		"applied":               {domain.Event{Type: TypeApplied, Source: Type, CorrelationKey: &key}, thread, true},
		"the agent's own words": {domain.Event{Type: TypeMessage, Source: "execution/x", CorrelationKey: &key}, thread, false},
		"a second start":        {start, thread, false},
		"a message unowned":     {domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key}, nil, false},
		"other Work's key":      {domain.Event{Type: TypeMessage, Source: extension.ActionSource, CorrelationKey: &key}, other, false},
		"another scope's key":   {domain.Event{Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &elsewhere, Data: start.Data}, nil, false},
		"a start with no key":   {domain.Event{Type: TypeStart, Source: extension.ActionSource, Data: start.Data}, nil, false},
		"a posted start":        {domain.Event{Type: TypeStart, Source: "core.manual", CorrelationKey: &key, Data: start.Data}, nil, false},
		"an empty start":        {domain.Event{Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &key, Data: payload(SchemaStart, Start{})}, nil, false},
	} {
		d, err := Route{}.Match(context.Background(), routeIn(c.e, c.w))
		if err != nil || d.Match != c.match || d.Reason == "" {
			t.Errorf("%s: %+v %v", name, d, err)
		}
	}
}

func TestParseAsk(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	in, deadline, err := parseAsk([]byte(`{"question":"Book the 9:40 to Leeds?","kind":"approval"}`), now)
	if err != nil || strings.Join(in.Choices, ",") != "Approve,Decline" || !deadline.Equal(now.Add(DefaultAskTimeout)) {
		t.Fatalf("approval: %+v %v %v", in, deadline, err)
	}
	if in, _, err := parseAsk([]byte(`{"question":"Your Octopus API key","kind":"secret","secret_name":"octopus-api"}`), now); err != nil || in.SecretName != "octopus-api" {
		t.Fatalf("secret: %+v %v", in, err)
	}
	if _, d, err := parseAsk([]byte(`{"question":"Which?","choices":["a","b"],"timeout_hours":5}`), now); err != nil || !d.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("timeout: %v %v", d, err)
	}
	for name, input := range map[string]string{
		"no question":           `{}`,
		"unknown kind":          `{"question":"x","kind":"bribe"}`,
		"secret without a name": `{"question":"x","kind":"secret"}`,
		"a bad secret name":     `{"question":"x","kind":"secret","secret_name":"Bad Name"}`,
		"a secret with choices": `{"question":"x","kind":"secret","secret_name":"k","choices":["a","b"]}`,
		"a name without secret": `{"question":"x","secret_name":"k"}`,
		"one choice":            `{"question":"x","choices":["a"]}`,
		"a repeated choice":     `{"question":"x","choices":["a","A"]}`,
		"a javascript link":     `{"question":"x","kind":"action","url":"javascript:alert(1)"}`,
		"too long a wait":       `{"question":"x","timeout_hours":10000}`,
		"an unknown field":      `{"question":"x","colour":"red"}`,
	} {
		var oe *extension.OperationError
		if _, _, err := parseAsk([]byte(input), now); !errors.As(err, &oe) || oe.Code != "invalid_input" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestScanText(t *testing.T) {
	for _, s := range []string{"ghp_" + strings.Repeat("a", 36), "sk-ant-" + strings.Repeat("b", 30), "AKIA" + strings.Repeat("C", 16), "sk-proj-" + strings.Repeat("d", 40)} {
		if scanText("here: "+s) == nil {
			t.Errorf("missed %s…", s[:8])
		}
	}
	if err := scanText("the reference is keyring:octopus-api"); err != nil {
		t.Fatalf("refused a reference: %v", err)
	}
}

func TestTitleOf(t *testing.T) {
	if got := titleOf("  Sort the boiler service \n details"); got != "Sort the boiler service" {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("é", 60)
	if got := titleOf(long); len(got) > 81 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%q", got)
	}
}

func TestInvokeRefusesWithoutTheStore(t *testing.T) {
	w := domain.Work{ID: "x", Kind: "github.issue", ScopeID: "assist"}
	for name, c := range map[string]extension.ActionCall{
		"not the operator": {Action: ActionStart, Input: "hi", Caller: extension.Caller{Kind: "slack_user", ID: "U1"}},
		"unknown action":   {Action: "apply", Caller: extension.LocalOperator},
		"empty start":      {Action: ActionStart, Input: "  ", Caller: extension.LocalOperator},
		"start on Work":    {Action: ActionStart, Target: extension.ActionTarget{Work: &w}, Caller: extension.LocalOperator},
		"message, no Work": {Action: ActionMessage, Caller: extension.LocalOperator},
		"other Work":       {Action: ActionMessage, Target: extension.ActionTarget{Work: &w}, Input: "hi", Caller: extension.LocalOperator},
	} {
		var oe *extension.OperationError
		if _, err := (Actions{}).Invoke(context.Background(), c); !errors.As(err, &oe) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRenderEvent(t *testing.T) {
	for _, c := range []struct {
		e    domain.Event
		want string
	}{
		{domain.Event{Type: TypeStart, Data: payload(SchemaStart, Start{Text: "Find a plumber", Role: RoleOperator})}, "Alex started a thread\nFind a plumber"},
		{domain.Event{Type: TypeStart, Data: payload(SchemaStart, Start{Text: "Your passport expires in May", Role: RoleAgent})}, "You opened this thread"},
		{domain.Event{Type: TypeMessage, Data: payload(SchemaMessage, Message{Role: RoleOperator, Text: "use the joint account"})}, "Alex says\nuse the joint account"},
		{domain.Event{Type: TypeAnswer, Data: payload(SchemaAnswer, Answer{Question: "q", Answer: "Approve"})}, "answered your question\nApprove"},
	} {
		if got, ok := RenderEvent(c.e); !ok || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q", c.e.Type, got)
		}
	}
	if _, ok := RenderEvent(domain.Event{Type: TypeMessage, Data: payload(SchemaMessage, Message{Role: RoleAgent, Text: "hi"})}); ok {
		t.Error("rendered the agent's own message")
	}
}

func TestManifestDeclaresEveryOperation(t *testing.T) {
	m := Manifest()
	var got []string
	for _, op := range m.Provides.Operations {
		got = append(got, op.Name)
		if op.Summary == "" || len(op.Input) == 0 {
			t.Errorf("%s has no summary or input", op.Name)
		}
		var schema map[string]any
		if json.Unmarshal(op.Input, &schema) != nil {
			t.Errorf("%s: input is not a schema", op.Name)
		}
	}
	if strings.Join(got, ",") != strings.Join(Operations(), ",") {
		t.Fatalf("declared %v, offered %v", got, Operations())
	}
}
