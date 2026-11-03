package assist_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/open-conduit/open-conduit/client"
	"github.com/open-conduit/open-conduit/extensiontest"
)

// TestDaemonChangeSet runs a change set on the real daemon: an add onto an
// existing route is refused rather than overwriting it, a change that
// drops the scope's assist route says so, and once the operator applies
// the approved change the woken thread can propose again.
func TestDaemonChangeSet(t *testing.T) {
	d := extensiontest.Daemon(t, extensiontest.Package("."), extensiontest.UseFakeClaude())
	route := map[string]any{"name": "inbox-check", "handler": "manual", "binding": "agent", "active_policy": "queue", "config": map[string]any{}}
	agent := d.Agent().
		Call("assist.propose", map[string]any{"op": "add", "pointer": "/scopes/test/routes/0", "value": route, "why": "a check"}).
		Call("assist.propose", map[string]any{"op": "add", "pointer": "/scopes/test/routes/manual", "value": route, "why": "a check"}).
		Call("assist.propose", map[string]any{"op": "replace", "pointer": "/scopes/test/routes/0", "value": route, "why": "a check"}).
		Call("assist.withdraw", map[string]any{"change": "c1"}).
		Call("assist.propose", map[string]any{"op": "add", "pointer": "/scopes/test/routes/-", "value": route, "why": "a check"}).
		Ends("help").
		Turn().Ends("help").
		Turn().Call("assist.propose", map[string]any{"op": "replace", "pointer": "/scopes/test/name", "value": "Test two", "why": "a better name"}).
		Ends("help")

	c := d.Client()
	ctx := context.Background()
	if _, err := c.InvokeAction(ctx, client.ActionTarget{ScopeID: "test"}, "assist", "start", "Check the inbox", "start-1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	var thread client.Work
	d.Until("the thread", 30*time.Second, func() bool {
		ws, err := c.Works(ctx, nil)
		if err != nil || len(ws.Items) == 0 {
			return false
		}
		thread = ws.Items[0]
		return true
	})
	first := agent.Played(1)
	for i, what := range []string{"an add at an index", "an add at a name"} {
		if r := first.Results[i]; !r.IsError || !strings.Contains(r.Text, "add_overwrites") || !strings.Contains(r.Text, "/scopes/test/routes/-") {
			t.Fatalf("%s: %+v", what, r)
		}
	}
	if r := first.Results[2]; r.IsError || !strings.Contains(r.Text, "orphans_threads") {
		t.Fatalf("a replace dropping the assist route: %+v", r)
	}
	if r := first.Results[4]; r.IsError || strings.Contains(r.Text, "orphans_threads") {
		t.Fatalf("an append: %+v", r)
	}
	d.Until("the first turn to end", 30*time.Second, func() bool {
		w, err := c.Work(ctx, thread.ID)
		return err == nil && w.State != "running"
	})
	if _, err := c.InvokeAction(ctx, client.ActionTarget{WorkID: thread.ID}, "assist", "approve_c2", "", "approve-1"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	agent.Played(2)
	d.Until("the second turn to end", 30*time.Second, func() bool {
		w, err := c.Work(ctx, thread.ID)
		return err == nil && w.State != "running"
	})
	cand, err := c.WorkConfigCandidate(ctx, thread.ID)
	if err != nil || cand.ConfigText == "" {
		t.Fatalf("candidate: %+v %v", cand, err)
	}
	if _, err = c.ApplyConfigText(ctx, []byte(cand.ConfigText), cand.CandidateHash, cand.BaseGeneration, "apply-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	third := agent.Played(3)
	if r := third.Results[0]; r.IsError || !strings.Contains(r.Text, "c3") {
		t.Fatalf("a propose after the apply: %+v", r)
	}
	cand, err = c.WorkConfigCandidate(ctx, thread.ID)
	if err != nil {
		t.Fatalf("candidate after apply: %v", err)
	}
	for _, ch := range cand.Changes {
		if ch.ID == "c2" && ch.State != "applied" || ch.ID == "c3" && ch.State != "proposed" {
			t.Fatalf("changes after apply: %+v", cand.Changes)
		}
	}
}
