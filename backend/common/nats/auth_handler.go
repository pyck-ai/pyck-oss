package nats

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go/micro"
	"github.com/nats-io/nkeys"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// Sentinel rejections of authorizeTenantSelection; each one answers the
// connecting client with a 401 naming the axis that refused it.
var (
	errTenantNotServed = errors.New("tenant is not served")
)

const (
	// defaultCalloutTimeout bounds one whole callout (token introspection, tenant
	// selection, liveness) from the top of Handle. The endpoint handles
	// requests serially per replica, and not everything it calls bounds
	// itself: introspection rides an HTTP client with no overall timeout.
	// Anything slower than the NATS server's own callout deadline is
	// answered to a server that stopped listening, so waiting longer only
	// wedges the queue behind one stall.
	//
	// Must stay BELOW the server's authorization timeout
	// (config/nats/nats.conf, pinned by TestCalloutBudgetFitsNatsConfTimeout)
	// and ABOVE the liveness cache's configured lookup timeout, which bounds
	// the liveness lookup inside it. The three form one hierarchy:
	// server > callout > lookup, and NewAuthService refuses a configuration
	// that breaks the lower half of it.
	defaultCalloutTimeout = 2 * time.Second

	// defaultUserJWTTTL bounds the life of a tenant user's minted NATS credential.
	// The callout runs only at connect time, so without an expiry a client
	// already connected when its tenant is disabled or soft-deleted would
	// keep its pub/sub permissions (the stream's retained history
	// included) for the life of the TCP connection. The server disconnects
	// the client when its JWT expires, the client reconnects on its own,
	// and the callout re-evaluates role and liveness. Five minutes bounds
	// revocation at the same order of magnitude as the HTTP fleet's
	// liveness TTL without putting every client on a per-minute reconnect
	// cadence. System users are exempt (see mintUserClaims).
	defaultUserJWTTTL = 5 * time.Minute

	// userJWTTTLJitter randomizes each minted expiry so a cohort admitted
	// together (a mass reconnect after a NATS restart or an outage
	// recovery) does not re-expire in lockstep every period forever, each
	// wave hitting the serial callout with a cold liveness cache. Spread
	// evenly around the TTL; one cycle desynchronizes any cohort. A TTL
	// below twice this spread gets half the TTL as its spread instead, so
	// no credential is minted already expired and each lives at least
	// three quarters of the configured TTL.
	userJWTTTLJitter = 2 * time.Minute
)

// AuthOption overrides a callout default; pass the service's configured
// values (envconfig.NatsConfig) from main. A non-positive duration is
// ignored, so an unset config field leaves the default bound in place.
type AuthOption func(*authService)

// WithCalloutTimeout overrides defaultCalloutTimeout. It must stay below
// the NATS server's own authorization timeout and above the liveness
// cache's lookup timeout; NewAuthService refuses the second violation and
// the package tests pin the first.
func WithCalloutTimeout(d time.Duration) AuthOption {
	return func(s *authService) {
		if d > 0 {
			s.calloutTimeout = d
		}
	}
}

// WithUserJWTTTL overrides defaultUserJWTTTL, the life of a minted tenant
// credential and with it the worst-case delay before a disabled tenant's
// connected client is re-evaluated.
func WithUserJWTTTL(d time.Duration) AuthOption {
	return func(s *authService) {
		if d > 0 {
			s.userJWTTTL = d
		}
	}
}

type authService struct {
	logger        log.Logger
	streamName    string
	accountName   string
	issuerKeyPair nkeys.KeyPair
	authn         authn.Authenticator
	// livenessCache holds the tenant verdicts under liveness.TenantKey;
	// shared with the auth provider and the tenant-events subscriber.
	livenessCache *liveness.Cache
	// tenantSource answers cache misses (management's tenant table).
	tenantSource liveness.TenantSource
	// faultLog keeps a failing source from writing one Error line per
	// connection attempt: the shared source going down would otherwise
	// emit at the reconnect rate of every NATS client.
	faultLog *log.FaultThrottle
	// contextBase yields the process context Handle roots per-request work
	// in: callout requests arrive without a context of their own, and a
	// detached context would keep in-flight Zitadel and DB work running
	// through a shutdown and drop the service's log fields from fault
	// lines. A func rather than a stored context, like authz.ClaimMapper.
	contextBase func() context.Context
	// calloutTimeout and userJWTTTL are the configured budgets of this
	// service; immutable after construction. See defaultCalloutTimeout
	// and defaultUserJWTTTL for what each one bounds.
	calloutTimeout time.Duration
	userJWTTTL     time.Duration
}

func newAuthService(ctx context.Context, streamName string, accountName string, authProvider authn.Authenticator, keyPair nkeys.KeyPair, cache *liveness.Cache, source liveness.TenantSource, opts ...AuthOption) *authService {
	service := &authService{
		logger:        *log.ForContext(ctx),
		streamName:    streamName,
		accountName:   accountName,
		authn:         authProvider,
		issuerKeyPair: keyPair,
		livenessCache: cache,
		tenantSource:  source,
		faultLog:      log.NewFaultThrottle(log.FaultLogWindow),
		contextBase:   func() context.Context { return ctx },

		calloutTimeout: defaultCalloutTimeout,
		userJWTTTL:     defaultUserJWTTTL,
	}
	for _, apply := range opts {
		apply(service)
	}
	return service
}

func (service *authService) Handle(r micro.Request) {
	ctx, cancel := context.WithTimeout(service.contextBase(), service.calloutTimeout)
	defer cancel()
	logger := service.logger

	defer func() {
		if rec := recover(); rec != nil {
			logger.Error().
				Interface("panic", rec).
				Msg("Recovered from panic in auth handler")

			err := r.Error("500", fmt.Sprintf("Internal server error: %v", rec), nil)
			if err != nil {
				logger.Err(err).Msg("Error responding to request after panic")
			}
		}
	}()

	// The request carries the client's credentials (connect options token
	// and password), so neither the raw data nor the decoded claims or
	// connect options are ever logged: only identifiers.
	logger.Debug().
		Str("request_subject", r.Subject()).
		Str("request_reply", r.Reply()).
		Msg("Auth handler received request")

	rc, err := jwt.DecodeAuthorizationRequestClaims(string(r.Data()))
	if err != nil {
		logger.Err(err).Msg("Failed to decode auth request")
		err = r.Error("500", fmt.Sprintf("Failed to decode auth request: %v", err.Error()), nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}
	logger.Debug().
		Str("user_nkey", rc.UserNkey).
		Interface("server", rc.Server).
		Interface("client_info", rc.ClientInformation).
		Str("client_name", rc.ConnectOptions.Name).
		Msg("Decoded auth request details")

	if rc.ConnectOptions.Token == "" {
		logger.Error().Msg("Missing token in request")
		err = r.Error("401", "Authentication failed: Missing user token", nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}
	logger.Debug().Msg("Found token in request")

	user, err := service.authn.Authenticate(ctx, rc.ConnectOptions.Token)
	if err != nil {
		// Info, not Error: a refused credential is an expected outcome of
		// this endpoint, and during a dependency outage every reconnect of
		// every denied client reaches this line; Error-level here would
		// bury the throttled line naming the outage's cause.
		logger.Info().Err(err).Msg("Token authorization failed")
		err = r.Error("401", fmt.Sprintf("Authentication failed: %v", err.Error()), nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}
	logger.Debug().
		Str("user_id", user.ID.String()).
		Str("user_tenant_id", user.TenantID.String()).
		Msg("User authorized successfully")

	userNkey := rc.UserNkey
	serverId := rc.Server.ID

	// TODO(michael): The callout enforces the single-tenant guard, the
	// ROLE_READER floor and the tenant liveness verdict (see
	// authorizeTenantSelection), but still not the full middleware chain
	// the HTTP and GRPC handlers run, notably RBAC beyond the reader
	// floor. Route it through the same middleware logic to close that gap.
	tenantID, err := service.authorizeTenantSelection(ctx, user, http.Header(r.Headers()))
	if err != nil {
		// Info for the same reason as the Authenticate refusal above.
		logger.Info().Err(err).Msg("Tenant authorization failed")
		err = r.Error("401", fmt.Sprintf("Authentication failed: %v", err.Error()), nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}

	claims := service.mintUserClaims(userNkey, tenantID, user.IsSystemUser())

	logger.Debug().
		Str("user_nkey", userNkey).
		Str("server_id", serverId).
		Str("tenant_id", tenantID.String()).
		Strs("allowed_pub_patterns", claims.Pub.Allow).
		Strs("allowed_sub_patterns", claims.Sub.Allow).
		Msg("Creating permissions")

	token, err := service.validateAndSign(claims)
	service.Respond(r, userNkey, serverId, token, err)
}

// mintUserClaims builds the user claims of one authorized connection:
// pub/sub permissions scoped to the tenant's own stream (tenantPermissions),
// and (for tenant users) an expiry of the configured TTL, which is what
// forces a connected client back through the role and liveness checks
// after its tenant is disabled. System users get no expiry: their access
// is not derived from tenant liveness, and an expiring credential would
// put every platform service on a reconnect cadence for no added
// enforcement.
func (service *authService) mintUserClaims(userNkey string, tenantID uuid.UUID, systemUser bool) *jwt.UserClaims {
	claims := jwt.NewUserClaims(userNkey)
	claims.Audience = service.accountName // account placement, case-sensitive
	claims.Name = tenantID.String()
	claims.Permissions = tenantPermissions(service.streamName, tenantID)
	if !systemUser {
		// The jitter spreads expiry instants across a cohort; it guards
		// no secret, so a predictable generator is fine.
		ttl := service.userJWTTTL
		if jitter := min(userJWTTTLJitter, ttl/2); jitter > 0 {
			ttl += rand.N(jitter) - jitter/2 //nolint:gosec
		}
		claims.Expires = time.Now().Add(ttl).Unix()
	}
	return claims
}

// authorizeTenantSelection resolves the connect headers to the single
// tenant this connection is scoped to and authorizes it on three axes:
// the selection must name exactly one tenant, the user must hold at least
// ROLE_READER on it, and a live row must back it; a soft-deleted tenant's
// retained JetStream history must not stay readable through a stale grant.
// System users skip the role and liveness axes: they are how platform
// services connect, and their identity is validated by the system-role
// check at introspection. The exactly-one guard applies to them too: an
// ambiguous selection has no answer for anyone. The first two axes are
// resolveTenant's; this adds the liveness axis on top.
// Extracted from Handle so the decision is testable without a NATS server.
func (service *authService) authorizeTenantSelection(ctx context.Context, user authn.User, headers http.Header) (uuid.UUID, error) {
	// The single-tenant guard and the ROLE_READER floor: refused before
	// tenant state is ever consulted, so the callout is no tenant-state
	// oracle for the unauthorized.
	tenantID, err := resolveTenant(ctx, user, headers)
	if err != nil {
		return uuid.Nil, err
	}

	if user.IsSystemUser() {
		return tenantID, nil
	}

	// LookupTenants bounds this wait itself (the cache's configured lookup
	// timeout), which keeps a stalled source from wedging the serial callout
	// handler; a timeout surfaces as err and refuses the uncached tenant
	// below like any other source fault.
	live, err := service.livenessCache.LookupTenants(ctx, []uuid.UUID{tenantID}, service.tenantSource)
	if err != nil {
		// Cached verdicts were applied; a tenant with no verdict is refused
		// below: a connection that cannot be verified is not granted
		// tenant-scoped permissions. Nothing is cached from the fault, so
		// the client's reconnect succeeds with the first answering lookup.
		service.faultLog.Err(log.ForContext(ctx), err).
			Msg("disabled-tenant lookup failed; refusing uncached tenants")
	}
	if !live[tenantID] {
		return uuid.Nil, fmt.Errorf("%w %q", errTenantNotServed, tenantID)
	}
	return tenantID, nil
}

func (service *authService) Respond(req micro.Request, userNKey string, serverId string, userJWT string, err error) {
	logger := service.logger

	rc := jwt.NewAuthorizationResponseClaims(userNKey)
	rc.Audience = serverId
	rc.Jwt = userJWT
	if err != nil {
		rc.Error = err.Error()
	}

	// Decode the nested JWT to log its contents instead of the raw token
	var decodedUserClaims *jwt.UserClaims
	if userJWT != "" {
		var err error
		decodedUserClaims, err = jwt.DecodeUserClaims(userJWT)
		if err != nil {
			logger.Err(err).Msg("Failed to decode user JWT for logging")
		}
	}

	// Create a copy of response claims without the JWT for logging
	rcForLogging := *rc
	rcForLogging.Jwt = "[REDACTED]"

	// Log the response claims before encoding, but with decoded JWT instead of raw
	logger.Debug().
		Str("user_nkey", userNKey).
		Str("server_id", serverId).
		Interface("decoded_user_claims", decodedUserClaims). // Log decoded claims instead of raw JWT
		Interface("response_claims", rcForLogging).          // Use the copy with redacted JWT
		Msg("Preparing response claims")

	token, err := rc.Encode(service.issuerKeyPair)
	if err != nil {
		logger.Err(err).
			Interface("response_claims", rcForLogging). // Use the copy with redacted JWT
			Msg("error encoding response jwt:")
		return
	}

	// Verify the token can be decoded before sending
	_, err = jwt.DecodeAuthorizationResponseClaims(token)
	if err != nil {
		logger.Err(err).Msg("Generated token fails verification")
		return
	}

	logger.Debug().Msg("Sending verified response token")

	err = req.Respond([]byte(token))
	if err != nil {
		logger.Err(err).Msg("error responding to request")
	}
}

func (service *authService) validateAndSign(claims *jwt.UserClaims) (string, error) {
	// Validate the claims.
	vr := jwt.CreateValidationResults()
	claims.Validate(vr)
	if len(vr.Errors()) > 0 {
		return "", errors.Join(vr.Errors()...)
	}

	// Sign it with the issuer key since this is non-operator mode.
	return claims.Encode(service.issuerKeyPair)
}
