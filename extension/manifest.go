package assist

import (
	"context"
	"encoding/json"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/protocol"
)

// Version is this package's own version.
const Version = "0.1.0"

// Command is the package's executable, relative to its directory.
const Command = "assist-extension"

// PlaybookDir is the package directory of its playbooks: assist.md is the
// assistant's playbook, which a thread's binding (`playbook: assist`)
// starts on.
const PlaybookDir = "playbooks"

// recorded is the loss every assist family states: the thread's own
// record, written once as it happens, with no source to re-derive it from.
const recorded = "Recorded once by the assist thread as it happens; nothing re-derives it."

// families are the Event types assist records or its actions produce.
var families = []string{TypeStart, TypeMessage, TypeQuestion, TypeAnswer, TypeStatus, TypeProposal, TypeDecision, TypeChecks, TypeApplied}

// schemas are the payload schema bases assist writes, each at version 1.
var schemas = []string{SchemaStart, SchemaMessage, SchemaQuestion, SchemaAnswer, SchemaStatus, SchemaProposal, SchemaDecision, SchemaChecks, SchemaApplied, SchemaContext, SchemaTool}

// Manifest is the package manifest.
func Manifest() protocol.Manifest {
	p := protocol.Provides{
		Extensions: []protocol.ExtensionDecl{{Type: Type}},
		Routes:     []protocol.RouteDecl{{Handler: Type, Config: true, Samples: true}},
		WorkKinds:  []protocol.WorkKindDecl{{Kind: WorkKind}},
		EntryGated: true,
		Tasks:      []protocol.TaskDecl{{Name: AppliedTask, IntervalSeconds: 1}},
		Actions:    true,
		Panels:     true,
		// Core reviews and applies the change set.
		ConfigCandidate: true,
		Render:          true,
		Playbooks:       PlaybookDir,
	}
	for _, pg := range (&Extension{}).Pages() {
		p.Pages = append(p.Pages, protocol.PageDecl{ID: pg.ID, Title: pg.Title, Summary: pg.Summary, Entry: pg.Entry, WorkKind: pg.WorkKind, Start: pg.Start, Compose: pg.Compose, Route: pg.Route})
	}
	for _, f := range families {
		p.Families = append(p.Families, protocol.Family{Type: f, Loss: recorded})
	}
	for _, s := range schemas {
		p.Schemas = append(p.Schemas, protocol.SchemaDecl{Name: s[:len(s)-len(".v1")], Versions: []int{1}})
	}
	for _, op := range (&Tools{}).Registration().Operations {
		kind := protocol.OperationHandler
		switch {
		case op.Wait != nil:
			kind = protocol.OperationWait
		case op.Emit != nil:
			kind = protocol.OperationEmit
		}
		p.Operations = append(p.Operations, protocol.OperationDecl{Name: op.Name, Kind: kind, Commit: kind == protocol.OperationEmit,
			Summary: op.Summary, Guidance: op.Guidance, Input: op.Input})
	}
	return protocol.Manifest{Protocol: []int{protocol.Version}, Name: Type, Version: Version,
		Summary:     "A personal assistant: durable threads with a Claude Code agent that works your jobs, asks only when it needs you, and proposes config for any new power it needs.",
		Command:     []string{Command},
		Permissions: []string{protocol.PermissionConfigRead},
		Provides:    p}
}

// CertifyScope is the scope the certification fixture configures.
const CertifyScope = "assist-certify"

// Certification is the package's certification.json: an inline fixture
// with one `assist` entry, the route starting a thread and skipping what
// isn't Alex's, and operations refusing bad input before they touch
// anything.
func Certification() protocol.CertificationFile {
	scope := domain.Scope{ID: CertifyScope, Name: CertifyScope}
	key := ThreadKey(CertifyScope, "certify")
	start := extension.RouteInput{Scope: scope, Route: domain.Route{Name: Type, Handler: Type, Binding: "agent"},
		Event: domain.AcceptedEvent{Sequence: 1, Event: domain.Event{ID: "certify-start", Type: TypeStart, Source: extension.ActionSource, CorrelationKey: &key,
			Data: payload(SchemaStart, Start{Text: "Book the car in for its MOT", Role: RoleOperator})}}}
	stray := start
	stray.Event.Event.ID, stray.Event.Event.Type, stray.Event.Event.Source = "certify-stray", TypeMessage, Type
	var cases []protocol.RouteCase
	for _, in := range []extension.RouteInput{start, stray} {
		want, _ := Route{}.Match(context.Background(), in)
		cases = append(cases, protocol.RouteCase{Handler: Type, Input: protocol.FromRouteInput(in), Want: protocol.FromRouteDecision(want)})
	}
	bad := func(op string, input string) protocol.OperationCase {
		return protocol.OperationCase{Operation: op, Input: protocol.Payload{Data: json.RawMessage(input)}, Error: "invalid_input"}
	}
	return protocol.CertificationFile{Fixture: &protocol.Fixture{
		Entries: []protocol.ConfigEntry{{ScopeID: CertifyScope, Type: Type, Name: Type, Config: json.RawMessage(`{}`)}},
		Routes:  cases,
		Operations: []protocol.OperationCase{
			bad(OpPropose, `{"op":"add","pointer":"no-slash","why":"certify"}`),
			bad(OpRead, `{"unknown":true}`),
			bad(OpWithdraw, `{"unknown":true}`),
		},
	}}
}
