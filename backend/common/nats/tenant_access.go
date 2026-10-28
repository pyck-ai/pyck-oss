package nats

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/nats-io/jwt/v2"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// tenantConsumerSuffixes name the consumers a tenant may use: "<tenantID>--<suffix>".
var tenantConsumerSuffixes = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"} //nolint:gochecknoglobals

// errTenantNotSingle is returned when the connect headers do not resolve to
// exactly one tenant. Permissions are scoped to a single tenant's subjects,
// so a connection cannot span several.
var errTenantNotSingle = errors.New("exactly one tenant required")

// resolveTenant decides which tenant a NATS client may be scoped to.
//
// It mirrors the role rule of tenant.HTTPMiddleware, not its liveness rule:
// the tenant headers are parsed, and the user must hold at least ROLE_READER
// in every tenant they resolve to, but a disabled tenant is not refused. On top
// of that the result must be exactly one tenant. The special "all" value (or
// no tenant header at all) expands to every tenant the user holds a role in,
// so it is accepted only when that expansion yields a single tenant.
//
// The liveness rule is applied on top of it by
// authService.authorizeTenantSelection.
//
// Service clients do not reach this function: their credentials are listed
// in the server's auth_callout auth_users and bypass the callout.
func resolveTenant(ctx context.Context, user authn.User, header http.Header) (uuid.UUID, error) {
	userCtx := authn.Context(ctx, &user)

	tenantIDs, err := tenant.ParseHeaders(userCtx, header)
	if err != nil {
		return uuid.Nil, err
	}

	if len(tenantIDs) != 1 {
		return uuid.Nil, fmt.Errorf("%w, got %d", errTenantNotSingle, len(tenantIDs))
	}

	tenantID := tenantIDs[0]
	if !user.HasRole(authn.ROLE_READER, tenantID) {
		return uuid.Nil, fmt.Errorf("%w %q", tenant.ErrNoAccessToTenantID, tenantID)
	}

	return tenantID, nil
}

// stateChangeDenyPattern returns the subject pattern covering every Temporal
// workflow state-change subject of the stream, whatever namespace it names
// and however many tokens follow "temporal". The format is
// events.TemporalWorkflowStateChangeTopic.
func stateChangeDenyPattern(streamName string) string {
	return streamName + ".*.temporal.>"
}

// TenantInboxPrefix is the inbox prefix (nats.CustomInboxPrefix) of tenantID's
// clients, the only subjects they may subscribe to.
func TenantInboxPrefix(tenantID uuid.UUID) string {
	return "_INBOX." + tenantID.String()
}

// tenantPermissions limits a tenant client's JetStream API calls and publishes
// to its own stream; listings are denied, since stream names carry tenant IDs.
// The server does not check reply subjects against them. A cross-account
// answer is delivered on the client's reply subject in the tenant account
// (config/nats/nats.conf), so it never reaches a stream; TestTenantPermissions
// "cannot have its requests answered into another stream" pins that server
// behaviour. All tenants share that account, so a client may subscribe to its
// own inbox only. Not covered: a push consumer's deliver_subject (#1751).
func tenantPermissions(streamPrefix string, tenantID uuid.UUID) jwt.Permissions {
	stream := topic.TenantStreamName(streamPrefix, tenantID)
	subjects := topic.TenantStreamSubjects(streamPrefix, tenantID)

	allowedPub := make([]string, 0, 3+5*len(tenantConsumerSuffixes))
	allowedPub = append(allowedPub,
		"$JS.API.INFO",
		subjects,
		"$JS.API.STREAM.INFO."+stream,
	)

	for _, suffix := range tenantConsumerSuffixes {
		consumer := fmt.Sprintf("%s--%s", tenantID, suffix)

		allowedPub = append(allowedPub,
			fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.%s.%s", stream, consumer, subjects),
			fmt.Sprintf("$JS.API.CONSUMER.DELETE.%s.%s", stream, consumer),
			fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.%s", stream, consumer),
			fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s", stream, consumer),
			fmt.Sprintf("$JS.ACK.%s.%s.>", stream, consumer),
		)
	}

	// Also denied explicitly, so that a broader allow cannot bring them back.
	deniedPub := []string{
		"$JS.API.STREAM.LIST",
		"$JS.API.STREAM.NAMES",
		"$JS.API.CONSUMER.LIST.>",
		"$JS.API.CONSUMER.NAMES.>",
		streamPrefix + ".*.crud.>",
		// Custom events go through sendCustomEvent, which needs the writer role.
		streamPrefix + ".*.custom.>",
		// State changes come from the Temporal server only.
		stateChangeDenyPattern(streamPrefix),
	}

	return jwt.Permissions{
		Pub: jwt.Permission{Allow: allowedPub, Deny: deniedPub},
		Sub: jwt.Permission{Allow: []string{TenantInboxPrefix(tenantID) + ".>"}},
	}
}
