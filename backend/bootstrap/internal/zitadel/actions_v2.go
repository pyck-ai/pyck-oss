package zitadel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	action_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/action/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pyck-ai/pyck/backend/common/log"

	"github.com/pyck-ai/pyck/backend/bootstrap/internal/exporters"
)

var (
	errMissingWebhookBaseURL = errors.New("webhook base URL is required")
	// errTargetNotFound is the sentinel for "no target with that name yet";
	// distinguished from a real RPC failure so callers can fall through to
	// CreateTarget instead of bailing.
	errTargetNotFound = errors.New("pyck pre-token Target not found")
)

const (
	// PyckPreTokenTargetName is the canonical Zitadel Target name for the
	// pyck-management webhook that injects pyck_tenant_id into OIDC tokens.
	PyckPreTokenTargetName = "pyck_pre_token" //nolint:gosec // Action name, not a credential

	// PyckLoginEventTargetName is the Zitadel Target for the pyck-management
	// webhook that emits a NATS event on human login. Separate from the
	// pre-token Target: wired to Event executions and a fire-and-forget
	// RestWebhook (not a body-reading RestCall).
	PyckLoginEventTargetName = "pyck_login_event"

	pyckActionTimeoutV2 = 10 * time.Second
)

// loginEventTypes are the eventstore events that mark a human completing a
// primary authentication (a login). They fire regardless of login UI version
// and, unlike token/session creation, never fire on token refresh — one NATS
// event per real login. Covers all three credential paths:
//   - password            → user.human.password.check.succeeded
//   - external IdP / SSO   → user.human.externallogin.check.succeeded
//   - passwordless/passkey → user.human.passwordless.token.check.succeeded
//
// MFA second-factor checks are excluded (they would double-fire with the
// primary check); ".token.verified" is excluded (passkey registration).
var loginEventTypes = []string{
	"user.human.password.check.succeeded",
	"user.human.externallogin.check.succeeded",
	"user.human.passwordless.token.check.succeeded",
}

// ensureActionsV2 provisions the Actions v2 Target + Executions that emit
// pyck_tenant_id as a plain top-level OIDC claim. Replaces the dead-in-v4.12
// v1 Action approach.
//
// The webhookBaseURL is supplied by Configuration.PreTokenWebhookBaseURL
// (e.g. http://management:8082 inside docker compose) — bootstrap appends
// /webhook/zitadel/actions/pre-token. The exports list comes from
// zitadel.pre_token_action.exports in the bootstrap YAML and drives where
// the HMAC signing_key is published (env files for compose, k8s Secrets
// for production). keyCreation is the same shape: if any guard reports the
// key is already present at its destination, this function is a no-op for
// the run.
//
// Idempotency:
//   - keyCreation guards drive the skip decision. If the signing-key entry
//     already exists at any configured guard destination (env file, k8s
//     Secret, file), the Target+Execution provisioning is skipped — no
//     Zitadel call.
//   - When the guard says the key is missing, the Target is forcibly
//     refreshed: any existing Target by the same name is deleted (we can't
//     read its server-side signing key), then CreateTarget mints a fresh
//     key, which flows out through the exports.
//   - The signing key is NEVER persisted to disk by this function. The
//     only authoritative copies are whatever the YAML exports declare —
//     typically a `0600`-mode env file (compose) or a k8s Secret
//     (production).
func (s *Seeder) ensureActionsV2(
	ctx context.Context,
	conn *zitadel.Connection,
	webhookBaseURL string,
	keyCreation, exports []*exporters.Export,
) error {
	logger := log.ForContext(ctx)

	if webhookBaseURL == "" {
		return errMissingWebhookBaseURL
	}

	// Skip-on-guard: if the signing key is already present at any guard
	// destination, we trust that pyck-management has it too. No Zitadel
	// calls, no key rotation. This is the steady-state path.
	if len(keyCreation) > 0 {
		exists, err := s.exporters.CredentialsExist(ctx, keyCreation)
		if err != nil {
			return fmt.Errorf("check pyck pre-token signing-key guards: %w", err)
		}
		if exists {
			logger.Debug().Msg("pyck pre-token signing key already present at guard destination; skipping Actions v2 provisioning")
			return nil
		}
	}

	client := action_pb.NewActionServiceClient(conn)
	endpoint := webhookBaseURL + "/webhook/zitadel/actions/pre-token"

	targetID, signingKey, err := s.recreatePyckPreTokenTarget(ctx, client, endpoint)
	if err != nil {
		return fmt.Errorf("ensure Target: %w", err)
	}

	for _, fn := range []string{"preuserinfo", "preaccesstoken"} {
		if _, err := client.SetExecution(ctx, &action_pb.SetExecutionRequest{
			Condition: &action_pb.Condition{
				ConditionType: &action_pb.Condition_Function{
					Function: &action_pb.FunctionExecution{Name: fn},
				},
			},
			Targets: []string{targetID},
		}); err != nil {
			return fmt.Errorf("SetExecution(function=%s): %w", fn, err)
		}
	}

	// Fan out the freshly-minted signing key through the configured
	// exporters. Local compose pipes it into config/keys/bootstrap.env +
	// process-env; K8s stages route it into a Secret. These are the ONLY
	// durable copies — the key is not written anywhere else by bootstrap.
	if err := s.exportFields(ctx,
		map[string]string{"signing_key": signingKey},
		exports,
		"pyck pre-token action signing key"); err != nil {
		return fmt.Errorf("export pyck pre-token action signing key: %w", err)
	}

	logger.Debug().
		Str("target_id", targetID).
		Str("endpoint", endpoint).
		Msg("Provisioned Zitadel Actions v2 Target + Executions for pyck_tenant_id claim")
	return nil
}

// recreatePyckPreTokenTarget unconditionally produces a fresh pre-token
// Target + signing_key. RestCall: the response body is read and used for
// claim injection (RestWebhook would ignore it — wrong here). Token issuance
// must succeed even if the webhook fails, hence InterruptOnError: false.
//
// Returns (targetID, signingKey).
func (s *Seeder) recreatePyckPreTokenTarget(
	ctx context.Context, client action_pb.ActionServiceClient,
	endpoint string,
) (string, string, error) {
	return s.recreateTarget(ctx, client, &action_pb.CreateTargetRequest{
		Name:     PyckPreTokenTargetName,
		Endpoint: endpoint,
		Timeout:  durationpb.New(pyckActionTimeoutV2),
		TargetType: &action_pb.CreateTargetRequest_RestCall{
			RestCall: &action_pb.RESTCall{InterruptOnError: false},
		},
	})
}

// recreateTarget unconditionally produces a fresh Target + signing_key by
// deleting any existing Target with req.Name and creating it anew from req.
// The Target's signing_key is server-minted at creation and only returned in
// the CreateTargetResponse — there is no "read existing key" primitive on
// Zitadel's side, so a refresh always rotates the key.
//
// Returns (targetID, signingKey).
func (s *Seeder) recreateTarget(
	ctx context.Context, client action_pb.ActionServiceClient,
	req *action_pb.CreateTargetRequest,
) (string, string, error) {
	existing, err := findTargetByName(ctx, client, req.GetName())
	if err != nil && !errors.Is(err, errTargetNotFound) {
		return "", "", err
	}
	if existing != nil {
		if _, err := client.DeleteTarget(ctx, &action_pb.DeleteTargetRequest{Id: existing.GetId()}); err != nil {
			return "", "", fmt.Errorf("DeleteTarget(stale): %w", err)
		}
	}

	resp, err := client.CreateTarget(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("CreateTarget: %w", err)
	}
	return resp.GetId(), resp.GetSigningKey(), nil
}

// ensureLoginEventAction provisions the Actions v2 Target + Event Executions
// that POST to pyck-management on human login.
//
// (Re)create-and-export happens when the Target is absent OR the exported
// signing key is missing at its destination (the keyCreation guard, like
// pre-token). The key can't be read back from Zitadel, so a lost export can
// only be restored by rotating the Target — this is what makes the setup
// self-healing. Otherwise the Target and its key are kept.
//
// Unlike pre-token (which skips entirely when the key is present), the
// Endpoint/Timeout and the Executions are always reconciled in place: a moved
// base URL is corrected, and editing loginEventTypes converges on a plain
// rerun (binds every entry, drops any Execution still wired to this Target for
// an event no longer in the list).
//
// The Target is a fire-and-forget RestWebhook (InterruptOnError: false), so a
// failed webhook never disrupts login.
//
// NOTE: CreateTarget validates the endpoint by resolving its host, so on the
// create path `management` must resolve (bootstrap claims that alias before
// management starts; a running management answers too).
func (s *Seeder) ensureLoginEventAction(
	ctx context.Context,
	client action_pb.ActionServiceClient,
	webhookBaseURL string,
	keyCreation, exports []*exporters.Export,
) error {
	logger := log.ForContext(ctx)

	if webhookBaseURL == "" {
		return errMissingWebhookBaseURL
	}

	endpoint := webhookBaseURL + "/webhook/zitadel/login"

	existing, err := findTargetByName(ctx, client, PyckLoginEventTargetName)
	if err != nil && !errors.Is(err, errTargetNotFound) {
		return fmt.Errorf("look up login-event Target: %w", err)
	}

	// Self-healing key guard: when no guard is configured we default to
	// "present" (so an existing Target is reconciled, not rotated).
	keyPresent := true
	if len(keyCreation) > 0 {
		keyPresent, err = s.exporters.CredentialsExist(ctx, keyCreation)
		if err != nil {
			return fmt.Errorf("check login-event signing-key guards: %w", err)
		}
	}

	var targetID string
	if existing == nil || !keyPresent {
		if existing != nil && !keyPresent {
			logger.Warn().Msg("login-event signing key missing at export destination; rotating Target to restore it")
		}
		targetID, err = s.createLoginTarget(ctx, client, endpoint, exports)
		if err != nil {
			return err
		}
	} else {
		targetID = existing.GetId()
		if err := reconcileLoginTargetEndpoint(ctx, client, existing, endpoint); err != nil {
			return err
		}
	}

	if err := reconcileLoginExecutions(ctx, client, targetID); err != nil {
		return err
	}

	logger.Debug().
		Str("target_id", targetID).
		Str("endpoint", endpoint).
		Strs("events", loginEventTypes).
		Msg("Ensured Zitadel Actions v2 Target + Event Executions for login NATS event")
	return nil
}

// reconcileLoginTargetEndpoint updates the Target's Endpoint/Timeout in place
// when they drift from the desired values, so a changed base URL doesn't
// silently leave the Target POSTing to a dead URL. UpdateTarget without
// ExpirationSigningKey keeps the existing server-minted key.
func reconcileLoginTargetEndpoint(ctx context.Context, client action_pb.ActionServiceClient, existing *action_pb.Target, endpoint string) error {
	if existing.GetEndpoint() == endpoint && existing.GetTimeout().AsDuration() == pyckActionTimeoutV2 {
		return nil
	}
	ep := endpoint
	if _, err := client.UpdateTarget(ctx, &action_pb.UpdateTargetRequest{
		Id:       existing.GetId(),
		Endpoint: &ep,
		Timeout:  durationpb.New(pyckActionTimeoutV2),
	}); err != nil {
		return fmt.Errorf("update login-event Target: %w", err)
	}
	log.ForContext(ctx).Info().Str("target_id", existing.GetId()).Str("endpoint", endpoint).
		Msg("reconciled login-event Target endpoint/timeout")
	return nil
}

// createLoginTarget creates the fire-and-forget RestWebhook Target and exports
// its freshly minted signing key. Returns the new Target ID.
func (s *Seeder) createLoginTarget(ctx context.Context, client action_pb.ActionServiceClient, endpoint string, exports []*exporters.Export) (string, error) {
	id, signingKey, err := s.recreateTarget(ctx, client, &action_pb.CreateTargetRequest{
		Name:     PyckLoginEventTargetName,
		Endpoint: endpoint,
		Timeout:  durationpb.New(pyckActionTimeoutV2),
		TargetType: &action_pb.CreateTargetRequest_RestWebhook{
			RestWebhook: &action_pb.RESTWebhook{InterruptOnError: false},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create login-event Target: %w", err)
	}
	if err := s.exportFields(ctx,
		map[string]string{"signing_key": signingKey},
		exports,
		"pyck login-event action signing key"); err != nil {
		return "", fmt.Errorf("export pyck login-event action signing key: %w", err)
	}
	return id, nil
}

// reconcileLoginExecutions binds an Event Execution for every loginEventTypes
// entry to targetID, then removes any event Execution still pointing at
// targetID for an event no longer in loginEventTypes — Zitadel removes an
// Execution when SetExecution is called with no targets.
func reconcileLoginExecutions(ctx context.Context, client action_pb.ActionServiceClient, targetID string) error {
	desired := make(map[string]bool, len(loginEventTypes))
	for _, eventType := range loginEventTypes {
		desired[eventType] = true
		if _, err := client.SetExecution(ctx, eventExecutionRequest(eventType, []string{targetID})); err != nil {
			return fmt.Errorf("SetExecution(event=%s): %w", eventType, err)
		}
	}

	resp, err := client.ListExecutions(ctx, &action_pb.ListExecutionsRequest{})
	if err != nil {
		return fmt.Errorf("list login executions: %w", err)
	}
	for _, ex := range resp.GetExecutions() {
		event := ex.GetCondition().GetEvent().GetEvent()
		if event == "" || desired[event] || !slices.Contains(ex.GetTargets(), targetID) {
			continue
		}
		if _, err := client.SetExecution(ctx, eventExecutionRequest(event, nil)); err != nil {
			return fmt.Errorf("remove stale execution(event=%s): %w", event, err)
		}
	}
	return nil
}

// eventExecutionRequest builds a SetExecutionRequest binding the given event
// condition to targets; nil/empty targets removes the Execution.
func eventExecutionRequest(eventType string, targets []string) *action_pb.SetExecutionRequest {
	return &action_pb.SetExecutionRequest{
		Condition: &action_pb.Condition{
			ConditionType: &action_pb.Condition_Event{
				Event: &action_pb.EventExecution{
					Condition: &action_pb.EventExecution_Event{Event: eventType},
				},
			},
		},
		Targets: targets,
	}
}

// findTargetByName returns the existing Target with the given name, or
// errTargetNotFound if absent. Other errors are wrapped and propagated.
func findTargetByName(ctx context.Context, client action_pb.ActionServiceClient, name string) (*action_pb.Target, error) {
	resp, err := client.ListTargets(ctx, &action_pb.ListTargetsRequest{
		Filters: []*action_pb.TargetSearchFilter{
			{
				Filter: &action_pb.TargetSearchFilter_TargetNameFilter{
					TargetNameFilter: &action_pb.TargetNameFilter{TargetName: name},
				},
			},
		},
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, errTargetNotFound
		}
		return nil, fmt.Errorf("ListTargets: %w", err)
	}
	for _, t := range resp.GetTargets() {
		if t.GetName() == name {
			return t, nil
		}
	}
	return nil, errTargetNotFound
}
