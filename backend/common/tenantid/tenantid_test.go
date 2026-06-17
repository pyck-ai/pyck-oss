package tenantid_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/tenantid"
)

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()

	a := uuid.MustParse("14bdf7fa-a6e3-568e-889b-df70f3a0c3fa")
	b := uuid.MustParse("8150d697-7784-558b-b112-00983a9a7327")

	ctx := tenantid.Context(context.Background(), a, b)

	assert.Equal(t, []uuid.UUID{a, b}, tenantid.FromContext(ctx))
}

func TestFromContextWithoutValueReturnsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, tenantid.FromContext(context.Background()))
}

func TestFromContextNilContextReturnsNil(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // explicitly exercising the nil-context guard
	assert.Nil(t, tenantid.FromContext(nil))
}

func TestString(t *testing.T) {
	t.Parallel()

	a := uuid.MustParse("14bdf7fa-a6e3-568e-889b-df70f3a0c3fa")
	b := uuid.MustParse("8150d697-7784-558b-b112-00983a9a7327")

	tests := map[string]struct {
		ids  []uuid.UUID
		want string
	}{
		"none":     {ids: nil, want: ""},
		"single":   {ids: []uuid.UUID{a}, want: a.String()},
		"multiple": {ids: []uuid.UUID{a, b}, want: a.String() + "," + b.String()},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tenantid.String(tc.ids))
		})
	}
}

// TestFieldNamesMatchConvention pins the telemetry field names. Changing
// them breaks downstream log/trace queries, so require an explicit code change.
func TestFieldNamesMatchConvention(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "tenant_id", tenantid.LogField)
	assert.Equal(t, "tenant.id", tenantid.AttributeKey)
}
