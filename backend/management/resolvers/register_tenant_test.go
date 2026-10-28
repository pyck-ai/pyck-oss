package resolvers_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	entgo "entgo.io/ent"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/lib/pq/pqerror"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"golang.org/x/crypto/bcrypt"

	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enthook "github.com/pyck-ai/pyck/backend/management/ent/gen/hook"
	entintercept "github.com/pyck-ai/pyck/backend/management/ent/gen/intercept"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/model"
	"github.com/pyck-ai/pyck/backend/management/resolvers"
)

// countTenantCreates counts every tenant insert the client attempts,
// whether or not the database accepts it: a refusal the pre-flight owns
// must not even reach the insert.
func (te *testEnv) countTenantCreates() *atomic.Int32 {
	te.t.Helper()
	var n atomic.Int32
	te.Ent.Tenant.Use(enthook.On(func(next ent.Mutator) ent.Mutator {
		return enthook.TenantFunc(func(ctx context.Context, m *ent.TenantMutation) (ent.Value, error) {
			n.Add(1)
			return next.Mutate(ctx, m)
		})
	}, ent.OpCreate))
	return &n
}

// countPasswordHashes counts the admin password hashes registerTenant
// computes from here on; the hash itself is still bcrypt's.
func (te *testEnv) countPasswordHashes() *atomic.Int32 {
	te.t.Helper()
	var n atomic.Int32
	resolvers.SetHashPassword(te.resolver, func(password []byte, cost int) ([]byte, error) {
		n.Add(1)
		return bcrypt.GenerateFromPassword(password, cost)
	})
	return &n
}

// assertNoRegistrationSideEffects checks a refused registerTenant left
// nothing behind: no row, no outbox event, no wait on a run.
func (te *testEnv) assertNoRegistrationSideEffects(rowsBefore int) {
	te.t.Helper()
	n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
	require.NoError(te.t, err)
	assert.Equal(te.t, rowsBefore, n, "a refused registration inserts no row")
	te.assertNoEvents(te.ctx(systemUser))
	te.TemporalClient.AssertNotCalled(te.t, "GetWorkflow", mock.Anything, mock.Anything, mock.Anything)
}

// =============================================================================
// FEATURE HEADERS
// =============================================================================

// registerTenant is open to anonymous callers and the gateway forwards
// X-Pyck-Feature, so every feature is a switch an attacker can flip on the
// row's own transaction: suppressevents skips the outbox write, leaving a
// pending row no run is ever started for. Any feature header is refused
// before validation, hashing and the database.
func TestRegisterTenant_RefusesAnyFeatureHeader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ctx  func(te *testEnv) context.Context
	}{
		{"suppressevents", func(te *testEnv) context.Context {
			return feature.Context(resolver.WithoutForcedFeatures(te.ctx(userA)), feature.FEATURE_SUPPRESS_EVENTS)
		}},
		{"syncupdates", func(te *testEnv) context.Context {
			// The harness default: every SendQuery carries syncupdates.
			return te.ctx(userA)
		}},
		{"showdeleted", func(te *testEnv) context.Context {
			return feature.Context(resolver.WithoutForcedFeatures(te.ctx(userA)), feature.FEATURE_SHOW_DELETED)
		}},
		{"showdeleted and suppressevents", func(te *testEnv) context.Context {
			return feature.Context(resolver.WithoutForcedFeatures(te.ctx(userA)),
				feature.FEATURE_SHOW_DELETED, feature.FEATURE_SUPPRESS_EVENTS)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			creates := te.countTenantCreates()

			msg := execErrMessage(te, tc.ctx(te), registerTenant, registerInput)

			assert.Contains(t, msg, "registerTenant accepts no X-Pyck-Feature header")
			assert.Zero(t, creates.Load())
			te.assertNoRegistrationSideEffects(0)
		})
	}
}

// The feature refusal comes first: input that would fail validation or
// hashing is not looked at, so the refusal cannot be probed around.
func TestRegisterTenant_FeatureRefusalPrecedesValidation(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	ctx := feature.Context(te.ctx(userA), feature.FEATURE_SUPPRESS_EVENTS)
	input := registerTenantInput
	input.AdminPassword = "weak"

	resp, err := te.resolver.Mutation().RegisterTenant(ctx, input)

	require.ErrorIs(t, err, resolvers.ErrRegistrationFeaturesRefused)
	assert.NotContains(t, err.Error(), "admin password")
	assert.Nil(t, resp)
	te.assertNoRegistrationSideEffects(0)
}

// Without any feature header the same request registers: the refusal is
// about the header, not about the caller or the input.
func TestRegisterTenant_RegistersWithoutAFeatureHeader(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	reg := te.mockRegistration(nil, te.finishRegistration)
	data := execOK[registerTenantData](te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, registerInput)

	assert.True(t, data.RegisterTenant.Success)
	require.NotNil(t, reg.row)
	te.assertEvents(te.ctx(systemUser), Create("tenant", reg.TenantID), Update("tenant", reg.TenantID))
}

// =============================================================================
// CASE-INSENSITIVE NAMES, RESERVED ACROSS ALL ROWS
// =============================================================================

// Zitadel compares org names case-insensitively, so "acme" next to a live
// "Acme" would pass a case-sensitive pre-flight and end as a permanent
// error row. The pre-flight folds case and refuses before any insert.
func TestRegisterTenant_RefusesANameThatDiffersOnlyInCase(t *testing.T) {
	t.Parallel()

	for _, status := range []enttenant.Status{enttenant.StatusDone, enttenant.StatusPending, enttenant.StatusError} {
		for _, name := range []string{"acme", "ACME", "aCmE", "Acme"} {
			t.Run(status.String()+"/"+name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				existingID := uuidgql.GenerateV7UUID()
				te.newTenant(te.ctxForTenant(systemUser, existingID), existingID).Name("Acme").Status(status).Create()
				te.clearEvents(te.ctx(systemUser))
				creates := te.countTenantCreates()
				hashes := te.countPasswordHashes()

				input := registerTenantInput
				input.Name = name
				resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), input)

				require.ErrorIs(t, err, resolvers.ErrTenantExists)
				assert.Nil(t, resp)
				assert.Zero(t, creates.Load(), "the pre-flight refuses before the insert")
				assert.Zero(t, hashes.Load(), "the pre-flight refuses before the bcrypt hash")
				te.assertNoRegistrationSideEffects(1)
			})
		}
	}
}

// A retired tenant keeps its name for good: otherwise anyone could
// register a disabled tenant's name and block its restoreTenant with a
// duplicate. The pre-flight counts soft-deleted rows,
// in any case.
func TestRegisterTenant_RefusesTheNameOfASoftDeletedTenant(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Acme", "ACME"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			retiredID := uuidgql.GenerateV7UUID()
			te.newTenant(te.ctxForTenant(systemUser, retiredID), retiredID).Name("Acme").Deleted().Create()
			te.clearEvents(te.ctx(systemUser))
			creates := te.countTenantCreates()
			hashes := te.countPasswordHashes()

			msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": name}))

			assert.Equal(t, "tenant with the provided name already exists", msg)
			assert.Zero(t, creates.Load(), "the pre-flight refuses before the insert")
			assert.Zero(t, hashes.Load(), "the pre-flight refuses before the bcrypt hash")
			te.assertNoRegistrationSideEffects(1)

			row := mustGetTenant(t, te, retiredID)
			assert.False(t, row.DeletedAt.IsZero(), "the retired tenant is untouched")
		})
	}
}

// Two registrations whose names differ only in case and that both pass
// the pre-flight are decided by the unique index on name_key; the
// loser sees the same friendly refusal. The competitor is committed
// between the pre-flight and the pending insert.
func TestRegisterTenant_CaseVariantRaceIsDecidedByTheIndex(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	raced := false
	te.Ent.Tenant.Use(enthook.On(func(next ent.Mutator) ent.Mutator {
		return enthook.TenantFunc(func(ctx context.Context, m *ent.TenantMutation) (ent.Value, error) {
			if raced {
				return next.Mutate(ctx, m)
			}
			raced = true
			tx, err := m.Tx()
			if err != nil {
				return nil, err
			}
			competitorID := uuidgql.GenerateV7UUID()
			if err := tx.Tenant.Create().
				SetID(competitorID).
				SetName("TEST TENANT").
				SetIdpOrgRef(competitorID.String()).
				Exec(ctx); err != nil {
				return nil, fmt.Errorf("insert competitor: %w", err)
			}
			return next.Mutate(ctx, m)
		})
	}, ent.OpCreate))

	resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), registerTenantInput)

	require.True(t, raced)
	require.ErrorIs(t, err, resolvers.ErrTenantExists)
	require.NotErrorIs(t, err, resolvers.ErrRegistrationInternal, "a constraint violation keeps its own answer")
	assert.Nil(t, resp)
	te.TemporalClient.AssertNotCalled(t, "GetWorkflow", mock.Anything, mock.Anything, mock.Anything)
}

// =============================================================================
// FINAL READ AFTER THE RUN CLOSED
// =============================================================================

// interceptTenantReads runs onRead after every tenant read once armed is
// set; onRead sees the row the read returned.
func (te *testEnv) interceptTenantReads(armed *atomic.Bool, onRead func(*ent.Tenant)) {
	te.t.Helper()
	te.Ent.Tenant.Intercept(entgo.InterceptFunc(func(next entgo.Querier) entgo.Querier {
		return entgo.QuerierFunc(func(ctx context.Context, q entgo.Query) (entgo.Value, error) {
			v, err := next.Query(ctx, q)
			if err != nil || !armed.Load() {
				return v, err
			}
			if rows, ok := v.([]*ent.Tenant); ok && len(rows) == 1 {
				onRead(rows[0])
			}
			return v, err
		})
	}))
}

// The final read goes to the reader pool, which can lag the writer the
// run's done flip went to. A registration that succeeded must not be
// reported pending because the first read hit a stale replica: the row is
// read again until it leaves pending. Here the first read after the run
// closed still sees pending, and the "replica" catches up right after it.
func TestRegisterTenant_RereadsARowAStaleReplicaStillReportsPending(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)
	resolvers.SetRegistrationWait(te.resolver, 5*time.Second, 10*time.Millisecond)

	var armed atomic.Bool
	var reads atomic.Int32
	reg := te.mockRegistration(nil, func(*registration) { armed.Store(true) })
	te.interceptTenantReads(&armed, func(row *ent.Tenant) {
		reads.Add(1)
		if row.Status == enttenant.StatusPending {
			// The replica replays the run's done flip after this read.
			armed.Store(false)
			te.finishRegistration(reg)
			armed.Store(true)
		}
	})

	data := execOK[registerTenantFullData](te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenantFull, withInput(map[string]any{
		"ExpiresAt": time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"Flavour":   "plain",
	}))

	assert.True(t, data.RegisterTenant.Success)
	assert.Equal(t, "done", data.RegisterTenant.Tenant.Status)
	assert.GreaterOrEqual(t, reads.Load(), int32(2), "the stale pending read must be repeated")
}

// A row that stays pending after the run succeeded is re-read until the
// registration wait is spent, then reported as an unknown outcome, not as
// a success, not after a single look, and not as a failure: the run
// succeeded and only the reader lags, so a client told "failed" would
// register again and be refused its own name.
func TestRegisterTenant_ReportsARowThatStaysPendingAfterTheWait(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)
	// The wait is long enough for a second read on a loaded runner. The
	// bounds below count reads instead of timing the call: the call also
	// spends the bcrypt hash and the insert, which a slow runner stretches
	// far past the wait.
	const wait, poll = time.Second, 10 * time.Millisecond
	resolvers.SetRegistrationWait(te.resolver, wait, poll)

	var armed atomic.Bool
	var reads atomic.Int32
	reg := te.mockRegistration(nil, func(*registration) { armed.Store(true) })
	te.interceptTenantReads(&armed, func(*ent.Tenant) { reads.Add(1) })

	resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), registerTenantInput)

	require.ErrorIs(t, err, resolvers.ErrRegistrationOutcomeUnknown)
	require.NotErrorIs(t, err, resolvers.ErrRegistrationUnsettled)
	assert.Nil(t, resp)
	assert.GreaterOrEqual(t, reads.Load(), int32(2), "the pending row is read again, not reported after a single look")
	// Every re-read waits at least one poll and none starts after the wait
	// ends, so the count is capped whatever the runner's speed.
	assert.LessOrEqual(t, reads.Load(), int32(wait/poll)+1, "the re-reads are bounded by the registration wait")

	row := mustGetTenant(t, te, reg.TenantID)
	assert.Equal(t, enttenant.StatusPending, row.Status, "the resolver never flips the row itself")
}

// =============================================================================
// FAILURE ANSWERS CARRY NO TEMPORAL IDENTIFIERS
// =============================================================================

const (
	leakWorkflowID = "register-tenant-0199c1d2-leak-4c3e-9b1a-000000000001"
	leakIdentity   = "1@44e62333c8ca@"
	leakDialError  = "dial tcp 10.1.2.3:8080: connect: connection refused"

	// leakDependencyText is what a dependency's raw error looks like inside
	// an application error: a request path, a secret's key name, a response
	// body. None of it may reach an anonymous registrant.
	leakDependencyText = `PUT https://worker-api.internal:8443/api/v1/namespaces/ns-7/workers: 500: secret "pyck-worker-credentials" key "service-token" missing: {"error":"boom"}`
)

// failingStepWorkflow runs one activity registered under the name of a
// real registration step, the way the run fails on a step.
func failingStepWorkflow(ctx workflow.Context) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 100 * time.Millisecond,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	return workflow.ExecuteActivity(ctx, "CreateTenantActivity").Get(ctx, nil)
}

// stepFailure returns the error WorkflowRun.Get hands back for a run that
// failed on CreateTenantActivity with activityErr, under a workflow ID,
// run ID and worker identity whose appearance in an answer is a leak.
func stepFailure(t *testing.T, activityFn any) error {
	t.Helper()
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.SetIdentity(leakIdentity)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: leakWorkflowID})
	// The SDK's 1 s deadlock detector fires on a CPU-starved runner and
	// replaces the activity failure this helper exists to produce with a
	// TMPRL1101 panic, so the identifiers below go missing. The workflow
	// only schedules one activity; nothing here can actually deadlock.
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Minute})
	env.RegisterWorkflow(failingStepWorkflow)
	env.RegisterActivityWithOptions(activityFn, activity.RegisterOptions{Name: "CreateTenantActivity"})
	env.ExecuteWorkflow(failingStepWorkflow)
	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)
	// The chain itself carries the identifiers; the answer must not.
	require.Contains(t, err.Error(), leakWorkflowID)
	require.Contains(t, err.Error(), leakIdentity)
	return err
}

// registerTenantAgainst runs a registration whose run fails with wfErr after
// marking the row error, and returns the error the anonymous caller sees.
func registerTenantAgainst(t *testing.T, wfErr error) string {
	t.Helper()
	te := setup(t)
	defer te.Close(t)
	te.mockRegistration(wfErr, func(reg *registration) {
		te.mutateTenant(te.ctxForTenant(systemUser, reg.TenantID), reg.TenantID, func(u *ent.TenantUpdateOne) *ent.TenantUpdateOne {
			return u.SetStatus(enttenant.StatusError)
		})
	})
	return execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, registerInput)
}

// registerTenant answers anonymous callers. A failed run must be reported
// by its step and a fixed text only; Temporal's own error text names the
// workflow ID, the run ID and the worker's host identity, and the
// application error's message is the dependency's raw error, all of which
// stay in the log.
func TestRegisterTenant_FailureAnswerCarriesNoTemporalIdentifiers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		activity any
		want     []string
	}{
		{
			name: "application error",
			activity: func(context.Context) error {
				return temporal.NewNonRetryableApplicationError("organization name taken", "org-rejected", nil)
			},
			want: []string{"step CreateTenantActivity", "provisioning failed"},
		},
		{
			name: "start-to-close timeout",
			activity: func(ctx context.Context) error {
				<-ctx.Done()
				time.Sleep(200 * time.Millisecond)
				return ctx.Err()
			},
			want: []string{"step CreateTenantActivity", "provisioning failed"},
		},
		{
			// The server's timeout failure wraps the last attempt's
			// failure; its message is the dependency's raw error.
			name: "schedule-to-close timeout after a failed attempt",
			activity: func(context.Context) error {
				return temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE,
					temporal.NewApplicationError(leakDialError, "zitadel-unavailable"))
			},
			want: []string{"step CreateTenantActivity", "provisioning failed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			msg := registerTenantAgainst(t, stepFailure(t, tc.activity))

			assert.Contains(t, msg, "tenant row marked error", "the verdict on the row stays")
			for _, want := range tc.want {
				assert.Contains(t, msg, want)
			}
			for _, leak := range []string{leakWorkflowID, "default-test-run-id", leakIdentity, "workflowID", "runID", "identity", "@", leakDialError, "zitadel-unavailable", "organization name taken", "org-rejected"} {
				assert.NotContains(t, msg, leak)
			}
		})
	}
}

// blockingWorkflow waits until it is cancelled, the way a registration run
// ends when deleteTenant terminates it under a concurrent registerTenant.
func blockingWorkflow(ctx workflow.Context) error {
	return workflow.Await(ctx, func() bool { return false })
}

// cancelledRun returns the error WorkflowRun.Get hands back for a run that
// was cancelled before any step failed: a WorkflowExecutionError that
// carries neither an ActivityError nor an ApplicationError, under a
// workflow ID whose appearance in an answer is a leak.
func cancelledRun(t *testing.T) error {
	t.Helper()
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.SetIdentity(leakIdentity)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: leakWorkflowID})
	env.RegisterWorkflow(blockingWorkflow)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
	env.ExecuteWorkflow(blockingWorkflow)
	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	var wfErr *temporal.WorkflowExecutionError
	require.ErrorAs(t, err, &wfErr, "the resolver only reads the row after a closed run")
	var canceled *temporal.CanceledError
	require.ErrorAs(t, err, &canceled)
	var appErr *temporal.ApplicationError
	require.NotErrorAs(t, err, &appErr, "the run must carry no application error")
	var actErr *temporal.ActivityError
	require.NotErrorAs(t, err, &actErr, "the run must carry no step")
	// The chain itself carries the identifiers; the answer must not.
	require.Contains(t, err.Error(), leakWorkflowID)
	return err
}

// A run that ends without a failing step (terminated or cancelled while
// the row is still pending, e.g. by a concurrent deleteTenant) has nothing
// to report but the unsettled verdict; Temporal's error text, which names
// the workflow ID and run ID, stays in the log.
func TestRegisterTenant_TerminatedRunAnswerCarriesNoTemporalIdentifiers(t *testing.T) {
	t.Parallel()

	te := setup(t)
	defer te.Close(t)
	resolvers.SetRegistrationWait(te.resolver, 200*time.Millisecond, 10*time.Millisecond)
	reg := te.mockRegistration(cancelledRun(t))

	msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, registerInput)

	assert.Contains(t, msg, "ended before the tenant row settled")
	for _, leak := range []string{leakWorkflowID, "default-test-run-id", leakIdentity, "workflow execution error", "canceled", "@"} {
		assert.NotContains(t, msg, leak)
	}
	row := mustGetTenant(t, te, reg.TenantID)
	assert.Equal(t, enttenant.StatusPending, row.Status, "the resolver never flips the row itself")
}

// A workflow failure with no step behind it still answers without the
// workflow error's identifiers.
func TestRegisterTenant_WorkflowFailureAnswerCarriesNoWorkflowID(t *testing.T) {
	t.Parallel()

	msg := registerTenantAgainst(t, workflowFailure(t, "provisioning boom"))

	assert.Contains(t, msg, "tenant row marked error")
	assert.NotContains(t, msg, "default-test-workflow-id")
	assert.NotContains(t, msg, "default-test-run-id")
	assert.NotContains(t, msg, "workflow execution error", "Temporal's wrapper text must not reach the caller")
}

// =============================================================================
// DELETE A PENDING TENANT
// =============================================================================

// deleteTenant never touches a registration run: it retires the row in its
// own transaction and nothing else, whatever the row's status. A run still
// provisioning notices the deleted row itself and deactivates its org, so
// the delete makes no Temporal call (the mock fails the test on an
// unexpected one) and works while Temporal is down.
func TestDeleteTenant_RetiresTheRowWithoutTouchingItsRun(t *testing.T) {
	t.Parallel()

	for _, status := range []enttenant.Status{enttenant.StatusPending, enttenant.StatusDone, enttenant.StatusError} {
		t.Run(status.String(), func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			tenantID := uuidgql.GenerateV7UUID()
			ctx := te.ctxForTenant(systemUser, tenantID)
			te.newTenant(ctx, tenantID).Status(status).Registration(testRegistrationData()).Create()
			te.clearEvents(ctx)

			data := execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(tenantID, nil))

			assert.True(t, data.DeleteTenant.Success)
			te.TemporalClient.AssertNotCalled(t, "TerminateWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			got := mustGetTenant(t, te, tenantID)
			assert.False(t, got.DeletedAt.IsZero())
			assert.Nil(t, got.Registration, "the registration data does not outlive the delete")
			te.assertEvents(ctx, Delete("tenant", tenantID))
		})
	}
}

// A writer of the tenant and another tenant's admin are refused before the
// row changes.
func TestDeleteTenant_RefusedCallerLeavesThePendingRow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		target uuid.UUID
		ctx    func(te *testEnv) context.Context
	}{
		{"writer of the tenant", resolver.TenantA, func(te *testEnv) context.Context { return te.ctx(userAWriter) }},
		{"admin of another tenant", resolver.TenantB, func(te *testEnv) context.Context { return te.ctx(userA) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			te.newTenant(te.ctxForTenant(systemUser, tc.target), tc.target).
				Status(enttenant.StatusPending).
				Registration(testRegistrationData()).
				Create()

			execErr(te, tc.ctx(te), deleteTenantMut, tenantArgs(tc.target, nil), "admin role required")

			got := mustGetTenant(t, te, tc.target)
			assert.True(t, got.DeletedAt.IsZero())
			assert.Equal(t, enttenant.StatusPending, got.Status)
			assert.NotNil(t, got.Registration)
		})
	}
}

// =============================================================================
// INPUT ZITADEL WOULD TRIM OR REFUSE
// =============================================================================

// countTenantReads counts every tenant query the client runs, the
// registerTenant name pre-flight among them.
func (te *testEnv) countTenantReads() *atomic.Int32 {
	te.t.Helper()
	var n atomic.Int32
	te.Ent.Tenant.Intercept(entintercept.TraverseTenant(func(context.Context, *ent.TenantQuery) error {
		n.Add(1)
		return nil
	}))
	return &n
}

// Zitadel trims an org name and the user's names before it stores them,
// and Postgres refuses NUL in text: " Acme" next to a live "Acme" passes
// the name index and collides inside Zitadel, a whitespace-only first name
// fails user import with the org already created, and a NUL reaches the
// driver. A decomposed password would register bytes no keyboard sends at
// login. Each is refused before the name pre-flight and before any row,
// event or run, and the refusal is the validation's, never driver text.
func TestRegisterTenant_RefusesUntrimmedBlankOrControlInputBeforeThePreflight(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		spoil func(*model.RegisterTenantInput)
		want  string
	}{
		{"leading space in the name", func(in *model.RegisterTenantInput) { in.Name = " Acme" }, "name has leading or trailing whitespace"},
		{"trailing NBSP in the name", func(in *model.RegisterTenantInput) { in.Name = "Acme\u00a0" }, "name has leading or trailing whitespace"},
		{"whitespace-only first name", func(in *model.RegisterTenantInput) { in.AdminFirstName = " \u2003 " }, "adminFirstName is blank"},
		{"whitespace-only last name", func(in *model.RegisterTenantInput) { in.AdminLastName = "\t" }, "adminLastName is blank"},
		{"NUL in the name", func(in *model.RegisterTenantInput) { in.Name = "Ac\x00me" }, "name contains a control character"},
		{"tab inside the username", func(in *model.RegisterTenantInput) { in.AdminUsername = "acme\tadmin" }, "adminUsername contains a control character"},
		{"trailing space in the e-mail", func(in *model.RegisterTenantInput) { in.AdminEmail = "admin@acme.test " }, "adminEmail has leading or trailing whitespace"},
		{"NUL in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = "SecurePass123!\x00" }, "adminPassword contains a control character"},
		{"tab in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = "Secure\tPass123!" }, "adminPassword contains a control character"},
		{"decomposed umlaut in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = "SecurePasso\u0308123!" }, "adminPassword is not in composed Unicode form (NFC)"},
		// Zitadel's e-mail host rule refuses these at user import, after
		// the org exists.
		{"IDN host in the e-mail", func(in *model.RegisterTenantInput) { in.AdminEmail = "admin@b\u00fccher.test" }, "other than an ASCII letter, digit or hyphen"},
		{"IP literal in the e-mail", func(in *model.RegisterTenantInput) { in.AdminEmail = "admin@[127.0.0.1]" }, "other than an ASCII letter, digit or hyphen"},
		{"edge hyphen in the e-mail host", func(in *model.RegisterTenantInput) { in.AdminEmail = "admin@acme-.test" }, "begins or ends with a hyphen"},
		{"64-byte e-mail host label", func(in *model.RegisterTenantInput) { in.AdminEmail = "admin@" + strings.Repeat("a", 64) + ".test" }, "host name label must have 1 to 63 bytes"},
		{"65-byte e-mail local part", func(in *model.RegisterTenantInput) { in.AdminEmail = strings.Repeat("a", 65) + "@acme.test" }, "local part has more than 64 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			// A live tenant whose name the untrimmed variant would collide
			// with inside Zitadel.
			existingID := uuidgql.GenerateV7UUID()
			te.newTenant(te.ctxForTenant(systemUser, existingID), existingID).Name("Acme").Create()
			te.clearEvents(te.ctx(systemUser))
			reads := te.countTenantReads()
			creates := te.countTenantCreates()
			// Only reached when the input is wrongly let through.
			te.mockRegistration(nil, te.finishRegistration)

			// Called directly: a NUL cannot travel in the test harness's
			// GraphQL document; the wire path is covered end to end.
			input := registerTenantInput
			tc.spoil(&input)
			resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), input)
			require.Error(t, err)
			assert.Nil(t, resp)
			msg := err.Error()

			assert.Contains(t, msg, "must be non-blank, carry no leading or trailing whitespace and no control characters")
			assert.Contains(t, msg, tc.want)
			assert.NotContains(t, msg, "pq:")
			assert.NotContains(t, msg, "already exists", "the refusal is the input's, not the name index's")
			assert.NotContains(t, msg, "SecurePass123!", "the password is never echoed")
			assert.Zero(t, reads.Load(), "refused before the name pre-flight reads anything")
			assert.Zero(t, creates.Load(), "refused before any insert")
			te.assertNoRegistrationSideEffects(1)
		})
	}
}

// =============================================================================
// NAMES FOLDED THE WAY ZITADEL FOLDS THEM
// =============================================================================

// Postgres' lower() under a libc locale leaves the circled letters
// unfolded; Zitadel's Go fold lowercases them, so "ⓐcme-x" next to a live
// "Ⓐcme-x" is the same org to Zitadel and would end as a permanent error
// row. The pre-flight compares name_key and refuses before the insert.
func TestRegisterTenant_RefusesANameZitadelFoldsToAnExistingOne(t *testing.T) {
	t.Parallel()

	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing row deleted=%t", deleted), func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			existingID := uuidgql.GenerateV7UUID()
			b := te.newTenant(te.ctxForTenant(systemUser, existingID), existingID).Name("Ⓐcme-x")
			if deleted {
				b = b.Deleted()
			}
			b.Create()
			te.clearEvents(te.ctx(systemUser))
			creates := te.countTenantCreates()
			// Only reached when the name is wrongly let through.
			te.mockRegistration(nil, te.finishRegistration)

			input := registerTenantInput
			input.Name = "ⓐcme-x"
			resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), input)

			require.ErrorIs(t, err, resolvers.ErrTenantExists)
			assert.Nil(t, resp)
			assert.Zero(t, creates.Load(), "the pre-flight refuses before the insert")
			n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
			require.NoError(t, err)
			assert.Equal(t, 1, n, "no second row holds the folded name")
		})
	}
}

// A Go-folded pair that both pass the pre-flight is decided by the unique
// name_key index: the loser's insert fails and the client sees the
// pre-flight's refusal. The competitor is committed between the pre-flight
// and the pending insert.
func TestRegisterTenant_GoFoldedRaceIsDecidedByTheNameKeyIndex(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	raced := false
	te.Ent.Tenant.Use(enthook.On(func(next ent.Mutator) ent.Mutator {
		return enthook.TenantFunc(func(ctx context.Context, m *ent.TenantMutation) (ent.Value, error) {
			if raced {
				return next.Mutate(ctx, m)
			}
			raced = true
			tx, err := m.Tx()
			if err != nil {
				return nil, err
			}
			competitorID := uuidgql.GenerateV7UUID()
			if err := tx.Tenant.Create().
				SetID(competitorID).
				SetName("ⓐcme race").
				SetIdpOrgRef(competitorID.String()).
				Exec(ctx); err != nil {
				return nil, fmt.Errorf("insert competitor: %w", err)
			}
			return next.Mutate(ctx, m)
		})
	}, ent.OpCreate))
	// Only reached when the index wrongly lets both rows in.
	te.mockRegistration(nil, te.finishRegistration)

	input := registerTenantInput
	input.Name = "ⒶCME RACE"
	resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), input)

	require.True(t, raced)
	require.ErrorIs(t, err, resolvers.ErrTenantExists)
	assert.Nil(t, resp)
}

// =============================================================================
// FAILURE ANSWERS CARRY NO DEPENDENCY TEXT
// =============================================================================

// leakFragments are the parts of leakDependencyText that must not appear
// in an answer.
var leakFragments = []string{"worker-api.internal", "/api/v1", "pyck-worker-credentials", "service-token", `"error"`, "boom", "500"}

// A step refused with a type the caller can act on is answered with the
// step and a fixed text for that type; the application error's own
// message, the dependency's raw error, stays in the log.
func TestRegisterTenant_FailureAnswerIsAFixedTextPerKnownType(t *testing.T) {
	t.Parallel()

	cases := []struct{ errType, want string }{
		{"organization-name-taken", "organization-name-taken: an organization with this tenant name already exists"},
		{"organization-invalid", "organization-invalid: the tenant name was refused"},
		// Answered as a refused account: its own answer would tell any
		// caller that the username exists somewhere on the instance.
		{"admin-username-taken", "admin-account-invalid: the admin account was refused"},
		{"admin-account-invalid", "admin-account-invalid: the admin account was refused"},
		// SetOrgMetadataActivity's answer to Zitadel refusing a key or value
		// of the caller's tenant data (InvalidArgument).
		{"org-metadata-invalid", "org-metadata-invalid: the tenant data was refused"},
	}
	for _, tc := range cases {
		t.Run(tc.errType, func(t *testing.T) {
			t.Parallel()

			msg := registerTenantAgainst(t, stepFailure(t, func(context.Context) error {
				return temporal.NewNonRetryableApplicationError(leakDependencyText, tc.errType, nil)
			}))

			assert.Contains(t, msg, "tenant row marked error")
			assert.True(t, strings.HasSuffix(msg, ": step CreateTenantActivity, "+tc.want), "answer %q", msg)
			for _, leak := range leakFragments {
				assert.NotContains(t, msg, leak)
			}
		})
	}
}

// Every other failure (an error type the caller cannot act on (the
// worker-api's StatusError among them), a typed refusal that only arrived
// wrapped in a timeout, an error without a type) is answered with the
// step and the generic text; neither the type nor the message is named.
func TestRegisterTenant_FailureAnswerIsGenericForEveryOtherFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		activity  any
		forbidden []string
	}{
		{
			name: "worker-api status error",
			activity: func(context.Context) error {
				return temporal.NewNonRetryableApplicationError(leakDependencyText, "worker-api-rejected", nil)
			},
			forbidden: []string{"worker-api-rejected"},
		},
		{
			name: "tenant sync rejected",
			activity: func(context.Context) error {
				return temporal.NewNonRetryableApplicationError(leakDependencyText, "tenant-sync-rejected", nil)
			},
			forbidden: []string{"tenant-sync-rejected"},
		},
		{
			// SetOrgMetadataActivity's answer to a service account that lost
			// the metadata grant: the caller's data was never refused.
			name: "an operator-side org metadata fault",
			activity: func(context.Context) error {
				return temporal.NewNonRetryableApplicationError(
					"rpc error: code = PermissionDenied desc = No matching permissions found (AUTH-5mWD2)", "org-metadata-rejected", nil)
			},
			forbidden: []string{"org-metadata-rejected", "tenant data", "PermissionDenied", "AUTH-5mWD2"},
		},
		{
			// CreateZitadelUserActivity's answer to Zitadel refusing pyck's
			// own bcrypt hash encoding (USER-JDk4t): a Zitadel configured
			// without it, which no change of the caller's input fixes.
			name: "an unsupported admin password hash",
			activity: func(context.Context) error {
				return temporal.NewNonRetryableApplicationError(
					"rpc error: code = InvalidArgument desc = Password hash encoding not supported (USER-JDk4t)", "admin-hash-unsupported", nil)
			},
			forbidden: []string{"admin-hash-unsupported", "USER-JDk4t", "hash", "admin account", "was refused", "InvalidArgument"},
		},
		{
			name: "a caller-actionable type wrapped in a timeout",
			activity: func(context.Context) error {
				return temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE,
					temporal.NewApplicationError(leakDependencyText, "organization-name-taken"))
			},
			forbidden: []string{"organization-name-taken", "already exists"},
		},
		{
			name: "an untyped error",
			activity: func(context.Context) error {
				return errors.New(leakDependencyText)
			},
			forbidden: []string{"errorString"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			msg := registerTenantAgainst(t, stepFailure(t, tc.activity))

			assert.Contains(t, msg, "tenant row marked error")
			assert.True(t, strings.HasSuffix(msg, ": step CreateTenantActivity, provisioning failed"), "answer %q", msg)
			for _, leak := range append(leakFragments, tc.forbidden...) {
				assert.NotContains(t, msg, leak)
			}
		})
	}
}

// A run that failed on a step while its row stayed pending (the status
// flip after the failed step did not land) is answered with the unsettled
// verdict and the same fixed text, never the dependency's message.
func TestRegisterTenant_UnsettledAnswerCarriesNoDependencyText(t *testing.T) {
	t.Parallel()

	te := setup(t)
	defer te.Close(t)
	resolvers.SetRegistrationWait(te.resolver, 200*time.Millisecond, 10*time.Millisecond)
	reg := te.mockRegistration(stepFailure(t, func(context.Context) error {
		return temporal.NewNonRetryableApplicationError(leakDependencyText, "admin-account-invalid", nil)
	}))

	msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, registerInput)

	assert.Contains(t, msg, "ended before the tenant row settled")
	assert.True(t, strings.HasSuffix(msg, ": step CreateTenantActivity, admin-account-invalid: the admin account was refused"), "answer %q", msg)
	for _, leak := range leakFragments {
		assert.NotContains(t, msg, leak)
	}
	assert.Equal(t, enttenant.StatusPending, mustGetTenant(t, te, reg.TenantID).Status)
}

// An outcome the resolver could not observe (the result stream reset,
// the wait's deadline, Temporal unavailable) is answered with the fixed
// unknown-outcome text; the wait's error is transport or Temporal text
// (stream resets, addresses) and stays in the log.
func TestRegisterTenant_OutcomeUnknownAnswerCarriesNoWaitError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		waitErr error
		leaks   []string
	}{
		{
			name:    "stream reset",
			waitErr: errors.New("rpc error: code = Internal desc = stream terminated by RST_STREAM with error code: PROTOCOL_ERROR"),
			leaks:   []string{"RST_STREAM", "rpc error", "PROTOCOL_ERROR"},
		},
		{
			name:    "deadline",
			waitErr: fmt.Errorf("await workflow result: %w", context.DeadlineExceeded),
			leaks:   []string{"deadline exceeded", "await workflow result"},
		},
		{
			name:    "Temporal unavailable",
			waitErr: serviceerror.NewUnavailable("dial tcp 10.1.2.3:7233: connect: connection refused"),
			leaks:   []string{"10.1.2.3", "dial tcp", "connection refused"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			te.mockRegistration(tc.waitErr)

			resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), registerTenantInput)

			assert.Nil(t, resp)
			require.ErrorIs(t, err, resolvers.ErrRegistrationOutcomeUnknown)
			require.NotErrorIs(t, err, tc.waitErr, "the wait's error is not part of the answer's chain")
			assert.Equal(t,
				resolvers.ErrRegistrationOutcomeUnknown.Error()+`, tenant "Test Tenant" stays pending until its registration run finishes`,
				err.Error())
			for _, leak := range tc.leaks {
				assert.NotContains(t, err.Error(), leak)
			}

			te.mockRegistration(tc.waitErr)
			msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "Other Tenant"}))
			assert.Equal(t, resolvers.ErrRegistrationOutcomeUnknown.Error()+`, tenant "Other Tenant" stays pending until its registration run finishes`, msg)
			for _, leak := range tc.leaks {
				assert.NotContains(t, msg, leak, "nor over GraphQL")
			}
		})
	}
}

// =============================================================================
// THE PASSWORD IS STORED AS SENT
// =============================================================================

// Zitadel stores the admin password's hash and trims nothing, so spaces at
// either end or inside are part of the password the admin logs in with.
// registerTenant registers such a password and hashes it exactly as sent:
// the stored hash opens with the password, not with its trimmed form.
func TestRegisterTenant_RegistersAPasswordWithSpacesAsSent(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, password string }{
		{"trailing space", "SecurePass123! "},
		{"leading NBSP", "\u00a0SecurePass123!"},
		{"inner and surrounding spaces", "  Secure Pass 123!  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			var hash string
			te.mockRegistration(nil, func(reg *registration) {
				require.NotNil(t, reg.row.Registration)
				hash = reg.row.Registration.PasswordHash
			}, te.finishRegistration)

			input := registerTenantInput
			input.AdminPassword = tc.password
			resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), input)

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.True(t, resp.Success)
			require.NotEmpty(t, hash)
			require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(tc.password)), "the hash is the password as sent")
			require.ErrorIs(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(tc.password))),
				bcrypt.ErrMismatchedHashAndPassword, "the trimmed password must not open the account")
		})
	}
}

// Every GraphQL String is NFC-normalized on the way in, but adminPassword
// is a Password, kept exactly as sent: Zitadel verifies the bytes the
// admin's login sends and never normalizes, and keyboards send NFC. A
// password that arrives decomposed ("o" + U+0308) is refused before any
// row, event or hash, so it never registers as bytes the admin cannot
// type; a composed non-ASCII password registers and is hashed byte for
// byte, so the scalar still transforms nothing. Driven over GraphQL,
// because the scalar sits before the resolver.
func TestRegisterTenant_RefusesADecomposedPasswordAndHashesAComposedOneAsSentOverGraphQL(t *testing.T) {
	t.Parallel()

	const (
		decomposed = "SecurePassö123!"
		composed   = "SecurePassö123!"
	)

	t.Run("decomposed is refused before anything is written", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		rowsBefore, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
		require.NoError(t, err)
		creates := te.countTenantCreates()
		// Only reached when the password is wrongly let through.
		te.mockRegistration(nil, te.finishRegistration)

		msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant,
			withInput(map[string]any{"AdminPassword": decomposed}))

		assert.Contains(t, msg, "adminPassword is not in composed Unicode form (NFC)")
		assert.NotContains(t, msg, "SecurePass", "the password is never echoed")
		assert.Zero(t, creates.Load(), "refused before any insert, so no hash is stored")
		te.assertNoRegistrationSideEffects(rowsBefore)
	})

	t.Run("composed is hashed exactly as sent", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		reg := te.mockRegistration(nil, te.finishRegistration)

		data := execOK[registerTenantData](te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant,
			withInput(map[string]any{"AdminPassword": composed}))

		assert.True(t, data.RegisterTenant.Success)
		require.NotNil(t, reg.row)
		require.NotNil(t, reg.row.Registration)
		hash := []byte(reg.row.Registration.PasswordHash)
		require.NoError(t, bcrypt.CompareHashAndPassword(hash, []byte(composed)), "the hash is the password as sent")
		require.ErrorIs(t, bcrypt.CompareHashAndPassword(hash, []byte(decomposed)), bcrypt.ErrMismatchedHashAndPassword,
			"the decomposed form is a different password and must not open the account")
	})
}

// =============================================================================
// FAILURES OUTSIDE THE RUN CARRY NO DRIVER TEXT
// =============================================================================

// leakPgError is what a database failure looks like to the resolver: the
// driver's error, whose text names the relation and the column and can
// carry the caller's own values. None of it may reach an anonymous
// registrant.
func leakPgError(code string) *pq.Error {
	return &pq.Error{
		Code:    pqerror.Code(code),
		Message: `column "registration" of relation "tenants" does not exist at 10.1.2.3:5432`,
		Detail:  `Failing row contains (SecurePass123!)`,
		Table:   "tenants",
	}
}

// leakDriverFragments are the parts of leakPgError and of a bcrypt error
// that must not appear in an answer.
var leakDriverFragments = []string{"pq:", "SQLSTATE", "relation", "tenants", "registration\"", "10.1.2.3", "5432", "bcrypt", "SecurePass123!", "57P01", "22P05", "XX000"}

// internalAnswer is the fixed answer to a registration that failed on
// pyck's own side outside its run.
func internalAnswer(name string) string {
	return resolvers.ErrRegistrationInternal.Error() + fmt.Sprintf(": tenant %q", name)
}

// withLogBuffer returns ctx carrying a logger that writes into the
// returned buffer, so a test can see what the resolver logged.
func withLogBuffer(ctx context.Context) (context.Context, *bytes.Buffer) {
	var buf bytes.Buffer
	return log.Context(ctx, zerolog.New(&buf)), &buf
}

// assertInternalAnswer checks err is the fixed internal answer for name
// and that the log line names the step and keeps the driver's error.
func assertInternalAnswer(t *testing.T, err error, name string, logged *bytes.Buffer, step, loggedCause string) {
	t.Helper()
	require.ErrorIs(t, err, resolvers.ErrRegistrationInternal)
	assert.Equal(t, internalAnswer(name), err.Error())
	var pgErr *pq.Error
	assert.NotErrorAs(t, err, &pgErr, "the driver's error is not part of the answer's chain")
	for _, leak := range leakDriverFragments {
		assert.NotContains(t, err.Error(), leak)
	}
	assert.Contains(t, logged.String(), `"step":"`+step+`"`, "the failing step is logged")
	assert.Contains(t, logged.String(), loggedCause, "the full error is logged server-side")
	assert.Contains(t, logged.String(), `"level":"error"`)
}

// A failure of pyck's own database or hash before the run (the name
// pre-flight, the pending row's insert, the bcrypt hash) is answered with
// the fixed text, never the driver's or bcrypt's, and logged in full.
// Nothing is written and no run is waited on. Each path is driven directly
// (to see the error value and the log) and over GraphQL (to see the text
// the presenter hands the caller).
func TestRegisterTenant_FailureBeforeTheRunIsAFixedAnswer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		step  string
		cause string
		// inject makes the step fail; the failure holds until done is set,
		// so the test's own reads afterwards succeed.
		inject func(te *testEnv, done *atomic.Bool)
	}{
		{
			name:  "the name pre-flight fails",
			step:  "check tenant name",
			cause: "57P01",
			inject: func(te *testEnv, done *atomic.Bool) {
				te.Ent.Tenant.Intercept(entintercept.TraverseTenant(func(context.Context, *ent.TenantQuery) error {
					if done.Load() {
						return nil
					}
					return leakPgError("57P01")
				}))
			},
		},
		{
			name:  "the pending insert fails",
			step:  "insert pending tenant",
			cause: "22P05",
			inject: func(te *testEnv, _ *atomic.Bool) {
				te.Ent.Tenant.Use(enthook.On(func(ent.Mutator) ent.Mutator {
					return enthook.TenantFunc(func(context.Context, *ent.TenantMutation) (ent.Value, error) {
						return nil, leakPgError("22P05")
					})
				}, ent.OpCreate))
			},
		},
		{
			name:  "the password hash fails",
			step:  "hash admin password",
			cause: "bcrypt",
			inject: func(te *testEnv, _ *atomic.Bool) {
				resolvers.SetHashPassword(te.resolver, func(password []byte, _ int) ([]byte, error) {
					return nil, fmt.Errorf("bcrypt: hashing %q: %w", password, bcrypt.ErrPasswordTooLong)
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			var done atomic.Bool
			tc.inject(te, &done)
			creates := te.countTenantCreates()

			ctx, logged := withLogBuffer(te.ctx(userA))
			resp, err := te.resolver.Mutation().RegisterTenant(ctx, registerTenantInput)

			assert.Nil(t, resp)
			assertInternalAnswer(t, err, registerTenantInput.Name, logged, tc.step, tc.cause)
			if tc.step != "insert pending tenant" {
				assert.Zero(t, creates.Load(), "nothing is inserted after %s fails", tc.step)
			}

			msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "Other Tenant"}))
			assert.Equal(t, internalAnswer("Other Tenant"), msg, "nor over GraphQL")
			for _, leak := range leakDriverFragments {
				assert.NotContains(t, msg, leak, "nor over GraphQL")
			}
			done.Store(true)
			te.assertNoRegistrationSideEffects(0)
		})
	}
}

// The read of the row after the run closed can fail too. The run may have
// succeeded, so the answer is the fixed text that claims neither outcome
// (the row exists here, done) and the driver's text stays in the log.
func TestRegisterTenant_FailedReadAfterTheRunIsAFixedAnswer(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	var armed atomic.Bool
	te.Ent.Tenant.Intercept(entintercept.TraverseTenant(func(context.Context, *ent.TenantQuery) error {
		if armed.Load() {
			return leakPgError("XX000")
		}
		return nil
	}))
	arm := func(*registration) { armed.Store(true) }

	reg := te.mockRegistration(nil, te.finishRegistration, arm)
	ctx, logged := withLogBuffer(te.ctx(userA))
	resp, err := te.resolver.Mutation().RegisterTenant(ctx, registerTenantInput)

	assert.Nil(t, resp)
	assertInternalAnswer(t, err, registerTenantInput.Name, logged, "fetch registered tenant", "XX000")
	armed.Store(false)
	assert.Equal(t, enttenant.StatusDone, mustGetTenant(t, te, reg.TenantID).Status, "the run's outcome stands; only the answer is fixed")

	te.mockRegistration(nil, te.finishRegistration, arm)
	msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "Other Tenant"}))
	armed.Store(false)
	assert.Equal(t, internalAnswer("Other Tenant"), msg, "nor over GraphQL")
	for _, leak := range leakDriverFragments {
		assert.NotContains(t, msg, leak, "nor over GraphQL")
	}
}
