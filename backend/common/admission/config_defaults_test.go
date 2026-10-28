package admission_test

import (
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/admission"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
)

// registrationDefaults parses TenantRegistrationConfig against an empty
// environment, so every value below is the envDefault a deployment that
// configures nothing gets, not whatever happens to be exported in the
// shell that runs the tests.
func registrationDefaults(t *testing.T) envconfig.TenantRegistrationConfig {
	t.Helper()

	cfg, err := env.ParseAsWithOptions[envconfig.TenantRegistrationConfig](env.Options{
		Environment: map[string]string{},
	})
	require.NoError(t, err)
	return cfg
}

// The shipped default of PYCK_REGISTER_TENANT_MAX_INFLIGHT is the only
// thing bounding the anonymous registerTenant mutation, and New documents a
// non-positive cap as "unbounded", so a default that drifted to zero would
// switch the bound off in every deployment while every limiter test kept
// passing on its own hand-written cap.
//
// The parsed value is fed into a real Limiter rather than only compared to
// a number, so the assertion is that the shipped configuration refuses:
// with the cap saturated, the next execution is answered and never reaches
// the resolver.
func TestTenantRegistrationDefaults_BoundRegisterTenant(t *testing.T) {
	t.Parallel()

	cfg := registrationDefaults(t)

	require.Positive(t, cfg.RegisterTenantMaxInFlight,
		"a zero default switches the admission cap off: registerTenant is anonymous and each admitted execution costs a bcrypt hash plus the registration wait")
	require.Greater(t, cfg.RegistrationWait, cfg.RegistrationPoll,
		"the resolver must get at least one poll inside its wait, or it reports a timeout without ever having asked")

	l := admission.New(map[string]int{limitedField: cfg.RegisterTenantMaxInFlight})
	c := &counter{}

	held := make([]graphql.ResponseHandler, 0, cfg.RegisterTenantMaxInFlight)
	for range cfg.RegisterTenantMaxInFlight {
		held = append(held, admit(t, l, limitedField, c.next))
	}
	require.Equal(t, int64(cfg.RegisterTenantMaxInFlight), c.calls.Load(),
		"the configured cap must admit exactly that many concurrent registrations")

	refused := admit(t, l, limitedField, c.next)

	assertRefused(t, refused)
	assert.Equal(t, int64(cfg.RegisterTenantMaxInFlight), c.calls.Load(),
		"the execution past the configured cap must not reach the resolver")

	for _, h := range held {
		assertAdmitted(t, limitedField, h)
	}
}
