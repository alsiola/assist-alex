// Package assist is Alex's personal assistant on Open Conduit: durable
// conversation threads with a Claude Code agent that works his jobs, keeps
// to itself until it needs him, and asks for whatever it lacks.
//
// A thread is Work of kind assist.thread in a scope with an `assist` entry.
// The entry's "start" action (or the agent's assist.spawn) produces
// assist.start on a fresh thread key, and the `assist` route gives that key
// Work. On the thread's Work, "message" and "answer" produce assist.message
// and assist.answer on the same key, which the route takes as more input
// (injected into a running turn, or queued).
//
// The agent talks back through the toolbelt, never through text it prints:
// assist.say emits assist.message with how loudly to tell Alex,
// assist.ask records assist.question (a question, an approval, a secret to
// store or something to do) and parks the thread until he answers or
// writes, assist.status names the thread and says where it stands, and the
// change set operations propose config for him to approve and apply.
// Nothing the agent writes carries the thread key, so nothing it says
// routes back to its own Work.
//
// It is an installed package: cmd/assist-extension serves Build over the
// extension protocol. The change set code is adapted from Open Conduit's
// setup extension (MIT).
package assist

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/open-conduit/open-conduit/domain"
)

// Type is the extension type, the toolbelt registration name, the route
// handler name and the source of thread keys.
const Type = "assist"

// WorkKind is a thread's Work kind.
const WorkKind = "assist.thread"

// Event families.
const (
	TypeStart    = "assist.start"
	TypeMessage  = "assist.message"
	TypeQuestion = "assist.question"
	TypeAnswer   = "assist.answer"
	TypeStatus   = "assist.status"
	TypeProposal = "assist.proposal"
	TypeDecision = "assist.decision"
	TypeChecks   = "assist.checks"
	TypeApplied  = "assist.applied"

	SchemaStart    = "assist.start.v1"
	SchemaMessage  = "assist.message.v1"
	SchemaQuestion = "assist.question.v1"
	SchemaAnswer   = "assist.answer.v1"
	SchemaStatus   = "assist.status.v1"
	SchemaProposal = "assist.proposal.v1"
	SchemaDecision = "assist.decision.v1"
	SchemaChecks   = "assist.checks.v1"
	SchemaApplied  = "assist.applied.v1"
	SchemaContext  = "assist.thread.v1"
	SchemaTool     = "assist.tool.v1"
)

// Message roles. The operator is Alex.
const (
	RoleAgent    = "agent"
	RoleOperator = "operator"
)

// Notify levels: how loudly a message reaches Alex. Quiet is the default:
// it is in the thread when he looks. FYI marks the thread unread. Now
// interrupts him with a notification.
const (
	NotifyQuiet = "quiet"
	NotifyFYI   = "fyi"
	NotifyNow   = "now"
)

// Question kinds. Every one parks the thread until Alex answers or writes.
const (
	// AskQuestion wants an answer, from the choices or his own words.
	AskQuestion = "question"
	// AskApproval wants a yes or no before the agent acts.
	AskApproval = "approval"
	// AskSecret wants a credential stored under SecretName. The app stores
	// what he types in his keychain through Open Conduit; the agent only
	// ever sees the reference.
	AskSecret = "secret"
	// AskAction wants him to do something himself (sign in, plug in,
	// click a link at URL) and say when it is done.
	AskAction = "action"
)

var askKinds = []string{AskQuestion, AskApproval, AskSecret, AskAction}

// Namespace is the coordination KV namespace of thread records:
// "question/<work id>" holds the thread's latest Question.
const Namespace = "assist.threads"

// Start is an assist.start Event's data and a thread's Work context.
type Start struct {
	// Text is what the thread is about: Alex's first message, or what the
	// agent opened it to tell him.
	Text string `json:"text"`
	// Title is the thread's short name, when its opener gave one.
	Title string `json:"title,omitempty"`
	// Role says who opened it: the operator, or the agent (assist.spawn).
	Role string `json:"role"`
	// Notify is how loudly an agent-opened thread reaches Alex.
	Notify string `json:"notify,omitempty"`
	// Model is the model the thread runs on, when one was chosen.
	Model string `json:"model,omitempty"`
}

// Message is an assist.message Event's data.
type Message struct {
	Role   string `json:"role"`
	Text   string `json:"text"`
	Notify string `json:"notify,omitempty"`
}

// Question is an assist.question Event's data and the thread's record of
// its latest question.
type Question struct {
	ID      string   `json:"question_id"`
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Context string   `json:"context,omitempty"`
	Choices []string `json:"choices,omitempty"`
	// SecretName is the name to store a secret under (kind secret).
	SecretName string `json:"secret_name,omitempty"`
	// URL is a link Alex needs for the question (kind action, often).
	URL string `json:"url,omitempty"`
	// Deadline is when the question stops waiting (RFC 3339).
	Deadline string `json:"deadline,omitempty"`
	// Sequence is the acceptance watermark when it was asked: its answer
	// comes after it.
	Sequence uint64 `json:"sequence,omitempty"`
}

// Answer is an assist.answer Event's data.
type Answer struct {
	Question string `json:"question_id"`
	Answer   string `json:"answer"`
}

// Status is an assist.status Event's data: the thread's name and a line
// on where it stands, as the agent last said.
type Status struct {
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// ThreadKey is the key of the thread started as id in scope. No Work owns
// it until the route gives assist.start one.
func ThreadKey(scope domain.ID, id string) domain.CorrelationKey {
	return domain.CorrelationKey{Source: Type, ExternalID: "thread/" + string(scope) + "/" + id}
}

// threadID is the thread's ID from its key, when it is one.
func threadID(scope domain.ID, k domain.CorrelationKey) (string, bool) {
	id, ok := strings.CutPrefix(k.ExternalID, "thread/"+string(scope)+"/")
	return id, ok && k.Source == Type && id != "" && !strings.Contains(id, "/")
}

// WorkID is the ID of the thread's Work.
func WorkID(scope domain.ID, id string) domain.ID {
	return domain.ID(Type + "/" + string(scope) + "/" + id)
}

// threadKey is the thread Work's own key.
func threadKey(w domain.Work) (domain.CorrelationKey, bool) {
	for _, k := range w.CorrelationKeys {
		if _, ok := threadID(w.ScopeID, k); ok {
			return k, true
		}
	}
	return domain.CorrelationKey{}, false
}

// Config is a scope's `assist` entry.
type Config struct {
	// Owner is who the assistant works for, as it addresses them.
	Owner string `json:"owner,omitempty"`
}

// ValidateExtension is the entry type's config validator.
func ValidateExtension(config json.RawMessage, secretNames []string) error {
	if len(secretNames) > 0 {
		return errors.New("assist takes no secrets")
	}
	var c Config
	if err := strict(config, &c); err != nil {
		return err
	}
	if len(c.Owner) > 80 || !utf8.ValidString(c.Owner) {
		return errors.New("owner must be a name of at most 80 bytes")
	}
	return nil
}

// ValidateRoute is the route config validator: the route takes none.
func ValidateRoute(config json.RawMessage, _ []string) error {
	var c struct{}
	return strict(config, &c)
}

// secretName is what `open-conduit secret set` accepts as a name.
var secretName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func strict(data []byte, out any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data")
	}
	return nil
}

func payload(schema string, v any) domain.Payload {
	data, _ := json.Marshal(v)
	return domain.Payload{Schema: schema, Data: data}
}

// titleOf is a short title from text: its first line, cut to 80 bytes.
func titleOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if len(line) <= 80 {
		return line
	}
	keep := 77
	for keep > 0 && !utf8.RuneStart(line[keep]) {
		keep--
	}
	return strings.TrimSpace(line[:keep]) + "…"
}
