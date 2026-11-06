package assist_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/open-conduit/open-conduit/client"
	"github.com/open-conduit/open-conduit/extensiontest"
)

// TestDaemonThread runs a thread on the real daemon, with the package
// installed and a scripted Claude Code making its calls through the real
// toolbelt: Alex starts a thread, the agent names it, tells him something
// and asks for an approval, which parks the thread with an answer action;
// his answer resumes it, and his message closes it.
func TestDaemonThread(t *testing.T) {
	d := extensiontest.Daemon(t, extensiontest.Package("."), extensiontest.UseFakeClaude())
	agent := d.Agent().
		Call("assist.status", map[string]any{"title": "Boiler service", "status": "Finding a slot"}).
		Call("assist.say", map[string]any{"text": "Found two engineers; British Gas can do Thursday.", "notify": "fyi"}).
		Call("assist.ask", map[string]any{"kind": "secret", "question": "Missing the name"}).
		Call("assist.ask", map[string]any{"kind": "approval", "question": "Book British Gas for Thursday 9-12, £89?"}).
		Ends("help").
		Turn().Call("assist.say", map[string]any{"text": "Booked."}).Ends("done").
		Turn().Ends("finished")

	c := d.Client()
	ctx := context.Background()
	scopes, err := c.Scopes(ctx, nil)
	if err != nil || len(scopes.Items) != 1 {
		t.Fatalf("scopes: %+v %v", scopes, err)
	}
	scope := scopes.Items[0].ID
	if _, err = c.InvokeAction(ctx, client.ActionTarget{ScopeID: scope}, "assist", "start", "Get the boiler serviced before winter", "start-1"); err != nil {
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
	if thread.Kind != "assist.thread" || thread.Display == nil || thread.Display.Title != "Get the boiler serviced before winter" {
		t.Fatalf("thread: %+v", thread)
	}
	first := agent.Played(1)
	if !strings.Contains(first.Input, "Alex started a thread") || !strings.Contains(first.Input, "Get the boiler serviced") {
		t.Fatalf("the agent was started with %q", first.Input)
	}
	if r := first.Results[2]; r.Operation != "assist.ask" || !r.IsError || !strings.Contains(r.Text, "secret_name") {
		t.Fatalf("a secret ask without a name: %+v", r)
	}
	d.WaitWork(thread.ID, "waiting")

	events := d.Events("assist.question")
	var q struct {
		Kind    string   `json:"kind"`
		Choices []string `json:"choices"`
	}
	if err = json.Unmarshal(events[len(events)-1].Data.Data, &q); err != nil || q.Kind != "approval" || strings.Join(q.Choices, ",") != "Approve,Decline" {
		t.Fatalf("question: %+v %v", q, err)
	}
	said := d.Events("assist.message")
	var m struct{ Role, Text, Notify string }
	if err = json.Unmarshal(said[0].Data.Data, &m); err != nil || m.Role != "agent" || m.Notify != "fyi" || said[0].WorkID != thread.ID {
		t.Fatalf("message: %+v %+v %v", said[0], m, err)
	}
	if st := d.Events("assist.status"); st[0].WorkID != thread.ID {
		t.Fatalf("status: %+v", st[0])
	}

	actions, err := c.Actions(ctx, client.ActionTarget{WorkID: thread.ID})
	if err != nil || !hasAction(actions, "answer") || !hasAction(actions, "message") {
		t.Fatalf("actions while asking: %+v %v", actions, err)
	}
	if _, err = c.InvokeAction(ctx, client.ActionTarget{WorkID: thread.ID}, "assist", "answer", "1", "answer-1"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	second := agent.Played(2)
	if !strings.Contains(second.Input, "Alex answered your question\nApprove") {
		t.Fatalf("resumed with %q", second.Input)
	}
	d.Until("the turn to end", 30*time.Second, func() bool {
		w, err := c.Work(ctx, thread.ID)
		return err == nil && w.State != "running"
	})
	if actions, err = c.Actions(ctx, client.ActionTarget{WorkID: thread.ID}); err != nil || hasAction(actions, "answer") {
		t.Fatalf("answer offered after it was answered: %+v %v", actions, err)
	}
	if _, err = c.InvokeAction(ctx, client.ActionTarget{WorkID: thread.ID}, "assist", "message", "Thanks, all done", "msg-1"); err != nil {
		t.Fatalf("message: %v", err)
	}
	third := agent.Played(3)
	if !strings.Contains(third.Input, "Alex says\nThanks, all done") {
		t.Fatalf("resumed with %q", third.Input)
	}
	d.WaitWork(thread.ID, "completed")

	// The conversation panel reads the thread from its own Events.
	want := "Get the boiler serviced before winter|Found two engineers; British Gas can do Thursday.|Book British Gas for Thursday 9-12, £89?|Approve|Booked.|Thanks, all done"
	var got string
	d.Until("the conversation", 30*time.Second, func() bool {
		panels, err := c.Panels(ctx, client.ActionTarget{WorkID: thread.ID})
		if err != nil || len(panels) != 1 || len(panels[0].Sections) != 1 {
			return false
		}
		var texts []string
		for _, m := range panels[0].Sections[0].Messages {
			if m.From != "note" {
				texts = append(texts, m.Text)
			}
		}
		got = strings.Join(texts, "|")
		return got == want
	})
}

func hasAction(actions []client.Action, id string) bool {
	for _, a := range actions {
		if a.ID == id {
			return true
		}
	}
	return false
}

// TestDaemonMessageSupersedesAsk: a message Alex writes while the thread
// waits on a question resumes it and closes the question, so the app stops
// offering an answer to it, even when the agent's next turn parks again
// without asking anything new.
func TestDaemonMessageSupersedesAsk(t *testing.T) {
	d := extensiontest.Daemon(t, extensiontest.Package("."), extensiontest.UseFakeClaude())
	agent := d.Agent().
		Call("assist.ask", map[string]any{"question": "Can you send the photos?", "choices": []string{"Sending now", "Later"}}).
		Ends("help").
		Turn().Call("assist.say", map[string]any{"text": "Got them, listing it."}).Ends("help").
		Turn().Ends("finished")

	c := d.Client()
	ctx := context.Background()
	scopes, err := c.Scopes(ctx, nil)
	if err != nil || len(scopes.Items) != 1 {
		t.Fatalf("scopes: %+v %v", scopes, err)
	}
	if _, err = c.InvokeAction(ctx, client.ActionTarget{ScopeID: scopes.Items[0].ID}, "assist", "start", "Sell the coffee machine", "start-1"); err != nil {
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
	agent.Played(1)
	d.WaitWork(thread.ID, "waiting")
	if actions, err := c.Actions(ctx, client.ActionTarget{WorkID: thread.ID}); err != nil || !hasAction(actions, "answer") {
		t.Fatalf("actions while asking: %+v %v", actions, err)
	}
	if _, err = c.InvokeAction(ctx, client.ActionTarget{WorkID: thread.ID}, "assist", "message", "These are the two I have", "msg-1"); err != nil {
		t.Fatalf("message: %v", err)
	}
	second := agent.Played(2)
	if !strings.Contains(second.Input, "These are the two I have") {
		t.Fatalf("resumed with %q", second.Input)
	}
	d.Until("the turn to end", 30*time.Second, func() bool {
		w, err := c.Work(ctx, thread.ID)
		return err == nil && w.State != "running" && w.State != "queued"
	})
	if actions, err := c.Actions(ctx, client.ActionTarget{WorkID: thread.ID}); err != nil || hasAction(actions, "answer") {
		t.Fatalf("answer still offered after his message: %+v %v", actions, err)
	}
	if _, err = c.InvokeAction(ctx, client.ActionTarget{WorkID: thread.ID}, "assist", "answer", "Sending now", "answer-1"); err == nil {
		t.Fatal("answered a question his message superseded")
	}
}
