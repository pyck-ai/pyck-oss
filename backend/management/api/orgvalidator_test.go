package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/management/api"
)

// stubGetOrg implements just enough of the Client interface for the
// validator under test. Other methods would be panic-on-call — but
// stubOrgClient only ever has GetOrganization called.
type stubGetOrg struct {
	api.Client
	resp *api.GetOrganization
	err  error
}

func (s *stubGetOrg) GetOrganization(_ context.Context, _ api.GetOrganizationArgs) (*api.GetOrganization, error) {
	return s.resp, s.err
}

// Active=true on a well-formed Organization edge → validator returns (true, nil).
func TestNewOrganizationValidator_ActiveTrue(t *testing.T) {
	t.Parallel()

	id := "org-123"
	stub := &stubGetOrg{resp: &api.GetOrganization{
		Organization: api.GetOrganization_Organization{Active: true, ID: &id},
	}}

	v := api.NewOrganizationValidator(stub)
	active, err := v(context.Background(), "sub")
	require.NoError(t, err)
	assert.True(t, active, "well-formed Active=true edge must return active")
}

// Active=false on a well-formed Organization edge → validator returns (false, nil).
// This is a legitimate "tenant is inactive" answer.
func TestNewOrganizationValidator_ActiveFalse(t *testing.T) {
	t.Parallel()

	id := "org-123"
	stub := &stubGetOrg{resp: &api.GetOrganization{
		Organization: api.GetOrganization_Organization{Active: false, ID: &id},
	}}

	v := api.NewOrganizationValidator(stub)
	active, err := v(context.Background(), "sub")
	require.NoError(t, err)
	assert.False(t, active, "well-formed Active=false edge must return inactive")
}

// Zero-valued Organization edge (no ID set) → validator returns an error,
// NOT (Active=false, nil). A zero-valued response means the gateway / codegen
// could not decode the `organization` field — schema drift, partial response,
// nilable-pointer change — and conflating that with a real revocation would
// 401 every healthy tenant in the fleet on a single malformed response.
func TestNewOrganizationValidator_ZeroValueResponse_ReturnsError(t *testing.T) {
	t.Parallel()

	stub := &stubGetOrg{resp: &api.GetOrganization{
		Organization: api.GetOrganization_Organization{}, // ID nil, Active zero
	}}

	v := api.NewOrganizationValidator(stub)
	active, err := v(context.Background(), "sub")
	require.Error(t, err, "zero-valued response must NOT be returned as (false, nil) — that is a silent fleet-wide revocation")
	assert.False(t, active, "error path returns active=false by convention")
	assert.ErrorIs(t, err, api.ErrOrganizationInvalid, "must use the typed error so callers can errors.Is")
}

// Nil top-level response (already covered) is symmetric with the zero-valued
// case — both indicate the codegen could not produce a usable payload.
func TestNewOrganizationValidator_NilResponse_ReturnsError(t *testing.T) {
	t.Parallel()

	stub := &stubGetOrg{resp: nil}

	v := api.NewOrganizationValidator(stub)
	active, err := v(context.Background(), "sub")
	require.Error(t, err)
	assert.False(t, active)
	assert.ErrorIs(t, err, api.ErrOrganizationInvalid)
}

// Transport / gateway errors propagate verbatim and translate to an error
// — never to (Active=false, nil). authn.rejectOrgActive treats validator
// errors as "stay cached," which is the correct response to a transient
// gateway blip.
func TestNewOrganizationValidator_TransportError_Propagates(t *testing.T) {
	t.Parallel()

	upstream := errors.New("connection refused")
	stub := &stubGetOrg{err: upstream}

	v := api.NewOrganizationValidator(stub)
	active, err := v(context.Background(), "sub")
	require.ErrorIs(t, err, upstream, "must wrap the underlying transport error")
	assert.False(t, active)
}
