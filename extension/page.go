package assist

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/storage"
)

// The Assist page is core's generic extension page, so a thread also shows
// in `open-conduit web`: assist only declares it, and draws a thread as a
// conversation panel of its Events. Core reviews the change set from
// ConfigCandidate. The Mac app reads the same Events directly.

// PageID is the Assist page's ID, and its path under /pages/.
const PageID = "assist"

// PanelConversation is the panel on a thread's Work.
const PanelConversation = "conversation"

// Pages declares the Assist page.
func (*Extension) Pages() []extension.Page {
	return []extension.Page{{
		ID:       PageID,
		Title:    "Assist",
		Summary:  "Alex's personal assistant: one thread per job. It works quietly, asks when it needs you, and proposes config for anything new it needs.",
		Entry:    Type,
		WorkKind: WorkKind,
		Start:    ActionStart,
		Compose:  []string{ActionAnswer, ActionMessage},
		Route:    Type,
	}}
}

// Panels shows a thread as its conversation.
func (a Actions) Panels(ctx context.Context, t extension.ActionTarget) ([]extension.Panel, error) {
	if t.Work == nil || t.Work.Kind != WorkKind {
		return nil, nil
	}
	w := *t.Work
	var events []domain.AcceptedEvent
	if err := a.transact(ctx, func(tx storage.Tx) error {
		all, err := tx.Events(0, 0)
		for _, e := range all {
			if e.Event.WorkID == w.ID || e.RoutedWorkID == w.ID {
				events = append(events, e)
			}
		}
		return err
	}); err != nil {
		return nil, err
	}
	return []extension.Panel{{ID: PanelConversation, Title: "Conversation", Sections: []extension.PanelSection{{Messages: Conversation(events)}}}}, nil
}

// Conversation is a thread's Events as messages, in the order they were
// accepted: what each side said, the agent's questions with the answers
// given, and a note for each probe and change set Event. Only the latest
// extension.MaxPanelMessages are kept.
func Conversation(events []domain.AcceptedEvent) []extension.PanelMessage {
	slices.SortStableFunc(events, func(a, b domain.AcceptedEvent) int {
		switch {
		case a.Sequence < b.Sequence:
			return -1
		case a.Sequence > b.Sequence:
			return 1
		}
		return 0
	})
	var out []extension.PanelMessage
	asked := map[string]int{}
	for _, ae := range events {
		e := ae.Event
		m := extension.PanelMessage{At: e.OccurredAt}
		switch e.Type {
		case TypeStart:
			var x Start
			if json.Unmarshal(e.Data.Data, &x) != nil || strings.TrimSpace(x.Text) == "" {
				continue
			}
			m.From, m.Text = extension.MessageOperator, x.Text
			if x.Role == RoleAgent {
				m.From, m.Label = extension.MessageAgent, "Assistant"
			}
		case TypeStatus:
			var x Status
			if json.Unmarshal(e.Data.Data, &x) != nil || x.Status == "" {
				continue
			}
			m.From, m.Label, m.Text = extension.MessageNote, "Status", x.Status
		case TypeMessage:
			var x Message
			if json.Unmarshal(e.Data.Data, &x) != nil || strings.TrimSpace(x.Text) == "" {
				continue
			}
			m.From, m.Text = extension.MessageAgent, x.Text
			m.Label = "Assistant"
			if x.Role == RoleOperator {
				m.From, m.Label = extension.MessageOperator, ""
			}
		case TypeQuestion:
			var q Question
			if json.Unmarshal(e.Data.Data, &q) != nil || strings.TrimSpace(q.Text) == "" {
				continue
			}
			label := "Assistant asks"
			switch q.Kind {
			case AskApproval:
				label = "Assistant asks for approval"
			case AskSecret:
				label = "Assistant needs a secret (" + q.SecretName + ")"
			case AskAction:
				label = "Assistant needs you to"
			}
			m.From, m.Label, m.Text, m.Choices = extension.MessageAgent, label, q.Text, q.Choices
			if q.URL != "" {
				m.Text += "\n\n" + q.URL
			}
			if q.Context != "" {
				m.Text += "\n\n" + q.Context
			}
			asked[q.ID] = len(out)
		case TypeAnswer:
			var x Answer
			if json.Unmarshal(e.Data.Data, &x) != nil || strings.TrimSpace(x.Answer) == "" {
				continue
			}
			if i, ok := asked[x.Question]; ok {
				out[i].Chosen = extension.Clip(x.Answer, extension.MaxPanelTitle)
			}
			m.From, m.Text = extension.MessageOperator, x.Answer
		default:
			line, ok := note(e)
			if !ok {
				continue
			}
			m.From, m.Label, m.Text = extension.MessageNote, "Change set", line
		}
		m.Text = clipText(m.Text)
		for i, c := range m.Choices {
			m.Choices[i] = extension.Clip(c, extension.MaxPanelTitle)
		}
		if len(m.Choices) > extension.MaxPanelChoices {
			m.Choices = m.Choices[:extension.MaxPanelChoices]
		}
		out = append(out, m)
	}
	if n := len(out) - extension.MaxPanelMessages; n > 0 {
		out = out[n:]
	}
	return out
}

// clipText keeps a message within what a panel shows: line breaks as LF,
// no other control character but tab, valid UTF-8, and within the text
// limit. A panel with one message over them isn't shown at all, so an
// operator's message typed with CRLF line breaks emptied the conversation.
func clipText(s string) string {
	s = strings.ToValidUTF8(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s), "\uFFFD")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) <= extension.MaxPanelText {
		return s
	}
	keep := extension.MaxPanelText - 40
	for keep > 0 && !utf8Start(s[keep]) {
		keep--
	}
	return s[:keep] + fmt.Sprintf(" … (%d more bytes)", len(s)-keep)
}

// note is a change set Event as a short line: what the agent
// proposed or withdrew, and what the operator decided, validated or
// applied.
func note(e domain.Event) (string, bool) {
	switch e.Type {
	case TypeProposal:
		var p Proposal
		if json.Unmarshal(e.Data.Data, &p) != nil || p.Change == "" {
			return "", false
		}
		line := "Proposed " + p.Change + ": " + p.Op + " " + p.Pointer
		if n := len(p.Diagnostics); n > 0 {
			line += " (" + plural(n, "problem") + ")"
		}
		return line, true
	case TypeDecision:
		var d Decision
		if json.Unmarshal(e.Data.Data, &d) != nil || len(d.Changes) == 0 || d.Decision == "" {
			return "", false
		}
		who, verb := "You", d.Decision
		if d.Decision == StateWithdrawn {
			who, verb = "The assistant", "withdrew"
		}
		line := who + " " + verb + " " + strings.Join(d.Changes, ", ")
		if d.Reason != "" {
			line += ": " + d.Reason
		}
		return line, true
	case TypeChecks:
		var c Checks
		if json.Unmarshal(e.Data.Data, &c) != nil {
			return "", false
		}
		who := "The assistant"
		if c.Source == RoleOperator {
			who = "You"
		}
		if c.Valid {
			return fmt.Sprintf("%s validated the approved changes: %s, valid", who, plural(c.Approved, "change")), true
		}
		return fmt.Sprintf("%s validated the approved changes: %s", who, plural(len(c.Diagnostics), "problem")), true
	case TypeApplied:
		var a Applied
		if json.Unmarshal(e.Data.Data, &a) != nil || a.Generation == "" {
			return "", false
		}
		return "Applied " + strings.Join(a.Changes, ", ") + " as generation " + a.Generation, true
	}
	return "", false
}

// plural is n and the noun, plural unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
