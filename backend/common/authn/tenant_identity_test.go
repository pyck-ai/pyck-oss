package authn_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
)

// The tenant ID must be what a login token for the org resolves to, or the
// registered admin can never reach their tenant.
func TestNewTenantIdentity_MatchesTokenDerivation(t *testing.T) {
	t.Parallel()

	const audience = "http://localhost:8080"

	orgID, tenantID := authn.NewTenantIdentity(audience)

	_, err := uuid.Parse(orgID)
	require.NoError(t, err, "org id must be a uuid string")
	assert.Equal(t, authn.ComputeUUID(audience, orgID), tenantID)

	otherOrgID, otherTenantID := authn.NewTenantIdentity(audience)
	assert.NotEqual(t, orgID, otherOrgID, "org ids must be random")
	assert.NotEqual(t, tenantID, otherTenantID)

	assert.NotEqual(t, tenantID, authn.ComputeUUID("http://other-stage", orgID),
		"the audience groups tenant ids per stage")
}

// Tenants registered before pyck chose organization IDs carry the numeric
// IDs Zitadel minted, and both shapes must derive the same way: the org ref
// is an opaque string to the derivation. The values are pinned rather than
// recomputed: a change to ComputeUUID or to the audience re-keys every
// deployed tenant at once, and every service resolves its tokens to a
// tenant row that no longer exists.
func TestComputeUUID_DeployedTenantIdentitiesAreStable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		audience string
		orgID    string
		want     string
	}{
		{"zitadel-minted org, local audience", "http://localhost:8080", "375900000000000001", "98fb7af5-2cbf-5f13-aa8c-6b98e9aae0a0"},
		{"zitadel-minted org, deployed audience", "https://zitadel.pyck.ai", "318472901234567890", "7b67c968-53d1-51d4-89e8-5afce64f99e5"},
		{"pyck-chosen org, local audience", "http://localhost:8080", "0199d7d0-1111-7000-8000-000000000001", "00a157f4-f68c-5081-b51a-a787b7a4068a"},
		{"pyck-chosen org, deployed audience", "https://zitadel.pyck.ai", "0199d7d0-1111-7000-8000-000000000001", "cd142ae0-84b5-524f-92d1-605aed59aa8c"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, authn.ComputeUUID(tc.audience, tc.orgID).String())
		})
	}
}
