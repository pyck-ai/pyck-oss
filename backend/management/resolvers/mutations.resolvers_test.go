package resolvers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"golang.org/x/crypto/bcrypt"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enthook "github.com/pyck-ai/pyck/backend/management/ent/gen/hook"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
	"github.com/pyck-ai/pyck/backend/management/model"
	"github.com/pyck-ai/pyck/backend/management/resolvers"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var registerTenant = resolver.ParseTemplate(`mutation {
	registerTenant(input: {
		name: "{{.Name}}"
		adminUsername: "{{.AdminUsername}}"
		adminEmail: "{{.AdminEmail}}"
		adminFirstName: "{{.AdminFirstName}}"
		adminLastName: "{{.AdminLastName}}"
		adminPassword: "{{.AdminPassword}}"
	}) {
		success
	}
}`)

var registerTenantFull = resolver.ParseTemplate(`mutation {
	registerTenant(input: {
		name: "{{.Name}}"
		adminUsername: "{{.AdminUsername}}"
		adminEmail: "{{.AdminEmail}}"
		adminFirstName: "{{.AdminFirstName}}"
		adminLastName: "{{.AdminLastName}}"
		adminPassword: "{{.AdminPassword}}"
		expiresAt: "{{.ExpiresAt}}"
		data: {flavour: "{{.Flavour}}"}
	}) {
		success
		tenant { id idpOrgRef name status expiresAt data }
	}
}`)

var registerTenantWithData = resolver.ParseTemplate(`mutation {
	registerTenant(input: {
		name: "{{.Name}}"
		adminUsername: "{{.AdminUsername}}"
		adminEmail: "{{.AdminEmail}}"
		adminFirstName: "{{.AdminFirstName}}"
		adminLastName: "{{.AdminLastName}}"
		adminPassword: "{{.AdminPassword}}"
		data: {flavour: "{{.Flavour}}"}
	}) {
		success
	}
}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type (
	registerTenantData struct {
		RegisterTenant struct {
			Success bool
		}
	}
	registerTenantFullData struct {
		RegisterTenant struct {
			Success bool
			Tenant  struct {
				ID        uuid.UUID
				IdpOrgRef string
				Name      string
				Status    string
				ExpiresAt time.Time
				Data      map[string]any
			}
		}
	}
	deleteTenantData struct {
		DeleteTenant struct{ Success bool }
	}

	restoreTenantData struct {
		RestoreTenant struct{ Success bool }
	}
	setTenantExpiryData struct {
		SetTenantExpiry struct{ Success bool }
	}
	setTenantUITemplateData struct {
		SetTenantUITemplate struct{ Success bool }
	}
)

// =============================================================================
// REGISTER TENANT TESTS
// =============================================================================

// registration is what the test learns about a registration from the
// Temporal mock: the resolver chooses the tenant identity and waits on
// register-tenant-<tenantID>, so the ID falls out of the workflow ID the
// wait targets. row is the pending row as it was when the wait started.
type registration struct {
	TenantID uuid.UUID
	row      *ent.Tenant
}

// mockRegistration wires the Temporal mock the way the resolver drives it:
// no start, no signal, just the wait for the result under the
// deterministic ID and an empty run ID. beforeResult runs when the
// resolver starts waiting, i.e. after the row is committed: the place to
// simulate what the real workflow's activities do to the DB.
func (te *testEnv) mockRegistration(wfErr error, beforeResult ...func(reg *registration)) *registration {
	te.t.Helper()
	reg := &registration{}
	mockRun := mocks.NewMockWorkflowRun("workflow-id", "run-id", nil, wfErr)
	te.TemporalClient.
		On("GetWorkflow", mock.Anything, mock.AnythingOfType("string"), "").
		Run(func(args mock.Arguments) {
			workflowID, ok := args.Get(1).(string)
			require.True(te.t, ok, "workflow id type")
			id, err := uuid.Parse(strings.TrimPrefix(workflowID, "register-tenant-"))
			require.NoError(te.t, err, "the wait must target register-tenant-<tenantID>, got %q", workflowID)
			reg.TenantID = id
			row, err := te.Ent.Tenant.Query().Where(enttenant.IDEQ(id)).Only(te.ctxWithDeleted(systemUser))
			require.NoError(te.t, err, "the row must be committed before the resolver waits")
			reg.row = row
			for _, fn := range beforeResult {
				fn(reg)
			}
		}).
		Return(mockRun).Once()
	return reg
}

// failingWorkflow is the body workflowFailure runs to obtain a genuine
// workflow failure; the message is what the resolver's caller must see.
func failingWorkflow(_ workflow.Context, msg string) error {
	return errors.New(msg)
}

// workflowFailure returns what a real WorkflowRun.Get hands back when the
// execution failed: a *temporal.WorkflowExecutionError wrapping the
// workflow's error. The SDK does not export a constructor, so the error is
// produced by running a one-line workflow in the test environment. Only
// this error type tells the resolver the run is closed and the row's
// status is final.
func workflowFailure(t *testing.T, msg string) error {
	t.Helper()
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterWorkflow(failingWorkflow)
	env.ExecuteWorkflow(failingWorkflow, msg)
	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.ErrorContains(t, err, msg)
	return err
}

// mutateTenant applies fn to the tenant row in its own tx with a txid, the
// way an activity would (the outbox hook refuses mutations without one).
func (te *testEnv) mutateTenant(ctx context.Context, id uuid.UUID, fn func(*ent.TenantUpdateOne) *ent.TenantUpdateOne) {
	te.t.Helper()
	require.NoError(te.t, te.withTx(ctx, func(tx *ent.Tx) error {
		return fn(tx.Tenant.UpdateOneID(id)).Exec(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
	}))
}

// finishRegistration is what SetTenantStatusActivity does at the end of a
// successful run: status done, registration cleared.
func (te *testEnv) finishRegistration(reg *registration) {
	te.mutateTenant(te.ctxForTenant(systemUser, reg.TenantID), reg.TenantID, func(u *ent.TenantUpdateOne) *ent.TenantUpdateOne {
		return u.SetStatus(enttenant.StatusDone).ClearRegistration()
	})
}

// tenantCreateEventDataAfter returns data_after of the tenant create
// outbox row: the row shape the tenant-lifecycle trigger reads status and
// idp_org_ref from before it starts the workflow.
func (te *testEnv) tenantCreateEventDataAfter(ctx context.Context) map[string]any {
	te.t.Helper()
	entries, err := te.Ent.EntityEventsOutbox.Query().All(ctx)
	require.NoError(te.t, err)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Topic, ".create") {
			continue
		}
		after, ok := entry.Payload["data_after"].(map[string]any)
		require.True(te.t, ok, "data_after must be the row, got %T", entry.Payload["data_after"])
		return after
	}
	require.FailNow(te.t, "no tenant create event in the outbox")
	return nil
}

var registerInput = map[string]any{
	"Name":           "Test Tenant",
	"AdminUsername":  "testadmin",
	"AdminEmail":     "testadmin@example.com",
	"AdminFirstName": "Test",
	"AdminLastName":  "Admin",
	"AdminPassword":  "SecurePass123!",
}

// registerTenantInput is registerInput for tests that call the resolver
// directly instead of going through GraphQL: those need the resolver's own
// error value, which the GraphQL layer flattens to a message.
var registerTenantInput = model.RegisterTenantInput{
	Name:           "Test Tenant",
	AdminUsername:  "testadmin",
	AdminEmail:     "testadmin@example.com",
	AdminFirstName: "Test",
	AdminLastName:  "Admin",
	AdminPassword:  "SecurePass123!",
}

// execErrMessage executes a GraphQL query that must fail and returns the
// first error message, for tests that assert what the message must NOT
// say as well as what it must.
func execErrMessage(te *testEnv, ctx context.Context, tpl resolver.Template, args any) string {
	te.t.Helper()
	result := resolver.Exec[json.RawMessage, *ent.Client](te.TestEnvironment, ctx, tpl, args)
	require.NotEmpty(te.t, result.Errors, "expected a GraphQL error")
	return result.Errors[0].Message
}

func withInput(overrides map[string]any) map[string]any {
	out := make(map[string]any, len(registerInput)+len(overrides))
	for k, v := range registerInput {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func TestRegisterTenant(t *testing.T) {
	t.Parallel()

	t.Run("commits the pending row with the hashed admin account, then waits on the tenant's workflow ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		reg := te.mockRegistration(nil, te.finishRegistration)

		data := execOK[registerTenantData](te, resolver.WithoutForcedFeatures(ctx), registerTenant, registerInput)
		assert.True(t, data.RegisterTenant.Success)

		// Identity: chosen here, invariant tenant.id == ComputeUUID(audience, org).
		require.NotEqual(t, uuid.Nil, reg.TenantID)
		require.NotNil(t, reg.row)
		assert.Equal(t, authn.ComputeUUID(core.Config.ZitadelAudience, reg.row.IdpOrgRef), reg.TenantID)
		assert.Equal(t, "Test Tenant", reg.row.Name)
		assert.Equal(t, enttenant.StatusPending, reg.row.Status, "the row is pending while the resolver waits")
		assert.Nil(t, reg.row.ExpiresAt)

		// The admin account travels on the row, password as a bcrypt hash.
		require.NotNil(t, reg.row.Registration)
		assert.Equal(t, "testadmin", reg.row.Registration.AdminUsername)
		assert.Equal(t, "testadmin@example.com", reg.row.Registration.AdminEmail)
		assert.Equal(t, "Test", reg.row.Registration.AdminFirstName)
		assert.Equal(t, "Admin", reg.row.Registration.AdminLastName)
		require.NoError(t, bcrypt.CompareHashAndPassword([]byte(reg.row.Registration.PasswordHash), []byte("SecurePass123!")))
		cost, err := bcrypt.Cost([]byte(reg.row.Registration.PasswordHash))
		require.NoError(t, err)
		assert.Equal(t, 12, cost, "the hash sits on a DB row until the workflow imports it; a cheaper hash weakens that window")
		assert.True(t, strings.HasPrefix(reg.row.Registration.PasswordHash, "$2a$"),
			"Zitadel's passwap verifies bcrypt only in modular crypt format")

		// The create event carries the tenant's own ID as mutation tenant:
		// it is what the lifecycle trigger turns into the workflow start.
		// The update is the simulated status flip.
		te.assertEvents(ctx, Create("tenant", reg.TenantID), Update("tenant", reg.TenantID))

		// The trigger keys on these two data_after fields (snake_case
		// column names, the enum serialised as its string) and the
		// Sensitive registration field must be absent from the payload.
		after := te.tenantCreateEventDataAfter(ctx)
		assert.Equal(t, enttenant.StatusPending.String(), after["status"])
		assert.Equal(t, reg.row.IdpOrgRef, after["idp_org_ref"])
		assert.NotContains(t, after, "registration")

		assert.Equal(t, []uuid.UUID{reg.TenantID}, te.streams.ensuredTenants(),
			"the create event is published to the tenant's own stream")

		// The resolver starts, signals and cancels nothing.
		te.TemporalClient.AssertExpectations(t)
		te.TemporalClient.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		te.TemporalClient.AssertNotCalled(t, "SignalWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		te.TemporalClient.AssertNotCalled(t, "CancelWorkflow", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("persists expiresAt and data on the pending row and returns the finished tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		expiry := time.Date(2099, 6, 1, 12, 0, 0, 0, time.UTC)
		reg := te.mockRegistration(nil, te.finishRegistration)

		data := execOK[registerTenantFullData](te, resolver.WithoutForcedFeatures(ctx), registerTenantFull, withInput(map[string]any{
			"ExpiresAt": expiry.Format(time.RFC3339),
			"Flavour":   "pyck-go",
		}))

		assert.True(t, data.RegisterTenant.Success)
		got := data.RegisterTenant.Tenant
		assert.Equal(t, reg.TenantID, got.ID, "the returned tenant is the one the resolver registered")
		assert.Equal(t, reg.row.IdpOrgRef, got.IdpOrgRef)
		assert.Equal(t, "done", got.Status)
		assert.True(t, got.ExpiresAt.Equal(expiry), "expiresAt must land on the row at insert, got %v", got.ExpiresAt)
		assert.Equal(t, "pyck-go", got.Data["flavour"])
		assert.Equal(t, "pyck-go", reg.row.Data["flavour"], "the workflow reads the flavour from the row")
	})

	t.Run("stores a valid UI template from a system caller on the pending row", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		const tmpl = "https://ui.example.test/{{.Version}}/mf-manifest.json"
		reg := te.mockRegistration(nil, te.finishRegistration)
		input := registerTenantInput
		input.Data = map[string]any{core.RemoteWebUITemplateKey: tmpl}

		resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(systemUser), input)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		require.NotNil(t, reg.row)
		assert.Equal(t, tmpl, reg.row.Data[core.RemoteWebUITemplateKey])
	})

	t.Run("waits for the execution to appear while the create event is in flight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// The consumer has not started the run yet on the first look.
		notYet := mocks.NewMockWorkflowRun("workflow-id", "", nil, serviceerror.NewNotFound("workflow not found"))
		te.TemporalClient.On("GetWorkflow", mock.Anything, mock.AnythingOfType("string"), "").Return(notYet).Once()
		te.mockRegistration(nil, te.finishRegistration)

		data := execOK[registerTenantData](te, resolver.WithoutForcedFeatures(ctx), registerTenant, registerInput)
		assert.True(t, data.RegisterTenant.Success)
		te.TemporalClient.AssertExpectations(t)
	})

	t.Run("reports the failure and keeps the row when the run marked it error", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		reg := te.mockRegistration(workflowFailure(t, "provisioning boom"), func(reg *registration) {
			// What SetTenantStatusActivity(error) does before the run fails.
			te.mutateTenant(te.ctxForTenant(systemUser, reg.TenantID), reg.TenantID, func(u *ent.TenantUpdateOne) *ent.TenantUpdateOne {
				return u.SetStatus(enttenant.StatusError)
			})
		})

		resp, err := te.resolver.Mutation().RegisterTenant(ctx, registerTenantInput)
		require.Nil(t, resp)
		// A closed, failed run is a known outcome: the client must learn
		// the row is terminal (marked error), not that the outcome is
		// unknown and the row still pending. The run's own error text
		// stays in the log.
		require.ErrorContains(t, err, "tenant row marked error")
		require.NotContains(t, err.Error(), "provisioning boom")
		require.NotContains(t, err.Error(), "outcome unknown")

		row, err := te.Ent.Tenant.Get(te.ctxWithDeleted(systemUser), reg.TenantID)
		require.NoError(t, err, "a failed registration keeps its row for the operator")
		assert.Equal(t, enttenant.StatusError, row.Status)
		assert.True(t, row.DeletedAt.IsZero(), "nothing is deleted on failure")
		assert.NotNil(t, row.Registration, "the registration data stays for a re-run")
		te.TemporalClient.AssertNotCalled(t, "CancelWorkflow", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("reports the row's status when the run failed without a terminal flip", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// The row is re-read until the wait ends; keep that short.
		resolvers.SetRegistrationWait(te.resolver, 200*time.Millisecond, 20*time.Millisecond)

		// The run failed on a step and its status flip never landed: the
		// row is left pending.
		reg := te.mockRegistration(stepFailure(t, func(context.Context) error {
			return temporal.NewNonRetryableApplicationError("organization name taken", "org-rejected", nil)
		}))

		execErr(te, resolver.WithoutForcedFeatures(ctx), registerTenant, registerInput, `before the tenant row settled: tenant "Test Tenant" in status "pending"`)

		row, err := te.Ent.Tenant.Get(te.ctxWithDeleted(systemUser), reg.TenantID)
		require.NoError(t, err)
		assert.Equal(t, enttenant.StatusPending, row.Status, "the resolver never flips the row itself")
		assert.True(t, row.DeletedAt.IsZero())
		assert.NotNil(t, row.Registration)
	})

	// Zitadel's 200-character limit is inclusive: a value of exactly 200
	// runes is imported, so the refusal must start at 201 and not one short
	// of it, or a valid name is turned away with a message naming its own
	// length as the limit.
	t.Run("accepts a name and admin names at Zitadel's 200-character limit", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		reg := te.mockRegistration(nil, te.finishRegistration)

		data := execOK[registerTenantData](te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{
			"Name":           strings.Repeat("n", 200),
			"AdminFirstName": strings.Repeat("é", 200),
		}))

		assert.True(t, data.RegisterTenant.Success)
		require.NotNil(t, reg.row)
		assert.Equal(t, 200, utf8.RuneCountInString(reg.row.Name))
		require.NotNil(t, reg.row.Registration)
		assert.Equal(t, 200, utf8.RuneCountInString(reg.row.Registration.AdminFirstName))
	})

	// The data key cap is inclusive as well: a map of exactly 32 keys is
	// what SetOrgMetadataActivity can still write, so it lands on the row.
	t.Run("accepts data with exactly 32 keys", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		atCap := make(map[string]any, 32)
		for i := range 32 {
			atCap[fmt.Sprintf("k%d", i)] = "v"
		}
		reg := te.mockRegistration(nil, te.finishRegistration)
		input := registerTenantInput
		input.Data = atCap

		resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), input)

		require.NoError(t, err)
		assert.True(t, resp.Success)
		require.NotNil(t, reg.row)
		assert.Len(t, reg.row.Data, 32, "every key must land on the row for the run to write")
	})

	// A registration leaves nothing behind that the next one trips over,
	// whatever the earlier one ended as.
	t.Run("registers again after an earlier registration returned", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name  string
			first func(t *testing.T, te *testEnv)
		}{
			{"after a registration that succeeded", func(t *testing.T, te *testEnv) {
				t.Helper()
				te.mockRegistration(nil, te.finishRegistration)
				data := execOK[registerTenantData](te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "First"}))
				assert.True(t, data.RegisterTenant.Success)
			}},
			{"after a registration that ended outcome unknown", func(t *testing.T, te *testEnv) {
				t.Helper()
				te.mockRegistration(errors.New("connection refused"))
				msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "First"}))
				require.Contains(t, msg, "outcome unknown")
			}},
			{"after a registration the run marked error", func(t *testing.T, te *testEnv) {
				t.Helper()
				te.mockRegistration(workflowFailure(t, "provisioning boom"), func(reg *registration) {
					te.mutateTenant(te.ctxForTenant(systemUser, reg.TenantID), reg.TenantID, func(u *ent.TenantUpdateOne) *ent.TenantUpdateOne {
						return u.SetStatus(enttenant.StatusError)
					})
				})
				msg := execErrMessage(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "First"}))
				require.Contains(t, msg, "tenant row marked error")
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				tc.first(t, te)

				reg := te.mockRegistration(nil, te.finishRegistration)
				result := resolver.Exec[json.RawMessage, *ent.Client](te.TestEnvironment, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "Second"}))
				require.Empty(t, result.Errors, "the second registration must go through")
				require.NotNil(t, reg.row)
				assert.Equal(t, "Second", reg.row.Name)
			})
		}
	})

	// The wait is sized under the gateway's request timeout, not the
	// consumer's redelivery schedule: a run that has not appeared when it
	// ends may still be started, so the verdict is "unknown", never "no
	// run", and the row keeps everything that run needs.
	t.Run("reports an unknown outcome and leaves the row pending when no run appears within the wait", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)
		resolvers.SetRegistrationWait(te.resolver, 30*time.Millisecond, 5*time.Millisecond)

		// No run under the ID on any look.
		notYet := mocks.NewMockWorkflowRun("workflow-id", "", nil, serviceerror.NewNotFound("workflow not found"))
		te.TemporalClient.On("GetWorkflow", mock.Anything, mock.AnythingOfType("string"), "").Return(notYet)

		msg := execErrMessage(te, resolver.WithoutForcedFeatures(ctx), registerTenant, registerInput)
		assert.Contains(t, msg, "outcome unknown")
		assert.NotContains(t, msg, "marked error", "no run closed, so nothing is known about the row")

		calls := 0
		for _, c := range te.TemporalClient.Calls {
			if c.Method == "GetWorkflow" {
				calls++
			}
		}
		assert.GreaterOrEqual(t, calls, 2, "the resolver must keep looking for the run until the wait is spent")

		row, err := te.Ent.Tenant.Query().Where(enttenant.Name("Test Tenant")).Only(te.ctxWithDeleted(systemUser))
		require.NoError(t, err, "the row stays for the run the consumer starts")
		assert.Equal(t, enttenant.StatusPending, row.Status)
		assert.True(t, row.DeletedAt.IsZero())
		assert.NotNil(t, row.Registration, "the registration data must survive for the run")
		te.TemporalClient.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		te.assertEvents(ctx, Create("tenant", row.ID))
	})

	t.Run("reports an unknown outcome and leaves the row pending when the result cannot be observed", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// The result read fails on transport, not because the run failed.
		reg := te.mockRegistration(errors.New("connection refused"))

		msg := execErrMessage(te, resolver.WithoutForcedFeatures(ctx), registerTenant, registerInput)
		assert.Contains(t, msg, "outcome unknown")
		assert.NotContains(t, msg, "marked error", "a transport error is not a verdict on the row")

		row, err := te.Ent.Tenant.Get(te.ctxWithDeleted(systemUser), reg.TenantID)
		require.NoError(t, err, "an unknown outcome must not remove a tenant the run may still provision")
		assert.Equal(t, enttenant.StatusPending, row.Status)
		assert.True(t, row.DeletedAt.IsZero())
	})

	// The request being cancelled while the run has not appeared yet is
	// not "no run was started": the create event is still in flight and
	// the consumer will start the run. The client must be told the outcome
	// is unknown, and the row must keep everything the run needs. The
	// resolver is called directly: over HTTP the cancelled context drops
	// the request before any response exists.
	t.Run("reports an unknown outcome when the request is cancelled while the run has not started", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx, cancel := context.WithCancel(te.ctx(userA))
		defer cancel()

		notYet := mocks.NewMockWorkflowRun("workflow-id", "", nil, serviceerror.NewNotFound("workflow not found"))
		te.TemporalClient.On("GetWorkflow", mock.Anything, mock.AnythingOfType("string"), "").
			Run(func(mock.Arguments) { cancel() }).
			Return(notYet)

		resp, err := te.resolver.Mutation().RegisterTenant(ctx, registerTenantInput)
		require.ErrorContains(t, err, "outcome unknown")
		require.NotContains(t, err.Error(), "marked error")
		require.NotContains(t, err.Error(), context.Canceled.Error(), "the wait's error stays in the log")
		assert.Nil(t, resp)

		row, err := te.Ent.Tenant.Query().Where(enttenant.Name("Test Tenant")).Only(te.ctxWithDeleted(systemUser))
		require.NoError(t, err, "the row must survive for the run the consumer starts")
		assert.Equal(t, enttenant.StatusPending, row.Status)
		assert.True(t, row.DeletedAt.IsZero())
		assert.NotNil(t, row.Registration, "the run still needs the admin account")
		te.TemporalClient.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}

// Refusals happen before any side effect: no row, no event, no wait.
func TestRegisterTenant_Refusals(t *testing.T) {
	t.Parallel()

	assertNothingHappened := func(t *testing.T, te *testEnv) {
		t.Helper()
		te.TemporalClient.AssertNotCalled(t, "GetWorkflow", mock.Anything, mock.Anything, mock.Anything)
		te.TemporalClient.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		assert.ElementsMatch(t, te.streams.ensuredTenants(), te.streams.deletedTenants(),
			"a stream put for a row that was not inserted is deleted again")
	}

	t.Run("inserts no row when the tenant's event stream cannot be created", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.streams.ensureErr = errors.New("jetstream unavailable")

		resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), registerTenantInput)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "tenant registration could not be processed")
		require.NotContains(t, err.Error(), "jetstream unavailable")

		n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
		require.NoError(t, err)
		assert.Zero(t, n, "the row's create event would have no stream to go to")
		assertNothingHappened(t, te)
	})

	// Every data key becomes one Zitadel metadata write inside the run;
	// a map the run cannot write must be refused here, where nothing has
	// been created yet, instead of ending as an error row with an org.
	t.Run("rejects data the run could not write as org metadata", func(t *testing.T) {
		t.Parallel()

		tooMany := make(map[string]any, 33)
		for i := range 33 {
			tooMany[fmt.Sprintf("k%d", i)] = "v"
		}
		cases := []struct {
			name string
			data map[string]any
			want string
		}{
			{"too many keys", tooMany, "33 keys"},
			{"over-long key", map[string]any{strings.Repeat("k", 201): "v"}, "key"},
			{"empty key", map[string]any{"": "v"}, "key"},
			{"nested value", map[string]any{"a": map[string]any{"b": "c"}}, "not a scalar"},
			{"list value", map[string]any{"a": []any{"b"}}, "not a scalar"},
			{"null value", map[string]any{"a": nil}, "not a scalar"},
			{"empty value", map[string]any{"a": ""}, "is empty"},
			{"too large", map[string]any{"a": strings.Repeat("x", 16*1024)}, "bytes"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), model.RegisterTenantInput{
					Name:           "Test Tenant",
					AdminUsername:  "testadmin",
					AdminEmail:     "testadmin@example.com",
					AdminFirstName: "Test",
					AdminLastName:  "Admin",
					AdminPassword:  "SecurePass123!",
					Data:           tc.data,
				})
				require.ErrorContains(t, err, "tenant data must be")
				require.ErrorContains(t, err, tc.want)
				assert.Nil(t, resp)

				assertNothingHappened(t, te)
				n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
				require.NoError(t, err)
				assert.Zero(t, n)
			})
		}
	})

	// Zitadel checks the admin account only at user import, after the org
	// exists; a value it would reject has to be refused here, where
	// nothing has been created yet.
	t.Run("rejects a name or admin account Zitadel would refuse at import", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name     string
			override map[string]any
			want     string
		}{
			{"empty username", map[string]any{"AdminUsername": ""}, "adminUsername is blank"},
			{"empty last name", map[string]any{"AdminLastName": ""}, "adminLastName is blank"},
			{"over-long first name", map[string]any{"AdminFirstName": strings.Repeat("é", 201)}, "adminFirstName has 201 characters"},
			{"over-long name", map[string]any{"Name": strings.Repeat("n", 201)}, "name has 201 characters"},
			{"not an address", map[string]any{"AdminEmail": "not-an-address"}, `adminEmail "not-an-address"`},
			{"address with display name", map[string]any{"AdminEmail": "Admin <admin@example.com>"}, `adminEmail "Admin <admin@example.com>"`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				execErr(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(tc.override), "must be non-blank")
				execErr(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(tc.override), tc.want)

				assertNothingHappened(t, te)
				n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
				require.NoError(t, err)
				assert.Zero(t, n)
				te.assertNoEvents(te.ctx(systemUser))
			})
		}
	})

	t.Run("rejects a UI template from a non-system caller", func(t *testing.T) {
		t.Parallel()
		for _, key := range core.RemoteUITemplateKeys {
			t.Run(key, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				input := registerTenantInput
				input.Data = map[string]any{key: "https://attacker.example.test/{{.Version}}/mf-manifest.json"}

				resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), input)
				require.Nil(t, resp)
				require.ErrorIs(t, err, authn.ErrSystemCallerRequired)
				require.ErrorContains(t, err, "registerTenant: system caller required to set "+key)
				assertNothingHappened(t, te)
			})
		}
	})

	t.Run("rejects an invalid UI template from a system caller", func(t *testing.T) {
		t.Parallel()
		for name, tc := range map[string]struct {
			value   any
			wantErr string
		}{
			"invisible host": {"https://ok\u202e.example.test/{{.Version}}", "invisible or control character"},
			"not a string":   {true, "must be a string"},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				input := registerTenantInput
				input.Data = map[string]any{core.RemoteWebUITemplateKey: tc.value}

				resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(systemUser), input)
				require.Nil(t, resp)
				require.ErrorIs(t, err, core.ErrInvalidUITemplate)
				require.ErrorContains(t, err, tc.wantErr)
				assertNothingHappened(t, te)
			})
		}
	})

	t.Run("rejects a weak admin password", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		execErr(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"AdminPassword": "password"}), "admin password does not meet")

		assertNothingHappened(t, te)
		n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	// bcrypt only hashes the first 72 bytes and golang.org/x/crypto/bcrypt
	// refuses longer input; a password past the cap must be refused by the
	// rule, not surface as a hashing error after the pre-flight.
	t.Run("rejects an admin password longer than the bcrypt cap", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tooLong := "Aa1!" + strings.Repeat("x", 69) // 73 bytes, otherwise valid
		execErr(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"AdminPassword": tooLong}), "admin password does not meet")

		assertNothingHappened(t, te)
		n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
		require.NoError(t, err)
		assert.Zero(t, n)
		te.assertNoEvents(te.ctx(systemUser))
	})

	t.Run("rejects a name held by an active tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		existingID := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, existingID), existingID).Name("Acme").Create()
		te.clearEvents(te.ctx(systemUser))

		execErr(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "Acme"}), "already exists")

		assertNothingHappened(t, te)
		n, err := te.Ent.Tenant.Query().Count(te.ctxWithDeleted(systemUser))
		require.NoError(t, err)
		assert.Equal(t, 1, n, "only the pre-existing tenant")
		te.assertNoEvents(te.ctx(systemUser))
	})

	// A registration in flight, or one that failed and awaits an operator,
	// reserves its name: a second registerTenant for the same name must
	// not race it into two Zitadel orgs.
	t.Run("rejects a name reserved by a pending or failed registration", func(t *testing.T) {
		t.Parallel()

		for _, status := range []enttenant.Status{enttenant.StatusPending, enttenant.StatusError} {
			t.Run(status.String(), func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				reservedID := uuidgql.GenerateV7UUID()
				te.newTenant(te.ctxForTenant(systemUser, reservedID), reservedID).Name("Acme").Create()
				te.mutateTenant(te.ctxForTenant(systemUser, reservedID), reservedID, func(u *ent.TenantUpdateOne) *ent.TenantUpdateOne {
					return u.SetStatus(status)
				})
				te.clearEvents(te.ctx(systemUser))

				execErr(te, resolver.WithoutForcedFeatures(te.ctx(userA)), registerTenant, withInput(map[string]any{"Name": "Acme"}), "already exists")

				assertNothingHappened(t, te)
				te.assertNoEvents(te.ctx(systemUser))
			})
		}
	})

	// Two registrations of one name that both pass the pre-flight are
	// decided by the unique index on name_key: the loser's insert
	// fails with a constraint error, which the client must see as the
	// same friendly refusal, not as the driver's message. The race winner
	// is simulated by a hook that commits the competitor row between the
	// pre-flight and the pending insert.
	t.Run("returns the friendly error when the unique index, not the pre-flight, catches the name", func(t *testing.T) {
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
					SetName("Test Tenant").
					SetIdpOrgRef(competitorID.String()).
					Exec(ctx); err != nil {
					return nil, fmt.Errorf("insert competitor: %w", err)
				}
				return next.Mutate(ctx, m)
			})
		}, ent.OpCreate))

		resp, err := te.resolver.Mutation().RegisterTenant(te.ctx(userA), registerTenantInput)
		require.True(t, raced, "the competitor must be inserted between pre-flight and insert")
		require.EqualError(t, err, "tenant with the provided name already exists")
		require.Nil(t, resp)

		assert.Len(t, te.streams.deletedTenants(), 1, "the loser's stream is deleted again")
		assertNothingHappened(t, te)
	})
}

// =============================================================================
// TENANT LIFECYCLE TEMPLATES (disable / restore / setExpiry)
// =============================================================================

var (
	deleteTenantMut = resolver.ParseTemplate(`mutation {
		deleteTenant(input: { tenantId: "{{.TenantID}}" }) {
			success
		}
	}`)

	restoreTenantMut = resolver.ParseTemplate(`mutation {
		restoreTenant(input: { tenantId: "{{.TenantID}}" }) {
			success
		}
	}`)

	restoreTenantWithExpiryMut = resolver.ParseTemplate(`mutation {
		restoreTenant(input: { tenantId: "{{.TenantID}}", expiresAt: "{{.ExpiresAt}}" }) {
			success
		}
	}`)

	setTenantExpiryMut = resolver.ParseTemplate(`mutation {
		setTenantExpiry(input: { tenantId: "{{.TenantID}}", expiresAt: "{{.ExpiresAt}}" }) {
			success
		}
	}`)

	clearTenantExpiryMut = resolver.ParseTemplate(`mutation {
		setTenantExpiry(input: { tenantId: "{{.TenantID}}", expiresAt: null }) {
			success
		}
	}`)

	setTenantUITemplateMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { webTemplate: "{{.Web}}", mobileTemplate: "{{.Mobile}}" }) {
			success
		}
	}`)

	setTenantUITemplateWebOnlyMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { webTemplate: "{{.Web}}" }) {
			success
		}
	}`)

	setTenantUITemplateEmptyMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: {}) {
			success
		}
	}`)

	setTenantUITemplateClearWebMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { clearWebTemplate: true }) {
			success
		}
	}`)

	setTenantUITemplateSetAndClearMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { webTemplate: "{{.Web}}", clearWebTemplate: true }) {
			success
		}
	}`)
)

// tenantArgs builds the template arguments of a tenant-lifecycle mutation:
// the explicit target the three of them take, plus whatever the case adds.
func tenantArgs(tenantID uuid.UUID, extra map[string]any) map[string]any {
	args := map[string]any{"TenantID": tenantID}
	maps.Copy(args, extra)
	return args
}

// =============================================================================
// DISABLE TENANT
// =============================================================================

func TestDeleteTenant(t *testing.T) {
	t.Parallel()

	t.Run("soft-deletes an active tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Name("ToDelete").Create()
		te.clearEvents(ctx)

		data := execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(tenantID, nil))
		assert.True(t, data.DeleteTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.False(t, got.DeletedAt.IsZero(), "deleted_at should be set")

		te.assertEvents(ctx, Delete("tenant", tenantID))
	})

	// The system-caller guard belongs to restoreTenant and setTenantExpiry.
	// Disabling a tenant is something that tenant's own admin may do, and
	// since registration grants that admin the admin project role, this is
	// the case a real tenant hits; only bringing it back is reserved for an
	// operator. Copying the guard onto this mutation would lock tenants out
	// of their own off switch, and nothing else here would notice: every
	// other deleteTenant case runs as the system user.
	t.Run("admits a non-system caller holding ROLE_ADMIN on the target", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)
		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("SelfService").Create()
		te.clearEvents(ctx)

		data := execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(resolver.TenantA, nil))
		assert.True(t, data.DeleteTenant.Success)

		got := mustGetTenant(t, te, resolver.TenantA)
		assert.False(t, got.DeletedAt.IsZero(), "the tenant's own admin must be able to disable it")
		te.assertEvents(ctx, Delete("tenant", resolver.TenantA))
	})

	// An error row keeps its admin account for the operator's re-run;
	// retiring it ends that option and must not leave the password hash on
	// a tombstone nothing ever hard-deletes.
	t.Run("clears the registration data of a retired error row", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Name("Failed").Registration(&types.TenantRegistration{
			AdminUsername: "admin", AdminEmail: "admin@example.com", AdminFirstName: "Ada", AdminLastName: "Admin", PasswordHash: "$2a$12$hash",
		}).Create()
		te.mutateTenant(ctx, tenantID, func(u *ent.TenantUpdateOne) *ent.TenantUpdateOne {
			return u.SetStatus(enttenant.StatusError)
		})
		te.clearEvents(ctx)

		data := execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(tenantID, nil))
		assert.True(t, data.DeleteTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.False(t, got.DeletedAt.IsZero(), "deleted_at should be set")
		assert.Nil(t, got.Registration, "a retired row must not hold the admin password hash")
		assert.Equal(t, enttenant.StatusError, got.Status, "the status is left as it was")
	})

	t.Run("is idempotent on an already-deleted tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Name("AlreadyOff").Deleted().Create()
		te.clearEvents(ctx)

		data := execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(tenantID, nil))
		assert.True(t, data.DeleteTenant.Success)

		te.assertNoEvents(ctx)
	})

	t.Run("errors on unknown tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)

		execErr(te, ctx, deleteTenantMut, tenantArgs(tenantID, nil), "query tenant")
	})

	t.Run("rejects unauthenticated callers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, tenantID), tenantID).Create()

		// Anonymous request: no X-Pyck-Tenant-Id header (user.TenantID is
		// uuid.Nil → SendQuery skips the header), so tenant.HTTPMiddleware
		// doesn't reject the request and the resolver's IsAuthenticated()
		// check is what surfaces the error.
		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, deleteTenantMut, tenantArgs(tenantID, nil), "authentication required")
	})

	// Regression test for M1 (PR #1172 round-2 review): authenticated-but-
	// non-admin callers must be rejected before the resolver swaps to the
	// system user. Without the role gate, any writer/reader of the target
	// tenant — or, once paired with the schema change, any authenticated
	// user of ANY tenant — could trigger the disable workflow.
	t.Run("rejects non-admin caller (M1: role gate)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// userAWriter is a writer on TenantA, not an admin. te.ctx sets
		// MutationTenantID = TenantA, so this exercises "caller is in the
		// target tenant but lacks ROLE_ADMIN" — the closest analogue of
		// the cross-tenant attack at the unit-test layer.
		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("VictimTenant").Create()

		writerCtx := te.ctx(userAWriter)
		execErr(te, writerCtx, deleteTenantMut, tenantArgs(resolver.TenantA, nil), "admin role required")
	})
}

// =============================================================================
// RESTORE TENANT
// =============================================================================

func TestRestoreTenant(t *testing.T) {
	t.Parallel()

	t.Run("clears deleted_at and preserves prior expiry when none supplied", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		priorExpiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(priorExpiry).Deleted().Create()
		te.clearEvents(ctx)

		data := execOK[restoreTenantData](te, ctx, restoreTenantMut, tenantArgs(tenantID, nil))
		assert.True(t, data.RestoreTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.True(t, got.DeletedAt.IsZero(), "deleted_at should be cleared")
		require.NotNil(t, got.ExpiresAt, "prior expiry should be preserved")
		assert.True(t, got.ExpiresAt.Equal(priorExpiry), "prior expiry should be unchanged")

		te.assertEvents(ctx, Update("tenant", tenantID))
		assert.Equal(t, []uuid.UUID{tenantID}, te.streams.ensuredTenants(),
			"the restore event needs the tenant's stream")
	})

	t.Run("stays disabled when the event stream cannot be created", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.streams.ensureErr = errors.New("jetstream unavailable")

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Deleted().Create()
		te.clearEvents(ctx)

		msg := execErrMessage(te, ctx, restoreTenantMut, tenantArgs(tenantID, nil))
		assert.Contains(t, msg, "event stream could not be created")
		assert.NotContains(t, msg, "jetstream unavailable", "the NATS error must not reach the caller")

		got := mustGetTenant(t, te, tenantID)
		assert.False(t, got.DeletedAt.IsZero(), "tenant must remain soft-deleted without its stream")
		te.assertNoEvents(ctx)
	})

	t.Run("overrides expiry when input.expiresAt is supplied", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		oldExpiry := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		newExpiry := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(oldExpiry).Deleted().Create()
		te.clearEvents(ctx)

		data := execOK[restoreTenantData](te, ctx, restoreTenantWithExpiryMut, tenantArgs(tenantID, map[string]any{
			"ExpiresAt": newExpiry.Format(time.RFC3339),
		}))
		assert.True(t, data.RestoreTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.True(t, got.DeletedAt.IsZero())
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(newExpiry), "expiry should be overwritten with new value")
	})

	t.Run("is idempotent on an already-active tenant and ignores input.expiresAt", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		priorExpiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(priorExpiry).Create()
		te.clearEvents(ctx)

		data := execOK[restoreTenantData](te, ctx, restoreTenantWithExpiryMut, tenantArgs(tenantID, map[string]any{
			"ExpiresAt": "2050-01-01T00:00:00Z",
		}))
		assert.True(t, data.RestoreTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(priorExpiry), "expiresAt arg must be ignored on already-active path")

		te.assertNoEvents(ctx)
		assert.Empty(t, te.streams.ensuredTenants(), "an active tenant already has its stream")
	})

	// Pre-fix the resolver accepted any timestamp for input.expiresAt,
	// including values already in the past. The tenant got restored, the
	// Zitadel org reactivated, and then the periodic expiry sweep re-
	// disabled it within ~60s — operator saw a brief restore-then-vanish
	// with no error. Post-fix a past timestamp returns a typed error
	// before any side effects fire.
	t.Run("rejects past expiresAt", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Deleted().Create()
		te.clearEvents(ctx)

		past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
		execErr(te, ctx, restoreTenantWithExpiryMut, tenantArgs(tenantID, map[string]any{
			"ExpiresAt": past,
		}), "expiresAt must be in the future")

		// Tenant must still be deleted — restore did NOT fire.
		got := mustGetTenant(t, te, tenantID)
		assert.False(t, got.DeletedAt.IsZero(), "tenant must remain soft-deleted after a rejected restore")
		te.assertNoEvents(ctx)
		assert.Empty(t, te.streams.ensuredTenants(), "a rejected restore must create no stream")
	})

	t.Run("errors on unknown tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)

		execErr(te, ctx, restoreTenantMut, tenantArgs(tenantID, nil), "query tenant")
	})

	t.Run("rejects unauthenticated callers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, tenantID), tenantID).Deleted().Create()

		// See TestDeleteTenant/rejects_unauthenticated_callers for why
		// te.ctx (not ctxForTenant) is the right shape here.
		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, restoreTenantMut, tenantArgs(tenantID, nil), "authentication required")
	})

	// Regression test for M1: see TestDeleteTenant for the same shape.
	t.Run("rejects non-admin caller (M1: role gate)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("DormantTenant").Deleted().Create()

		writerCtx := te.ctx(userAWriter)
		execErr(te, writerCtx, restoreTenantMut, tenantArgs(resolver.TenantA, nil), "admin role required")
	})

	// Restore is an operator action, and this resolver is where that is
	// enforced. The tenant middleware also refuses a non-system caller that
	// names a disabled tenant, but that is a property of the header axis:
	// relax it to filtering and, without this guard, any ROLE_ADMIN holder —
	// including one holding admin through a cross-tenant grant — could undo
	// an operator's disable. The middleware is out of the picture here.
	t.Run("rejects a non-system caller holding ROLE_ADMIN on the target", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)
		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).
			Name("DisabledTenant").Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, restoreTenantMut, tenantArgs(resolver.TenantA, nil), "system caller required")

		got := mustGetTenant(t, te, resolver.TenantA)
		assert.False(t, got.DeletedAt.IsZero(), "the tenant must remain soft-deleted after a refused restore")
		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// SET TENANT EXPIRY
// =============================================================================

func TestSetTenantExpiry(t *testing.T) {
	t.Parallel()

	t.Run("sets expiry on an active tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()
		te.clearEvents(ctx)

		newExpiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		data := execOK[setTenantExpiryData](te, ctx, setTenantExpiryMut, tenantArgs(tenantID, map[string]any{
			"ExpiresAt": newExpiry.Format(time.RFC3339),
		}))
		assert.True(t, data.SetTenantExpiry.Success)

		got := mustGetTenant(t, te, tenantID)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(newExpiry))

		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("clears expiry when input.expiresAt is null", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantExpiryData](te, ctx, clearTenantExpiryMut, tenantArgs(tenantID, nil))
		assert.True(t, data.SetTenantExpiry.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.Nil(t, got.ExpiresAt)
	})

	t.Run("is idempotent when the value already matches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		expiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(expiry).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantExpiryData](te, ctx, setTenantExpiryMut, tenantArgs(tenantID, map[string]any{
			"ExpiresAt": expiry.Format(time.RFC3339),
		}))
		assert.True(t, data.SetTenantExpiry.Success)

		te.assertNoEvents(ctx)
	})

	// Pre-fix the equality check used Truncate(time.Second) on both sides.
	// Two values differing only at sub-second precision compared equal and
	// the resolver short-circuited Success without executing an Update —
	// no outbox event, no cache eviction, the caller's new value silently
	// lost. Post-fix any difference (even sub-second) goes through.
	// RFC3339 lacks sub-second precision, so the test uses
	// RFC3339Nano via a custom mutation that takes the raw string.
	t.Run("sub-second change fires the update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		stored := time.Date(2099, 1, 1, 12, 34, 56, 300*int(time.Millisecond), time.UTC)
		desired := time.Date(2099, 1, 1, 12, 34, 56, 500*int(time.Millisecond), time.UTC)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(stored).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantExpiryData](te, ctx, setTenantExpiryMut, tenantArgs(tenantID, map[string]any{
			"ExpiresAt": desired.Format(time.RFC3339Nano),
		}))
		assert.True(t, data.SetTenantExpiry.Success)

		got := mustGetTenant(t, te, tenantID)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(desired), "sub-second change must land on the row (got %v, want %v)", got.ExpiresAt, desired)
		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("errors on unknown tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)

		execErr(te, ctx, clearTenantExpiryMut, tenantArgs(tenantID, nil), "query tenant")
	})

	t.Run("errors on a soft-deleted tenant (invisible to this resolver)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, clearTenantExpiryMut, tenantArgs(tenantID, nil), "query tenant")
	})

	t.Run("rejects unauthenticated callers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, tenantID), tenantID).Create()

		// See TestDeleteTenant/rejects_unauthenticated_callers for why
		// te.ctx (not ctxForTenant) is the right shape here.
		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, clearTenantExpiryMut, tenantArgs(tenantID, nil), "authentication required")
	})

	// Regression test for M1: see TestDeleteTenant for the same shape.
	t.Run("rejects non-admin caller (M1: role gate)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("ExpiryTarget").Create()

		writerCtx := te.ctx(userAWriter)
		execErr(te, writerCtx, clearTenantExpiryMut, tenantArgs(resolver.TenantA, nil), "admin role required")
	})

	// The expiry is the deadline an operator imposes on the tenant. Since
	// registration grants the tenant's admin the admin project role, that
	// admin (and any cross-tenant ROLE_ADMIN holder) passes the role
	// gate; without this guard it could clear its own tenant's expiry and
	// outlive the operator's decision. The worst case is pinned here: an
	// attempt to clear an expiry that is already set.
	t.Run("rejects a non-system caller holding ROLE_ADMIN on the target", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		expiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		ctx := te.ctx(userA)
		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).
			Name("ExpiryLocked").ExpiresAt(expiry).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, clearTenantExpiryMut, tenantArgs(resolver.TenantA, nil), "system caller required")

		got := mustGetTenant(t, te, resolver.TenantA)
		require.NotNil(t, got.ExpiresAt, "the expiry must survive a refused setTenantExpiry")
		assert.True(t, got.ExpiresAt.Equal(expiry), "the expiry must remain unchanged after a refused setTenantExpiry (got %v)", got.ExpiresAt)
		te.assertNoEvents(ctx)
	})
}

// mustGetTenant reads a tenant including soft-deleted rows.
// =============================================================================
// SET TENANT UI TEMPLATE
// =============================================================================

func TestSetTenantUITemplate(t *testing.T) {
	t.Parallel()

	const (
		webKey     = core.RemoteWebUITemplateKey
		mobileKey  = core.RemoteMobileUITemplateKey
		webTmpl    = "https://cdn.example.com/t/web/{{.Slug}}/{{.Version}}/mf-manifest.json"
		mobileTmpl = "https://cdn.example.com/t/mobile/{{.Slug}}/{{.Version}}/widgets.rfw"
	)

	t.Run("system user sets both templates", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		})
		assert.True(t, data.SetTenantUITemplate.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.Equal(t, webTmpl, got.Data[webKey])
		assert.Equal(t, mobileTmpl, got.Data[mobileKey])

		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("writes only the provided field, leaving the other unchanged", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Data(map[string]any{mobileKey: "keep-mobile"}).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateWebOnlyMut, map[string]any{
			"Web": webTmpl,
		})
		assert.True(t, data.SetTenantUITemplate.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.Equal(t, webTmpl, got.Data[webKey])
		assert.Equal(t, "keep-mobile", got.Data[mobileKey])
	})

	t.Run("is idempotent when the values already match", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Data(map[string]any{webKey: webTmpl, mobileKey: mobileTmpl}).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		})
		assert.True(t, data.SetTenantUITemplate.Success)

		te.assertNoEvents(ctx)
	})

	t.Run("rejects a non-system (admin) caller", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Create()

		adminCtx := te.ctx(userA)
		execErr(te, adminCtx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		}, "system caller required")
	})

	t.Run("rejects an unauthenticated caller", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		}, "system caller required")
	})

	t.Run("rejects a request that changes nothing", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()

		execErr(te, ctx, setTenantUITemplateEmptyMut, nil, "at least one")
	})

	t.Run("rejects a malformed template", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()

		// Missing the {{.Version}} placeholder.
		execErr(te, ctx, setTenantUITemplateWebOnlyMut, map[string]any{
			"Web": "https://cdn.example.com/web/{{.Slug}}/mf-manifest.json",
		}, "invalid UI bundle URL template")
	})

	t.Run("rejects an invisible character in the host, keeping the stored template", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Data(map[string]any{webKey: webTmpl}).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, setTenantUITemplateWebOnlyMut, map[string]any{
			"Web": "https://ok\u202e.example.test/{{.Version}}",
		}, "invisible or control character")

		assert.Equal(t, webTmpl, mustGetTenant(t, te, tenantID).Data[webKey])
		te.assertNoEvents(ctx)
	})

	// A system caller is the only one who reaches template validation, so
	// the traversal guard is pinned here: a ".." segment would let the
	// rendered URL leave the owner prefix and load another tenant's
	// bundle. Both templates are checked; nothing is stored.
	t.Run("rejects a template whose path leaves the owner prefix with ..", func(t *testing.T) {
		t.Parallel()

		traversal := "https://cdn.example.com/{{.Owner}}/../../tenants/other/{{.Version}}/{{.Slug}}/mf.json"
		cases := map[string]map[string]any{
			"web":    {"Web": traversal, "Mobile": mobileTmpl},
			"mobile": {"Web": webTmpl, "Mobile": traversal},
		}
		for name, args := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				tenantID := uuidgql.GenerateV7UUID()
				ctx := te.ctxForTenant(systemUser, tenantID)
				te.newTenant(ctx, tenantID).Create()
				te.clearEvents(ctx)

				execErr(te, ctx, setTenantUITemplateMut, args, "invalid UI bundle URL template")

				got := mustGetTenant(t, te, tenantID)
				assert.NotContains(t, got.Data, webKey, "a refused request stores neither template")
				assert.NotContains(t, got.Data, mobileKey)
				te.assertNoEvents(ctx)
			})
		}
	})

	t.Run("clears a stored template, reverting to the default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Data(map[string]any{webKey: webTmpl, mobileKey: mobileTmpl}).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateClearWebMut, nil)
		assert.True(t, data.SetTenantUITemplate.Success)

		got := mustGetTenant(t, te, tenantID)
		_, webPresent := got.Data[webKey]
		assert.False(t, webPresent, "web template should be deleted, not blanked")
		assert.Equal(t, mobileTmpl, got.Data[mobileKey], "mobile template untouched")

		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("rejects setting and clearing the same template", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()

		execErr(te, ctx, setTenantUITemplateSetAndClearMut, map[string]any{
			"Web": webTmpl,
		}, "cannot set and clear")
	})
}

func mustGetTenant(t *testing.T, te *testEnv, id uuid.UUID) *ent.Tenant {
	t.Helper()
	tenant, err := te.Ent.Tenant.Get(te.ctxWithDeleted(systemUser), id)
	require.NoError(t, err)
	return tenant
}

var createUserMut = resolver.ParseTemplate(`mutation {
	createUser(input: {
		idpID: "331100000000000001"
		username: "sync-user"
		email: "sync-user@test.com"
		firstName: "Sync"
		lastName: "User"
		tenantID: "{{.TenantID}}"
	}) { id username tenantID }
}`)

type createUserData struct {
	CreateUser struct {
		ID       uuid.UUID
		Username string
		TenantID uuid.UUID
	}
}

// The management User table is a projection of Zitadel users owned by the
// zitadel-sync workflow: role assignment resolves its Zitadel target from a
// row's idp_id, so a tenant principal writing rows could redirect role
// grants to an arbitrary Zitadel user. Registration grants the tenant's
// admin ROLE_ADMIN, which passes the ent AllowIfAdmin mutation policy,
// so the resolvers must refuse every non-system caller themselves.
// Cross-tenant refusals are covered by the tenant-isolation scenarios.
func TestUserMutations_RequireSystemCaller(t *testing.T) {
	t.Parallel()

	t.Run("createUser refuses the tenant's own admin and admits the system user", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Create()

		args := map[string]any{"TenantID": resolver.TenantA}
		execErr(te, te.ctx(userA), createUserMut, args, "system caller required")

		data := execOK[createUserData](te, te.ctxForTenant(systemUser, resolver.TenantA), createUserMut, args)
		assert.Equal(t, "sync-user", data.CreateUser.Username)
		assert.Equal(t, resolver.TenantA, data.CreateUser.TenantID)
	})

	t.Run("updateUser and deleteUser refuse the tenant's own admin", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)
		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Create()
		seeded := te.newUser(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateUser, map[string]any{"ID": seeded.ID, "Username": "hijacked"}, "system caller required")
		execErr(te, ctx, deleteUser, map[string]any{"ID": seeded.ID}, "system caller required")

		got, err := te.Ent.User.Get(te.ctx(systemUser), seeded.ID)
		require.NoError(t, err)
		assert.Equal(t, seeded.Username, got.Username, "the projection row must be untouched after a refused updateUser")
		assert.True(t, got.DeletedAt.IsZero(), "the projection row must survive a refused deleteUser")
		te.assertNoEvents(ctx)
	})
}
