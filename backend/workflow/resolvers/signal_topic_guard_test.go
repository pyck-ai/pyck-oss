package resolvers_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
)

// signalTopicSeeds are the signal topics both the fuzz target and the
// resolver agreement test start from: the shapes SDK workers register, and
// the hostile shapes a hand-written client can send. "A" is replaced by the
// caller's tenant before use.
var signalTopicSeeds = []string{
	// accepted shapes
	"request.reply.pyck.A.crud.workflow.workflowsignal.*.created",
	"request.reply.pyck.*.crud.workflow.workflowsignal.123e4567-e89b-12d3-a456-426614174000.created",
	"request.reply.pyck.*.crud.workflow.>",
	"request.reply.pyck.*.>",
	"request.reply.*.*.crud.*.*.*.*",
	"*.*.*.*.*.*.*.*.*",
	"pyck.A.temporal.q.t.w.r.s",
	"pyck.*.temporal.q.>",
	"pyck.*.temporal.*.*.*.*.*",
	"  request.reply.pyck.A.crud.workflow.item.*.created  ",
	// empty tokens: the class the subscription-subject guard refuses
	"request.reply.pyck.A.crud..item.*.created",
	"request.reply.pyck.A.crud.workflow..*.created",
	"request.reply.pyck.A.crud.workflow.item.*.",
	"request.reply..A.crud.workflow.item.*.created",
	"request.reply.pyck.*.crud..item.*.created",
	"pyck.A.temporal..t.w.r.s",
	"pyck.*.temporal.q..w.r.s",
	"pyck.*.temporal.q.t.w.r.",
	".request.reply.pyck.A.crud.workflow.item.*.created",
	"request.reply.pyck.A.crud.workflow.item.*.created.",
	"pyck..temporal.q.t.w.r.s",
	// wildcard misuse and characters NATS refuses inside a token
	"request.reply.pyck.A.crud.wo*rk.item.*.created",
	"request.reply.pyck.A.crud.workflow.item.*.cre>ated",
	"request.reply.pyck.A.crud.>.item.*.created",
	"request.reply.pyck.>.>",
	"request.reply.pyck.A.crud.work flow.item.*.created",
	"request.reply.pyck.A.crud.work\tflow.item.*.created",
	"request.reply.pyck.A.crud.work\vflow.item.*.created",
	"request.reply.pyck.A.crud.work\x00flow.item.*.created",
	"request.reply.pyck.A.crud.work\u00a0flow.item.*.created",
	"request.reply.pyck.A.crud.work\u200bflow.item.*.created",
	"request.reply.pyck.A.crud.work\u2028flow.item.*.created",
	"request.reply.pyck.A.crud.Workflow.item.*.created",
	"request.reply.pyck.A.crud.workflow.item.*.\xff",
	// tenant and entity tokens that are UUIDs in another spelling
	"request.reply.pyck.00000000-0000-0000-0000-000000000000.crud.workflow.item.*.created",
	"request.reply.pyck.11111111-1111-1111-1111-111111111111.crud.workflow.item.*.created",
	"request.reply.pyck.A.crud.workflow.item.not-a-uuid.created",
	"pyck.11111111-1111-1111-1111-111111111111.temporal.q.t.w.r.s",
	// The nil UUID is the topic model's wildcard value: Parse reads "*" as
	// uuid.Nil and String renders uuid.Nil as "*", so this entity token is a
	// wildcard by design, the same as the nil-UUID tenant token above, which
	// the resolver expands like "*".
	"request.reply.pyck.A.crud.workflow.item.00000000-0000-0000-0000-000000000000.created",
	"request.reply.pyck.A.crud.workflow.item.123E4567-E89B-12D3-A456-426614174000.created",
	"request.reply.pyck.A.crud.workflow.item.123e4567e89b12d3a456426614174000.created",
	// other topic types and garbage
	"pyck.A.crud.workflow.item.*.created",
	"pyck.custom-events",
	"",
	".",
	"..",
	">",
	"*",
	strings.Repeat("a.", 200) + "a",
}

// withCallerTenant replaces the "A" placeholder of a seed with tenant.
func withCallerTenant(seed string, tenant uuid.UUID) string {
	return strings.ReplaceAll(seed, ".A.", "."+tenant.String()+".")
}

// storedSignalSubject replays the topic checks registerWorkflow runs on one
// signal, in the resolver's order, for a caller whose only tenant is tenant.
// It returns the subject that would be stored ("" when the topic is empty and
// nothing is subscribed) and whether the signal is accepted. guardRefused
// reports a refusal by the subscription-subject check alone, so the fuzz target
// can tell the guard's refusals apart from the earlier checks'.
func storedSignalSubject(natsTopic string, tenant uuid.UUID) (stored string, accepted, guardRefused bool) {
	natsTopic = strings.TrimSpace(natsTopic)
	if natsTopic == "" {
		return "", true, false
	}

	parsed, err := events.Parse(natsTopic)
	if err != nil {
		return "", false, false
	}

	switch parsed.(type) {
	case *events.MutationEventTopic, *events.MutationEventWithReplyTopic, *events.TemporalWorkflowStateChangeTopic:
	default:
		return "", false, false
	}
	if !events.Matchable(natsTopic) {
		return "", false, false
	}
	if !events.IsValidSubscriptionSubject(natsTopic) {
		return "", false, true
	}

	// Legacy request.reply.* registrations are stored in the fire-and-forget
	// form, as the resolver normalises them after the checks above.
	normalized := natsTopic
	if _, ok := parsed.(*events.MutationEventWithReplyTopic); ok {
		normalized = strings.TrimPrefix(natsTopic, "request.reply.")
		if parsed, err = events.Parse(normalized); err != nil {
			return "", false, false
		}
	}

	// A topic that still parses as the legacy form after the strip (nine
	// "*" tokens: the wildcards match the literal request and reply
	// positions, so there is no prefix to strip) can never match a
	// published event and is refused.
	if _, ok := parsed.(*events.MutationEventWithReplyTopic); ok {
		return "", false, false
	}

	// The tenant is read from the topic fields, as GetTenantID reads it, so
	// the replay does not depend on that method's signature.
	var topicTenant uuid.UUID
	switch p := parsed.(type) {
	case *events.MutationEventTopic:
		topicTenant = p.TenantID
	case *events.TemporalWorkflowStateChangeTopic:
		if p.Namespace != "" && p.Namespace != "*" {
			if topicTenant, err = uuid.Parse(p.Namespace); err != nil {
				return "", false, false
			}
		}
	default:
		return "", false, false
	}

	stored = normalized
	switch topicTenant {
	case uuid.Nil:
		stored, err = events.WithTenant(normalized, tenant)
		if err != nil {
			return "", false, false
		}
	case tenant:
	default:
		return "", false, false // the caller holds no role on that tenant
	}
	return stored, true, false
}

// literalPositions are the token positions of each accepted signal topic type
// that hold a fixed word ("request", "crud", "temporal"); every other position
// carries a value the router compares.
var literalPositions = map[events.TopicType]map[int]bool{
	events.TopicTypeMutationEvent:                    {2: true},
	events.TopicTypeTemporalWorkflowStateChangeEvent: {2: true},
}

// hiddenWildcardTokens returns the value tokens of a stored subject that the
// signal router reads as a wildcard although the caller did not write "*" or
// cover them with ">". It compares each token with the topic's canonical
// String() form, which renders every router wildcard (an empty string, the nil
// UUID) as "*". The nil UUID written out is not reported: it is the topic
// model's own spelling of the wildcard, not an empty token.
func hiddenWildcardTokens(t *testing.T, stored string) []string {
	t.Helper()

	parsed, err := events.Parse(stored)
	require.NoError(t, err, "a stored subject must parse for the router: %q", stored)

	parts := strings.Split(stored, ".")
	canonical := strings.Split(parsed.String(), ".")
	literals := literalPositions[parsed.Type()]

	var hidden []string
	for i, part := range parts {
		if part == ">" {
			break
		}
		if literals[i] || part == "*" || part == uuid.Nil.String() {
			continue
		}
		if i < len(canonical) && canonical[i] == "*" {
			hidden = append(hidden, part)
		}
	}
	return hidden
}

// assertStoredSubjectSafe checks what the signal router relies on for a
// subject registerWorkflow stores for a caller whose only tenant is tenant:
// it is a valid subscription subject of an accepted signal type, it is bound
// to the caller's tenant, and no token is a wildcard the caller did not write.
func assertStoredSubjectSafe(t *testing.T, input, stored string, tenant uuid.UUID) {
	t.Helper()

	require.True(t, events.IsValidSubscriptionSubject(stored),
		"accepted %q but stored %q, which is not a valid subscription subject", input, stored)

	parsed, err := events.Parse(stored)
	require.NoError(t, err, "accepted %q but the router cannot parse stored %q", input, stored)

	var gotTenant uuid.UUID
	switch p := parsed.(type) {
	case *events.MutationEventTopic:
		gotTenant = p.TenantID
	case *events.TemporalWorkflowStateChangeTopic:
		gotTenant, err = uuid.Parse(p.Namespace)
		require.NoError(t, err, "accepted %q but stored %q has namespace %q", input, stored, p.Namespace)
	default:
		t.Fatalf("accepted %q but stored %q parses as %s", input, stored, parsed.Type())
	}
	require.Equal(t, tenant, gotTenant,
		"accepted %q but stored %q is bound to another tenant", input, stored)

	require.Empty(t, hiddenWildcardTokens(t, stored),
		"accepted %q but stored %q holds tokens the router reads as wildcards", input, stored)

	published := publishedEventFor(t, parsed, tenant)
	require.True(t, parsed.Matches(published),
		"accepted %q but stored %q matches no event published in the caller's tenant, e.g. %q", input, stored, published.String())
}

// publishedEventFor returns an event topic, in the form a publisher emits it,
// that a stored signal topic must match: every wildcard is filled with a
// concrete value and the tenant is the caller's, so the event is one the
// caller can actually cause.
func publishedEventFor(t *testing.T, signal events.Topic, tenant uuid.UUID) events.Topic {
	t.Helper()

	fill := func(s string) string {
		if s == "" || s == "*" {
			return "x"
		}
		return s
	}
	entity := uuid.MustParse("0198a6b2-0000-7000-8000-0000000000e1")

	var event events.Topic
	switch s := signal.(type) {
	case *events.MutationEventTopic:
		e := *s
		e.StreamName = fill(s.GetStreamName())
		e.TenantID = tenant
		e.ServiceName, e.SchemaName, e.OperationName = fill(s.ServiceName), fill(s.SchemaName), fill(s.OperationName)
		if s.EntityID != uuid.Nil {
			entity = s.EntityID
		}
		e.EntityID = entity
		event = e
	case *events.TemporalWorkflowStateChangeTopic:
		e := *s
		e.StreamName = fill(s.GetStreamName())
		e.Namespace = tenant.String()
		e.TaskQueue, e.WorkflowTypeName, e.WorkflowID = fill(s.TaskQueue), fill(s.WorkflowTypeName), fill(s.WorkflowID)
		e.RunID, e.Status = fill(s.RunID), fill(s.Status)
		event = e
	default:
		t.Fatalf("no published form for %s", signal.Type())
	}

	parsed, err := events.Parse(event.String())
	require.NoError(t, err, "published form %q must parse", event.String())
	return parsed
}

// FuzzSignalTopicGuard guards the hidden-wildcard signal class: a signal topic
// registerWorkflow accepts must be stored as a valid subscription subject,
// bound to the caller's tenant, with no token the signal router reads as a
// wildcard the caller never wrote (an empty token such as "crud..item"). Such
// a signal would start the workflow on events it never named.
//
// It also bounds the guard from the other side: a topic the caller wrote as a
// valid subscription subject is never refused by the subscription-subject
// check, and tenant expansion never turns it into an invalid stored subject.
//
// The target replays the resolver's checks (storedSignalSubject) rather than
// calling the resolver, which needs a database; TestSignalTopicGuardAgreement
// runs the same seeds through the real resolver and fails when the replay
// drifts from it.
//
// Run continuously with:
//
//	go test ./resolvers/ -run '^$' -fuzz FuzzSignalTopicGuard
//
// Under a plain `go test` the seed corpus runs as ordinary regression. The
// package links ent and the Temporal SDK, so expect thousands of execs per
// second, not hundreds of thousands; inputs over 1 KiB are skipped.
func FuzzSignalTopicGuard(f *testing.F) {
	for _, s := range signalTopicSeeds {
		f.Add(withCallerTenant(s, tenantA))
	}

	f.Fuzz(func(t *testing.T, natsTopic string) {
		if len(natsTopic) > 1024 {
			return
		}

		stored, accepted, guardRefused := storedSignalSubject(natsTopic, tenantA)
		if guardRefused {
			require.False(t, events.IsValidSubscriptionSubject(strings.TrimSpace(natsTopic)),
				"the subscription-subject check refused %q, a valid subject", natsTopic)
		}
		if !accepted || stored == "" {
			return // refused, or no subscription: nothing reaches the router
		}

		assertStoredSubjectSafe(t, natsTopic, stored, tenantA)
	})
}

// registerSignalTopic registers one intermediate signal with a topic taken
// verbatim from the caller: TopicJSON is a JSON string literal, which is also
// a valid GraphQL string, so NUL, quotes and invalid UTF-8 reach the resolver
// unchanged instead of breaking the document.
var registerSignalTopic = resolver.ParseTemplate(`mutation {
	registerWorkflow(input: {
		name: "{{.Name}}",
		taskQueue: "test-queue",
		workerID: "test-worker",
		signals: [{
			natsTopic: {{.TopicJSON}},
			temporalSignal: "S",
			temporalSignalType: intermediate,
			filterRule: "true"
		}]
	}) {
		id
	}
}`)

// TestSignalTopicGuardAgreement runs every fuzz seed through the real
// registerWorkflow resolver as a tenant writer and checks that it accepts
// exactly what storedSignalSubject accepts, stores exactly the subject the
// replay predicts, and leaves nothing behind when it refuses.
func TestSignalTopicGuardAgreement(t *testing.T) {
	t.Parallel()

	for i, seed := range signalTopicSeeds {
		topic := withCallerTenant(seed, tenantA)
		if !utf8.ValidString(topic) {
			continue // the GraphQL parser refuses the document before the resolver runs
		}

		// The seed is not the subtest name: the harness derives the SQLite
		// file name from it, and a seed may hold '/' or NUL.
		t.Run(fmt.Sprintf("seed%02d", i), func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			ctx := te.ctx(userWriter)

			wantStored, wantAccepted, _ := storedSignalSubject(topic, tenantA)

			topicJSON, err := json.Marshal(topic)
			require.NoError(t, err)
			res := resolver.Exec[json.RawMessage, *ent.Client](te.TestEnvironment, ctx, registerSignalTopic, map[string]any{
				"Name":      "wf_signal_topic_guard",
				"TopicJSON": string(topicJSON),
			})

			if !wantAccepted {
				require.NotEmpty(t, res.Errors, "the resolver accepted %q, which the replay refuses", topic)
				assert.NotContains(t, res.Errors[0].Message, "internal system error",
					"%q must be refused by a check, not by a recovered panic", topic)
				assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
				assert.Empty(t, te.Ent.Workflow.Query().AllX(ctx))
				te.assertNoEvents(ctx)
				return
			}

			require.Empty(t, res.Errors, "the resolver refused %q, which the replay accepts: %v", topic, res.Errors)
			got := te.Ent.WorkflowSignal.Query().Select(entworkflowsignal.FieldNatsTopic).StringsX(ctx)
			if wantStored == "" {
				assert.Empty(t, got)
				return
			}
			require.Equal(t, []string{wantStored}, got)
			assertStoredSubjectSafe(t, topic, got[0], tenantA)
		})
	}
}
