package requestid_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"

	"github.com/pyck-ai/pyck/backend/common/requestid"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

func TestWithRequestIDRoundTrip(t *testing.T) {
	t.Parallel()

	id := uuidgql.GenerateV7UUID().String()

	ctx, err := requestid.WithRequestID(context.Background(), id)
	require.NoError(t, err)

	assert.Equal(t, id, requestid.FromContext(ctx))
}

func TestFromContextEmptyWhenAbsent(t *testing.T) {
	t.Parallel()

	assert.Empty(t, requestid.FromContext(context.Background()))
}

func TestWithRequestIDPreservesExistingBaggage(t *testing.T) {
	t.Parallel()

	other, err := baggage.NewMember("other.key", "other-value")
	require.NoError(t, err)
	bag, err := baggage.New(other)
	require.NoError(t, err)
	ctx := baggage.ContextWithBaggage(context.Background(), bag)

	id := uuid.NewString()
	ctx, err = requestid.WithRequestID(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, id, requestid.FromContext(ctx))
	assert.Equal(t, "other-value", baggage.FromContext(ctx).Member("other.key").Value())
}

func TestWithRequestIDRejectsInvalidValue(t *testing.T) {
	t.Parallel()

	// baggage.NewMember rejects values containing characters outside the
	// allowed set; a raw control character triggers that path.
	_, err := requestid.WithRequestID(context.Background(), "bad\x01value")
	require.Error(t, err)
}
