package nats_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go/micro"
	"github.com/nats-io/nkeys"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/nats"
)

// The tests here drive Handle end to end (a signed authorization request in,
// a signed authorization response out) through the public constructor, the
// way management wires it. The selection decision and the minted claims have
// their own unit tests next to the handler; this file pins the wiring around
// them: the account placement, that every refused caller is answered with a
// 401 rather than a user JWT, and that no credential reaches the log.

type stubAuthenticator struct {
	user authn.User
	err  error
}

func (s stubAuthenticator) Authenticate(_ context.Context, _ string) (authn.User, error) {
	return s.user, s.err
}

func (s stubAuthenticator) HTTPMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

// stubRequest records what the handler answered.
type stubRequest struct {
	data []byte

	response  []byte
	errCode   string
	errDetail string
}

var _ micro.Request = (*stubRequest)(nil)

func (r *stubRequest) Respond(data []byte, _ ...micro.RespondOpt) error {
	r.response = data
	return nil
}

func (r *stubRequest) RespondJSON(_ any, _ ...micro.RespondOpt) error { return nil }

func (r *stubRequest) Error(code, description string, _ []byte, _ ...micro.RespondOpt) error {
	r.errCode = code
	r.errDetail = description
	return nil
}

func (r *stubRequest) Data() []byte           { return r.data }
func (r *stubRequest) Headers() micro.Headers { return nil }
func (r *stubRequest) Subject() string        { return nats.NatsAuthSubject }
func (r *stubRequest) Reply() string          { return "_INBOX.test" }

// authRequest builds the request the NATS server sends for a connecting client.
func authRequest(t *testing.T, token string) (req *stubRequest, userNkey string, serverID string) {
	t.Helper()

	return authRequestWith(t, jwt.ConnectOptions{Token: token})
}

// authRequestWith is authRequest with the client's full connect options.
func authRequestWith(t *testing.T, opts jwt.ConnectOptions) (req *stubRequest, userNkey string, serverID string) {
	t.Helper()

	serverKP, err := nkeys.CreateServer()
	require.NoError(t, err)
	serverID, err = serverKP.PublicKey()
	require.NoError(t, err)

	userKP, err := nkeys.CreateUser()
	require.NoError(t, err)
	userNkey, err = userKP.PublicKey()
	require.NoError(t, err)

	rc := jwt.NewAuthorizationRequestClaims(serverID)
	rc.Server = jwt.ServerID{Name: "test-nats", ID: serverID}
	rc.UserNkey = userNkey
	rc.ConnectOptions = opts

	encoded, err := rc.Encode(serverKP)
	require.NoError(t, err)

	return &stubRequest{data: []byte(encoded)}, userNkey, serverID
}

// handlerFor wires the auth service the same way management does, with a
// liveness source that reports every tenant live.
func handlerFor(t *testing.T, accountName string, auth authn.Authenticator) micro.Handler {
	t.Helper()

	accountKP, err := nkeys.CreateAccount()
	require.NoError(t, err)
	seed, err := accountKP.Seed()
	require.NoError(t, err)

	cfg, err := nats.NewAuthService(t.Context(), "test", "pyck", accountName, auth, string(seed),
		livenessmocks.NewTestCache(t), livenessmocks.NoDisabledTenants)
	require.NoError(t, err)

	return cfg.Endpoint.Handler
}

// readerOn returns a tenant user holding the ROLE_READER floor the callout
// demands on each of the given tenants.
func readerOn(tenantIDs ...uuid.UUID) authn.User {
	roles := make(map[uuid.UUID]authn.Role, len(tenantIDs))
	for _, tenantID := range tenantIDs {
		roles[tenantID] = authn.ROLE_READER
	}

	return authn.User{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Username: "tester",
		Roles:    roles,
	}
}

// mintedUserClaims decodes the user JWT out of a successful response.
func mintedUserClaims(t *testing.T, req *stubRequest) *jwt.UserClaims {
	t.Helper()

	require.Empty(t, req.errCode, "handler answered with error %s: %s", req.errCode, req.errDetail)
	require.NotEmpty(t, req.response, "handler did not respond")

	responseClaims, err := jwt.DecodeAuthorizationResponseClaims(string(req.response))
	require.NoError(t, err)
	require.Empty(t, responseClaims.Error, "authorization response carries an error")

	userClaims, err := jwt.DecodeUserClaims(responseClaims.Jwt)
	require.NoError(t, err)

	return userClaims
}

// The audience places the connection into a NATS account, by exact name: a
// value that disagrees with the server config rejects every
// callout-authenticated connection with an authorization violation, so the
// account must be whatever the service was configured with, casing included.
func TestHandlePlacesUserInConfiguredAccount(t *testing.T) {
	t.Parallel()

	for _, accountName := range []string{"pyck", "PYCK", "SomeOtherAccount"} {
		t.Run(accountName, func(t *testing.T) {
			t.Parallel()

			tenantID := uuid.New()
			handler := handlerFor(t, accountName, stubAuthenticator{user: readerOn(tenantID)})

			req, userNkey, serverID := authRequest(t, "a-token")
			handler.Handle(req)

			userClaims := mintedUserClaims(t, req)
			assert.Equal(t, accountName, userClaims.Audience, "user JWT audience")
			assert.Equal(t, userNkey, userClaims.Subject, "user JWT subject")
			assert.Equal(t, tenantID.String(), userClaims.Name, "user JWT name")

			responseClaims, err := jwt.DecodeAuthorizationResponseClaims(string(req.response))
			require.NoError(t, err)
			assert.Equal(t, serverID, responseClaims.Audience, "response audience must be the server ID")
		})
	}
}

// A selection that does not resolve to exactly one tenant is answered with a
// 401 and no user JWT, including a system user, whose "all" expansion holds
// no per-tenant roles.
func TestHandleRejectsUsersWithoutSingleTenant(t *testing.T) {
	t.Parallel()

	tests := map[string]authn.User{
		"no tenant":      readerOn(),
		"two tenants":    readerOn(uuid.New(), uuid.New()),
		"system service": *authn.SystemUser(),
	}

	for name, user := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := handlerFor(t, "pyck", stubAuthenticator{user: user})

			req, _, _ := authRequest(t, "a-token")
			handler.Handle(req)

			assert.Equal(t, "401", req.errCode, req.errDetail)
			assert.Empty(t, req.response, "handler issued a user JWT for a user without a single tenant")
		})
	}
}

func TestHandleRejectsUnauthenticatedRequests(t *testing.T) {
	t.Parallel()

	t.Run("missing token", func(t *testing.T) {
		t.Parallel()

		handler := handlerFor(t, "pyck", stubAuthenticator{user: readerOn(uuid.New())})

		req, _, _ := authRequest(t, "")
		handler.Handle(req)

		assert.Equal(t, "401", req.errCode, req.errDetail)
		assert.Empty(t, req.response)
	})

	t.Run("rejected token", func(t *testing.T) {
		t.Parallel()

		handler := handlerFor(t, "pyck", stubAuthenticator{err: errors.New("invalid token")})

		req, _, _ := authRequest(t, "a-token")
		handler.Handle(req)

		assert.Equal(t, "401", req.errCode, req.errDetail)
		assert.Empty(t, req.response)
	})
}

const (
	callerToken    = "caller-bearer-token-must-not-be-logged"
	callerPassword = "caller-password-must-not-be-logged"
)

// TestAuthHandlerNeverLogsCredentials drives the callout at Debug level
// through a success, a refused token and an undecodable request, and
// asserts no log line carries the caller's token or password.
func TestAuthHandlerNeverLogsCredentials(t *testing.T) {
	t.Parallel()

	authorized := readerOn(uuid.New())
	authorized.Token = callerToken

	withCredentials := func(t *testing.T) *stubRequest {
		t.Helper()
		req, _, _ := authRequestWith(t, jwt.ConnectOptions{
			Token:    callerToken,
			Username: "caller",
			Password: callerPassword,
			Name:     "test-client",
		})
		return req
	}

	tests := []struct {
		name     string
		auth     stubAuthenticator
		req      func(t *testing.T) *stubRequest
		wantCode string
	}{
		{
			name: "authorized",
			auth: stubAuthenticator{user: authorized},
			req:  withCredentials,
		},
		{
			name:     "authentication refused",
			auth:     stubAuthenticator{err: errors.New("token rejected")},
			req:      withCredentials,
			wantCode: "401",
		},
		{
			name: "undecodable request",
			auth: stubAuthenticator{user: authorized},
			req: func(*testing.T) *stubRequest {
				return &stubRequest{data: []byte("not-a-jwt " + callerToken + " " + callerPassword)}
			},
			wantCode: "500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			ctx := log.Context(t.Context(), zerolog.New(&buf).Level(zerolog.DebugLevel))

			issuer, err := nkeys.CreateAccount()
			require.NoError(t, err)

			req := tt.req(t)
			nats.NewAuthHandler(ctx, "pyck", "pyck", tt.auth, issuer,
				livenessmocks.NewTestCache(t), livenessmocks.NoDisabledTenants).Handle(req)

			assert.Equal(t, tt.wantCode, req.errCode, req.errDetail)
			if tt.wantCode == "" {
				assert.NotEmpty(t, req.response, "authorized request must be answered")
			}

			logged := buf.String()
			require.NotEmpty(t, logged, "the handler must log at Debug level")
			assert.NotContains(t, logged, callerToken)
			assert.NotContains(t, logged, callerPassword)
		})
	}
}
