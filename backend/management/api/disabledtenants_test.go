package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/management/api"
)

// stubDisabledTenants implements just enough of the Client interface for
// the checker under test and records the IDs it was asked about.
type stubDisabledTenants struct {
	api.Client
	resp  *api.GetDisabledTenants
	err   error
	asked []string
}

func (s *stubDisabledTenants) GetDisabledTenants(_ context.Context, in api.GetDisabledTenantsArgs) (*api.GetDisabledTenants, error) {
	s.asked = in.Ids
	return s.resp, s.err
}

func TestNewDisabledTenantsSource_ReturnsParsedSubset(t *testing.T) {
	t.Parallel()

	a, b := uuid.New(), uuid.New()
	stub := &stubDisabledTenants{resp: &api.GetDisabledTenants{DisabledTenants: []string{b.String()}}}

	got, err := api.NewDisabledTenantsSource(stub)(t.Context(), []uuid.UUID{a, b})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{b}, got)
	assert.Equal(t, []string{a.String(), b.String()}, stub.asked, "all requested IDs must be forwarded as strings")
}

func TestNewDisabledTenantsSource_EmptyAnswerIsNoneDisabled(t *testing.T) {
	t.Parallel()

	stub := &stubDisabledTenants{resp: &api.GetDisabledTenants{}}
	got, err := api.NewDisabledTenantsSource(stub)(t.Context(), []uuid.UUID{uuid.New()})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestNewDisabledTenantsSource_EmptyInputSkipsClient(t *testing.T) {
	t.Parallel()

	stub := &stubDisabledTenants{err: errors.New("must not be called")}
	got, err := api.NewDisabledTenantsSource(stub)(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Nil(t, stub.asked)
}

// A transport / gateway error is wrapped and surfaced — the caller treats it
// as unknown state, not as "no tenant is disabled".
func TestNewDisabledTenantsSource_ClientErrorSurfaces(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	stub := &stubDisabledTenants{err: boom}
	got, err := api.NewDisabledTenantsSource(stub)(t.Context(), []uuid.UUID{uuid.New()})
	require.ErrorIs(t, err, boom, "transport error must be wrapped, not replaced")
	assert.Nil(t, got)
}

// A nil payload or an ID that does not parse is schema drift, surfaced as
// ErrDisabledTenantsInvalid rather than silently dropped (dropping would
// turn "disabled" into "allowed").
func TestNewDisabledTenantsSource_MalformedResponse(t *testing.T) {
	t.Parallel()

	nilStub := &stubDisabledTenants{}
	_, err := api.NewDisabledTenantsSource(nilStub)(t.Context(), []uuid.UUID{uuid.New()})
	require.ErrorIs(t, err, api.ErrDisabledTenantsInvalid)

	badStub := &stubDisabledTenants{resp: &api.GetDisabledTenants{DisabledTenants: []string{"not-a-uuid"}}}
	_, err = api.NewDisabledTenantsSource(badStub)(t.Context(), []uuid.UUID{uuid.New()})
	require.ErrorIs(t, err, api.ErrDisabledTenantsInvalid)
}
