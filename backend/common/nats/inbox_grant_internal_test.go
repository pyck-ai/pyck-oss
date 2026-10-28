package nats

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/jwt/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// admit runs the decision and the minting Handle runs for one connection of
// user selecting header, and returns the claims Handle would sign.
func admit(t *testing.T, user authn.User, header http.Header) (*jwt.UserClaims, error) {
	t.Helper()

	service := newTestAuthService(t, livenessmocks.NoDisabledTenants)

	tenantID, err := service.authorizeTenantSelection(t.Context(), user, header)
	if err != nil {
		return nil, err
	}

	return service.mintUserClaims("UAUSERNKEY", tenantID, user.IsSystemUser()), nil
}

// The inbox grant follows the selected tenant, not the user's home tenant,
// and a system token, which the callout places in one tenant as well, gets
// the same grant.
func TestMintUserClaims_InboxGrantFollowsTheSelectedTenant(t *testing.T) {
	t.Parallel()

	home, selected := uuid.New(), uuid.New()

	cases := map[string]authn.User{
		"tenant user": {
			ID:       uuid.New(),
			TenantID: home,
			Username: "multi",
			Roles: map[uuid.UUID]authn.Role{
				home:     authn.ROLE_ADMIN,
				selected: authn.ROLE_READER,
			},
		},
		"system user": *authn.SystemUser(),
	}

	for name, user := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			claims, err := admit(t, user, tenantHeader(selected))
			require.NoError(t, err)

			require.Equal(t, selected.String(), claims.Name)
			assert.Equal(t, []string{TenantInboxPrefix(selected) + ".>"}, inboxReaching(claims.Sub.Allow))
		})
	}
}

// Fixed tenants, so a fuzz crasher replays with the same roles.
var (
	fuzzTenantReader = uuid.MustParse("0192f2a4-1111-7000-8000-000000000001")
	fuzzTenantAdmin  = uuid.MustParse("0192f2a4-2222-7000-8000-000000000002")
	fuzzTenantNone   = uuid.MustParse("0192f2a4-3333-7000-8000-000000000003")
)

// calloutFuzzUser picks the system user, a multi-tenant user or a
// single-tenant reader.
func calloutFuzzUser(who uint8) authn.User {
	switch who % 3 {
	case 0:
		return *authn.SystemUser()
	case 1:
		return authn.User{
			ID:       uuid.MustParse("0192f2a4-aaaa-7000-8000-00000000000a"),
			TenantID: fuzzTenantReader,
			Username: "multi",
			Roles: map[uuid.UUID]authn.Role{
				fuzzTenantReader: authn.ROLE_READER,
				fuzzTenantAdmin:  authn.ROLE_ADMIN,
				fuzzTenantNone:   authn.ROLE_NONE,
			},
		}
	default:
		return authn.User{
			ID:       uuid.MustParse("0192f2a4-bbbb-7000-8000-00000000000b"),
			TenantID: fuzzTenantReader,
			Username: "single",
			Roles:    map[uuid.UUID]authn.Role{fuzzTenantReader: authn.ROLE_READER},
		}
	}
}

// reachesInboxSpace reports whether pattern can match an inbox subject.
func reachesInboxSpace(pattern string) bool {
	first, _, _ := strings.Cut(pattern, ".")
	return first == "_INBOX" || first == "*" || first == ">"
}

// inboxReaching returns the patterns of subjects that can match an inbox.
func inboxReaching(subjects jwt.StringList) []string {
	var out []string
	for _, pattern := range subjects {
		if reachesInboxSpace(pattern) {
			out = append(out, pattern)
		}
	}
	return out
}

// assertInboxConfined asserts the claims admit user to a tenant it reads,
// subscribe on that tenant's inbox prefix only, and publish to no inbox.
func assertInboxConfined(t *testing.T, claims *jwt.UserClaims, user authn.User) {
	t.Helper()

	admitted, err := uuid.Parse(claims.Name)
	require.NoError(t, err)
	require.Equal(t, admitted.String(), claims.Name, "claims name must be the canonical tenant id")
	require.True(t, user.HasRole(authn.ROLE_READER, admitted), "admitted to tenant %s without a reader role", admitted)

	require.Equal(t, []string{TenantInboxPrefix(admitted) + ".>"}, inboxReaching(claims.Sub.Allow))
	require.Empty(t, inboxReaching(claims.Pub.Allow), "publish grants must not reach inboxes")
	require.Empty(t, inboxReaching(claims.Sub.Deny), "subscribe denies must not take the own inbox away")
}

// FuzzMintUserClaims_InboxGrantConfinedToAdmittedTenant: whatever tenant
// header a client sends, an admitted connection reaches only its own
// tenant's inbox. Run with:
//
//	go test ./nats/ -run '^$' -fuzz FuzzMintUserClaims_InboxGrantConfinedToAdmittedTenant
func FuzzMintUserClaims_InboxGrantConfinedToAdmittedTenant(f *testing.F) {
	reader, admin, none := fuzzTenantReader.String(), fuzzTenantAdmin.String(), fuzzTenantNone.String()
	seeds := []string{
		// real selections
		reader, admin, none, "", "all", reader + "," + admin, "all," + reader, reader + "," + reader,
		" " + reader + " ", uuid.Nil.String(), uuid.Max.String(),
		// alternative spellings uuid.Parse accepts
		strings.ToUpper(reader), "{" + reader + "}", "urn:uuid:" + reader, strings.ReplaceAll(reader, "-", ""),
		// subject metacharacters and break-outs
		"_INBOX.>", ">", "*", reader + ".>", reader + ".*", reader + " ", reader + "\x00",
		reader + "\n" + admin, "ALL", "all\u200b", "\uff10" + reader[1:], reader[:35],
	}
	for _, seed := range seeds {
		for who := range uint8(3) {
			f.Add(seed, who)
		}
	}

	f.Fuzz(func(t *testing.T, header string, who uint8) {
		user := calloutFuzzUser(who)

		claims, err := admit(t, user, http.Header{tenant.TenantIDHeader: []string{header}})
		if err != nil {
			return
		}

		assertInboxConfined(t, claims, user)
	})
}
