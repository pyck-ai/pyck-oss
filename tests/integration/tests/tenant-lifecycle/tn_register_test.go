//go:build integration

package tenants

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// Mirror of backend/management/workflows/identity.go and
// workflows/register-tenant/{workflow,activities}.go. Kept as local
// literals so the test stays decoupled from the worker package's
// dependency graph.
const (
	registerWorkflowIDPrefix = "register-tenant-"
	registerWorkflowType     = "RegisterTenantDbFirstWorkflow"
	loadRowActivityType      = "LoadTenantRegistrationActivity"
	createOrgActivityType    = "CreateTenantActivity"
	createAdminActivityType  = "CreateZitadelUserActivity"
	setStatusActivityType    = "SetTenantStatusActivity"

	// registerVisibilityTimeout bounds the wait for the finished
	// registration run to show up in Temporal visibility, which lags the
	// synchronous registerTenant response by the indexer's cadence.
	registerVisibilityTimeout = 30 * time.Second
)

func registerWorkflowID(tenantID string) string {
	return registerWorkflowIDPrefix + tenantID
}

// TnRegisterSuite proves the event-driven registration wiring end to end:
// the pending row's create event must travel outbox → JetStream → the
// tenant-lifecycle durable consumer → ExecuteWorkflow, the run must
// start from the row (load first, no signal) and finish with the admin
// able to authenticate with the password the hashed import carried. Unit
// tests cover the handler, the workflow and the SDK request shape in
// isolation; only a live stack exercises the consumer's FilterSubjects and
// Zitadel's acceptance of the bcrypt hash: a wrong hash format is
// accepted at import and only refused at login.
type TnRegisterSuite struct {
	tests.Base

	// Temporal is a workflow client on the management ("default")
	// namespace, presenting the system service token (see DeletedOrgSuite
	// for why an external client needs it).
	Temporal temporalclient.Client
}

func (s *TnRegisterSuite) SetupSuite() {
	s.Base.SetupSuite()

	tc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, "default", s.Cfg.ServiceToken)
	s.Require().NoError(err, "dial temporal default namespace")
	s.Temporal = tc
}

func (s *TnRegisterSuite) TearDownSuite() {
	if s.Temporal != nil {
		s.Temporal.Close()
	}
	s.Base.TearDownSuite()
}

// TestRegisterTenant_ConsumerStartsRunFromRow registers a tenant and reads
// the registration run's history: the run must have been started (not
// signalled) with nothing but the tenant ID as input, its first activity
// must be the row load (the row existed before anything was provisioned)
// the org creation must follow it, and the status flip must be the last
// activity. A regressed consumer filter would surface as a registerTenant
// timeout rather than as a different history, so the shape is asserted
// explicitly instead of relying on the mutation's success alone.
func (s *TnRegisterSuite) TestRegisterTenant_ConsumerStartsRunFromRow() {
	r := s.Require()
	tenant := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	r.NotEmpty(rt.ID)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status, "the resolver answers from the row's final status")

	workflowID := registerWorkflowID(rt.ID)
	runID, err := waitForCompletedRun(s.Ctx, s.Temporal, workflowID, registerWorkflowType, registerVisibilityTimeout)
	r.NoError(err, "the registration run must be visible as completed")

	events, err := historyEvents(s.Ctx, s.Temporal, workflowID, runID)
	r.NoError(err)
	r.NotEmpty(events)

	// The run is started by the consumer, never signalled.
	r.Equal(enums.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, events[0].eventType, "history: %v", events)
	r.Contains(events[0].input, rt.ID, "the start input names the tenant row")
	r.NotContains(strings.ToLower(events[0].input), "password", "nothing secret enters the start input (history: %s)", events[0].input)
	for _, ev := range events {
		r.NotEqual(enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED, ev.eventType, "no signal must be part of a registration run (history: %v)", events)
	}

	var activities []string
	for _, ev := range events {
		if ev.eventType == enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED {
			activities = append(activities, ev.activityType)
		}
	}
	r.NotEmpty(activities, "the workflow must have scheduled its activities")
	r.Equal(loadRowActivityType, activities[0], "the run starts from the row (history: %v)", events)
	r.Equal(createOrgActivityType, activities[1], "the org is created right after the load")
	r.Equal(setStatusActivityType, activities[len(activities)-1], "the row is flipped last")
	r.Equal(1, countOf(activities, setStatusActivityType), "exactly one status flip on a successful run")

	// A healthy stack must need no retry at all. The retry policy has no
	// attempt cap, so a step misclassified as retryable (or one that is
	// not idempotent under retry) costs every registration minutes of
	// backoff instead of failing visibly. The map is asserted non-empty
	// first: an attempt count that stops being extracted from history
	// would otherwise turn this guard into a loop over nothing.
	attempts := attemptsByActivity(events)
	r.NotEmpty(attempts, "no activity attempt could be read from the history (history: %v)", events)
	r.Contains(attempts, createOrgActivityType, "the org step must be among them (history: %v)", events)
	for name, attempt := range attempts {
		r.Equal(int32(1), attempt, "%s was retried on a healthy stack (history: %v)", name, events)
	}
}

// TestRegisterTenant_AdminCanAuthenticateWithPassword registers a tenant
// and opens a Zitadel session for its admin with the registration
// password: the only place a hash Zitadel cannot verify (wrong format, or
// a clear-text import of the hash) shows up.
func (s *TnRegisterSuite) TestRegisterTenant_AdminCanAuthenticateWithPassword() {
	r := s.Require()
	tenant := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status)

	// The user is created before the run returns, but Zitadel's user
	// projection may still be catching up when the mutation answers.
	var userID string
	err = tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		id, err := zitadelclient.CheckPassword(s.Ctx, s.ZConn, rt.IdpOrgRef, tenant.AdminUsername, tenant.AdminPassword)
		if err != nil {
			return err
		}
		userID = id
		return nil
	})
	r.NoError(err, "the admin must be able to authenticate with the registration password")
	r.NotEmpty(userID)

	_, err = zitadelclient.CheckPassword(s.Ctx, s.ZConn, rt.IdpOrgRef, tenant.AdminUsername, tenant.AdminPassword+"x")
	r.Error(err, "a wrong password must be refused; otherwise the check proves nothing")
}

// TestRegisterTenant_PersistsDataMap registers a tenant whose input
// carries a data map and reads it back from three vantage points: the
// mutation's own payload, the tenants query (the row's JSON column), and
// the Zitadel org metadata the workflow mirrors it into. The pending-row
// insert is the only writer of tenant.data on this path; a builder that
// hands the Go map to the driver unmarshalled is refused with
// "unsupported type map[string]interface {}" and the whole registration
// fails before any row exists. The JSON bool must come back as a bool,
// never as its string form. The payload deliberately carries no flavour
// key: data.flavour / isPyckGo select the pyck-go worker branch of the
// workflow, which is a different contract.
func (s *TnRegisterSuite) TestRegisterTenant_PersistsDataMap() {
	r := s.Require()
	tenant := fixtures.NewTenant()
	tenant.Data = map[string]any{"support-note": "issue-1402", "isSetupPending": true}

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err, "a data map must not break the pending-row insert")
	r.NotEmpty(rt.ID)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status)
	r.Equal(tenant.Data, rt.Data, "the mutation answers with the data it stored")

	stored, err := tenantByName(s.Ctx, s.Cfg, tenant.Name)
	r.NoError(err)
	r.Equal(rt.ID, stored.ID)
	r.Equal("done", stored.Status)
	r.Equal(tenant.Data, stored.Data, "the tenants query reads the same JSON back from the row")
	r.IsType(true, stored.Data["isSetupPending"], "a JSON bool must survive the round trip as a bool")

	// SetOrgMetadataActivity runs before the status flip, but the org
	// metadata list is a Zitadel projection that lags the write.
	want := map[string]string{"support-note": "issue-1402", "isSetupPending": "true"}
	var meta map[string]string
	err = tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		m, err := zitadelclient.OrgMetadata(s.Ctx, s.ZConn, rt.IdpOrgRef)
		if err != nil {
			return err
		}
		if len(m) < len(want) {
			return fmt.Errorf("org metadata not projected yet: %v", m)
		}
		meta = m
		return nil
	})
	r.NoError(err, "the workflow must mirror the data keys into org metadata")
	r.Equal(want, meta, "every data key lands in org metadata as its string form, nothing else")
}

// TestRegisterTenant_InvalidAdminEmailIsRefused proves the resolver
// refuses an admin account Zitadel would reject at user import before it
// writes anything: no row, so no name is held and no org is created for
// a typo.
func (s *TnRegisterSuite) TestRegisterTenant_InvalidAdminEmailIsRefused() {
	r := s.Require()
	tenant := fixtures.NewTenant()
	tenant.AdminEmail = "not-an-address"

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.Error(err, "an e-mail that is not an address must be refused")
	r.Nil(rt)
	r.Contains(err.Error(), "adminEmail")

	_, err = tenantByName(s.Ctx, s.Cfg, tenant.Name)
	r.Error(err, "a refused registration leaves no row behind")
	r.Contains(err.Error(), "not visible")
}

// TestRegisterTenant_PasswordWithSurroundingSpacesAuthenticatesAsSent
// registers a tenant whose admin password starts with a no-break space
// and ends with a space and logs the admin in with it. Zitadel stores the
// hash pyck computed and trims nothing, so the spaces are part of the
// password: the exact string opens a session, the trimmed one must not.
// A registerTenant that refused or trimmed such a password would either
// turn away a valid password or hand the admin one it never chose.
func (s *TnRegisterSuite) TestRegisterTenant_PasswordWithSurroundingSpacesAuthenticatesAsSent() {
	r := s.Require()
	tenant := fixtures.NewTenant()
	tenant.AdminPassword = "\u00a0" + tenant.AdminPassword + " x "

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err, "a password with surrounding spaces must register")
	r.NotNil(rt)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status)

	err = tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		_, err := zitadelclient.CheckPassword(s.Ctx, s.ZConn, rt.IdpOrgRef, tenant.AdminUsername, tenant.AdminPassword)
		return err
	})
	r.NoError(err, "the admin must authenticate with the password exactly as registered")

	_, err = zitadelclient.CheckPassword(s.Ctx, s.ZConn, rt.IdpOrgRef, tenant.AdminUsername, strings.TrimSpace(tenant.AdminPassword))
	r.Error(err, "the trimmed password must be refused: the spaces are part of the password")
}

// TestRegisterTenant_DecomposedPasswordIsRefusedComposedAuthenticatesAsSent
// pins the NFC rule of adminPassword on the live stack. The Password
// scalar keeps the bytes as sent and Zitadel compares the login's bytes
// with the hash, never normalizing; keyboards send NFC. A password whose
// "ö" arrives decomposed ("o" + U+0308, as text pasted from an NFD source
// arrives) is refused at registration, naming the rule and not the
// password, and leaves no tenant. The same password with a composed "ö"
// registers, the admin authenticates with exactly those bytes, and the
// decomposed form does not open the account: the hash is of the bytes as
// sent, not of a normalized form.
func (s *TnRegisterSuite) TestRegisterTenant_DecomposedPasswordIsRefusedComposedAuthenticatesAsSent() {
	base := fixtures.NewTenant().AdminPassword
	decomposed := base + "o\u0308"
	composed := base + "\u00f6"

	s.Run("decomposed is refused", func() {
		r := s.Require()
		tenant := fixtures.NewTenant()
		tenant.AdminPassword = decomposed
		defer s.cleanupIfRegistered(tenant.Name)

		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)

		r.Error(err, "a decomposed password must be refused")
		r.Nil(rt)
		r.Contains(err.Error(), "adminPassword is not in composed Unicode form (NFC)")
		r.NotContains(err.Error(), base, "the password is never echoed")
		r.Zero(s.tenantRowsLike(tenant.Name), "a refused registration leaves no row behind")
	})

	s.Run("composed authenticates as sent", func() {
		r := s.Require()
		tenant := fixtures.NewTenant()
		tenant.AdminPassword = composed

		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err, "a composed non-ASCII password must register")
		r.NotNil(rt)
		s.DeferTenantCleanup(rt.ID)
		r.Equal("done", rt.Status)

		err = tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
			_, err := zitadelclient.CheckPassword(s.Ctx, s.ZConn, rt.IdpOrgRef, tenant.AdminUsername, composed)
			return err
		})
		r.NoError(err, "the admin must authenticate with the composed bytes exactly as registered")

		_, err = zitadelclient.CheckPassword(s.Ctx, s.ZConn, rt.IdpOrgRef, tenant.AdminUsername, decomposed)
		r.Error(err, "the decomposed form is a different password and must be refused")
	})
}

// TestRegisterTenant_AdminEmailZitadelRefusesIsRefusedUpFront proves the
// admin e-mail is held to Zitadel's host-name rule before anything is
// written. Zitadel refuses an IDN domain or an IP literal only at user
// import, after the org exists, so without the up-front check the
// registration ends as an error row holding the name behind an ACTIVE
// org. Here it is refused at the wire, naming the field and the rule, and
// no row of the name exists.
func (s *TnRegisterSuite) TestRegisterTenant_AdminEmailZitadelRefusesIsRefusedUpFront() {
	for _, tc := range []struct{ name, domain string }{
		{"an IDN domain", "b\u00fccher.example"},
		{"an IP literal", "[127.0.0.1]"},
	} {
		s.Run(tc.name, func() {
			r := s.Require()
			tenant := fixtures.NewTenant()
			tenant.AdminEmail = "admin@" + tc.domain
			defer s.cleanupIfRegistered(tenant.Name)

			rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)

			r.Error(err, "an e-mail on %s must be refused", tc.domain)
			r.Nil(rt)
			r.Contains(err.Error(), "adminEmail")
			r.Contains(err.Error(), "other than an ASCII letter, digit or hyphen")
			r.NotContains(err.Error(), "tenant row marked error", "refused before the run, not by Zitadel")
			r.Zero(s.tenantRowsLike(tenant.Name), "a refused registration leaves no row behind")
		})
	}
}

// TestRegisterTenant_AdminEmailsAtTheEdgeOfZitadelsRuleRegister is the
// other half of the e-mail rule: every address pyck accepts must also be
// one Zitadel takes at user import, or the registration ends as an error
// row holding the name behind an ACTIVE org, the failure the up-front
// rule exists to prevent. The cases sit where pyck's transcription of
// Zitadel's rule is most likely to drift from the running Zitadel: a
// non-ASCII local part (Zitadel bounds only its length; a stricter
// RFC-5321 or HTML rule would refuse it), every atext special, a
// single-label host, an all-digit host, a 63-byte label, a 64-byte local
// part and an upper-case host. A Zitadel upgrade that tightens its e-mail
// rule turns this red before a customer meets the error row.
func (s *TnRegisterSuite) TestRegisterTenant_AdminEmailsAtTheEdgeOfZitadelsRuleRegister() {
	for _, tc := range []struct{ name, local, host string }{
		{"a non-ASCII local part", "\u00fcser", "example.test"},
		{"every atext special in the local part", "!#$%&'*+/=?^_`{|}~-", "example.test"},
		{"a single-label host", "admin", "localhost"},
		{"an all-digit host", "admin", "1.2.3.4"},
		{"a 63-byte host label", "admin", strings.Repeat("b", 63) + ".test"},
		{"a 64-byte local part", strings.Repeat("a", 64), "example.test"},
		{"an upper-case host", "admin", "EXAMPLE.TEST"},
	} {
		s.Run(tc.name, func() {
			r := s.Require()
			tenant := fixtures.NewTenant()
			tenant.AdminEmail = tc.local + "@" + tc.host

			rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
			s.cleanupIfRegistered(tenant.Name)

			r.NoError(err, "pyck accepted %q, so Zitadel must take it at user import", tenant.AdminEmail)
			r.Equal("done", rt.Status, "the registration finished; no error row holds the name")
		})
	}
}

// TestRegisterTenant_FailedProvisioningKeepsErrorRow proves the failure
// contract live: a registration whose provisioning fails after the org
// exists ends with the row marked error and nothing compensated (the org
// stays, the name stays held) and deleteTenant retires it through
// DisableTenantWorkflow. The deterministic trigger is a second tenant
// registering the first tenant's admin username: Zitadel's default domain
// policy makes usernames unique across the whole instance, so the hashed
// import answers AlreadyExists, the instance-wide lookup finds the name
// under the first tenant's organization, and the activity classifies it
// as admin-username-taken instead of burning retries on "user not found".
func (s *TnRegisterSuite) TestRegisterTenant_FailedProvisioningKeepsErrorRow() {
	r := s.Require()
	first := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, first)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	second := fixtures.NewTenant()
	second.AdminUsername = first.AdminUsername

	dup, err := gateway.RegisterTenant(s.Ctx, s.Cfg, second)
	r.Error(err, "a username held by another org's user cannot be provisioned")
	r.Nil(dup)
	r.Contains(err.Error(), "marked error", "the resolver must answer from the row's error status")
	r.Contains(err.Error(), "CreateZitadelUserActivity", "the failure must name the step that ended the run")
	// The step's refusal is answered with the fixed text of its error type;
	// the activity's own message and Zitadel's error behind it stay in the
	// log.
	// A taken username is answered like any refused admin account, so the
	// answer does not tell that the username exists on the instance.
	r.Contains(err.Error(), "step CreateZitadelUserActivity, admin-account-invalid: the admin account was refused")
	for _, raw := range []string{"admin-username-taken", "admin username is not available", "rpc error", "AlreadyExists", "Errors.", first.AdminUsername} {
		r.NotContains(err.Error(), raw, "the failure answer must not carry the dependency's own text")
	}
	// The answer reaches anonymous callers: Temporal's own error text (the
	// workflow ID, the run ID, the worker's host identity) stays in the log.
	for _, leak := range []string{registerWorkflowIDPrefix, "workflowID", "runID", "identity", "@"} {
		r.NotContains(err.Error(), leak, "the failure answer must not carry Temporal identifiers")
	}

	failed, err := tenantByName(s.Ctx, s.Cfg, second.Name)
	r.NoError(err)
	s.DeferTenantCleanup(failed.ID)
	r.Equal("error", failed.Status, "the row is kept, marked error")
	r.NotEmpty(failed.IdpOrgRef)

	state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, failed.IdpOrgRef)
	r.NoError(err)
	r.Equal("ACTIVE", state, "nothing is compensated: the org created before the failing step stays")

	again, err := gateway.RegisterTenant(s.Ctx, s.Cfg, second)
	r.Error(err, "an error row holds its name")
	r.Nil(again)
	r.Contains(err.Error(), "already exists")

	// The refusal must have ended the step on its first attempt. Under an
	// unbounded retry policy a step whose error is not classified is
	// retried for its whole schedule-to-close budget, which holds the
	// workflow ID and the row in pending for minutes instead of failing
	// at once, the failure mode #1436's classification exists to
	// prevent, and the only place a live Zitadel produces this outcome.
	failedRunID, err := waitForClosedRun(s.Ctx, s.Temporal, registerWorkflowID(failed.ID), registerWorkflowType, registerVisibilityTimeout)
	r.NoError(err, "the failed registration run must be visible as closed")

	failedEvents, err := historyEvents(s.Ctx, s.Temporal, registerWorkflowID(failed.ID), failedRunID)
	r.NoError(err)
	attempts := attemptsByActivity(failedEvents)
	r.Contains(attempts, createAdminActivityType, "the admin import must have run (history: %v)", failedEvents)
	r.Equal(int32(1), attempts[createAdminActivityType],
		"a username another org holds is refused on the first attempt, never retried (history: %v)", failedEvents)

	var scheduled []string
	for _, ev := range failedEvents {
		if ev.eventType == enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED {
			scheduled = append(scheduled, ev.activityType)
		}
	}
	r.Equal(1, countOf(scheduled, setStatusActivityType),
		"the failing run marks the row exactly once (history: %v)", failedEvents)
	r.Equal(setStatusActivityType, scheduled[len(scheduled)-1],
		"the error mark is the last thing a failed run does (history: %v)", failedEvents)

	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, failed.ID), "deleteTenant retires an error row")
	_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, failed.IdpOrgRef, "INACTIVE", 30*time.Second)
	r.NoError(err, "the delete event of an error row must dispatch DisableTenantWorkflow")
}

// tenantByName reads the tenant row with the given name through the
// tenants query with the system service token: the way to reach a row
// whose registration failed, since the mutation returned no tenant.
func tenantByName(ctx context.Context, cfg *config.Config, name string) (*gateway.RegisteredTenant, error) {
	c := gateway.NewClient(cfg, cfg.ServiceToken)
	first := 1
	resp, err := c.GetTenants(ctx, managementapi.GetTenantsArgs{
		First: &first,
		Where: &managementapi.TenantWhereInput{Name: &name},
	})
	if err != nil {
		return nil, fmt.Errorf("getTenants: %w", err)
	}
	for _, edge := range resp.GetTenants().GetEdges() {
		if edge == nil || edge.GetNode() == nil {
			continue
		}
		node := edge.GetNode()
		return &gateway.RegisteredTenant{
			ID:        node.GetID(),
			IdpOrgRef: node.GetIdpOrgRef(),
			Name:      node.GetName(),
			Status:    node.GetStatus().String(),
			Data:      node.GetData(),
		}, nil
	}
	return nil, fmt.Errorf("tenant %q not visible", name)
}

// attemptsByActivity maps each activity type of a run to the attempt
// number its task reached: 1 when it succeeded or was refused on the
// first try, higher when the retry policy had to come back to it.
func attemptsByActivity(events []historyEvent) map[string]int32 {
	scheduled := map[int64]string{}
	for _, ev := range events {
		if ev.eventType == enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED {
			scheduled[ev.id] = ev.activityType
		}
	}

	attempts := map[string]int32{}
	for _, ev := range events {
		if ev.eventType != enums.EVENT_TYPE_ACTIVITY_TASK_STARTED {
			continue
		}
		name, ok := scheduled[ev.scheduledEventID]
		if !ok {
			continue
		}
		if ev.attempt > attempts[name] {
			attempts[name] = ev.attempt
		}
	}
	return attempts
}

func countOf(items []string, want string) int {
	n := 0
	for _, it := range items {
		if it == want {
			n++
		}
	}
	return n
}

// waitForCompletedRun polls visibility for the run of the given workflow
// ID + type in COMPLETED status and returns its run ID. Visibility is used
// rather than Describe because the indexer lags the resolver's response.
func waitForCompletedRun(ctx context.Context, tc temporalclient.Client, workflowID, workflowType string, timeout time.Duration) (string, error) {
	query := fmt.Sprintf(`WorkflowId = %q AND WorkflowType = %q`, workflowID, workflowType)
	var runID string
	err := tests.PollUntil(ctx, timeout, 500*time.Millisecond, func() error {
		resp, err := tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{Query: query})
		if err != nil {
			return fmt.Errorf("list workflows: %w", err)
		}
		for _, e := range resp.GetExecutions() {
			if e.GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
				runID = e.GetExecution().GetRunId()
				return nil
			}
		}
		return fmt.Errorf("no completed %s run for %s yet", workflowType, workflowID)
	})
	return runID, err
}

// waitForClosedRun polls visibility for a run of the given workflow ID +
// type that has reached any terminal status, and returns its run ID. A
// failed registration never reaches COMPLETED, so waitForCompletedRun
// cannot be reused for it.
func waitForClosedRun(ctx context.Context, tc temporalclient.Client, workflowID, workflowType string, timeout time.Duration) (string, error) {
	query := fmt.Sprintf(`WorkflowId = %q AND WorkflowType = %q`, workflowID, workflowType)
	var runID string
	err := tests.PollUntil(ctx, timeout, 500*time.Millisecond, func() error {
		resp, err := tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{Query: query})
		if err != nil {
			return fmt.Errorf("list workflows: %w", err)
		}
		for _, e := range resp.GetExecutions() {
			if e.GetStatus() != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
				runID = e.GetExecution().GetRunId()
				return nil
			}
		}
		return fmt.Errorf("no closed %s run for %s yet", workflowType, workflowID)
	})
	return runID, err
}

// historyEvent is the slice of a history event the test reasons about.
type historyEvent struct {
	id           int64
	eventType    enums.EventType
	activityType string
	// input is the JSON of the start event's input payloads (empty for
	// every other event).
	input string
	// scheduledEventID and attempt come from ACTIVITY_TASK_STARTED, the
	// only event carrying an activity's attempt count: a retried activity
	// is scheduled ONCE and its attempts are counted on the start event,
	// so counting ACTIVITY_TASK_SCHEDULED would report every step as a
	// single attempt however long it was retried.
	scheduledEventID int64
	attempt          int32
}

func (e historyEvent) String() string {
	switch e.eventType {
	case enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
		return fmt.Sprintf("%d:%s(%s)", e.id, e.eventType, e.activityType)
	default:
		return fmt.Sprintf("%d:%s", e.id, e.eventType)
	}
}

// historyEvents reads the run's full history in order.
func historyEvents(ctx context.Context, tc temporalclient.Client, workflowID, runID string) ([]historyEvent, error) {
	iter := tc.GetWorkflowHistory(ctx, workflowID, runID, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var events []historyEvent
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("read history of %s: %w", workflowID, err)
		}
		var input strings.Builder
		for _, p := range ev.GetWorkflowExecutionStartedEventAttributes().GetInput().GetPayloads() {
			input.Write(p.GetData())
		}
		events = append(events, historyEvent{
			id:               ev.GetEventId(),
			eventType:        ev.GetEventType(),
			activityType:     ev.GetActivityTaskScheduledEventAttributes().GetActivityType().GetName(),
			input:            input.String(),
			scheduledEventID: ev.GetActivityTaskStartedEventAttributes().GetScheduledEventId(),
			attempt:          ev.GetActivityTaskStartedEventAttributes().GetAttempt(),
		})
	}
	return events, nil
}
