package idempotency_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

func TestFromHeaders_Absent_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	got, err := idempotency.FromHeaders(http.Header{})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFromHeaders_Present_Trimmed(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set(idempotency.HeaderName, "  abc-123  ")

	got, err := idempotency.FromHeaders(h)
	require.NoError(t, err)
	assert.Equal(t, "abc-123", got)
}

func TestFromHeaders_OnlyWhitespace_TreatedAsAbsent(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set(idempotency.HeaderName, "   ")

	got, err := idempotency.FromHeaders(h)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFromHeaders_TooLong_Errors(t *testing.T) {
	t.Parallel()

	overflow := idempotency.MaxKeyLen + 17
	h := http.Header{}
	h.Set(idempotency.HeaderName, strings.Repeat("x", overflow))

	_, err := idempotency.FromHeaders(h)
	require.ErrorIs(t, err, idempotency.ErrKeyTooLong)
	// m3: error should carry both the max and the actual length so an
	// operator looking at logs can size the limit decision.
	assert.Contains(t, err.Error(), "max 255")
	assert.Contains(t, err.Error(), "got 272")
}

func TestFromHeaders_ExactlyMaxLen_OK(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set(idempotency.HeaderName, strings.Repeat("x", idempotency.MaxKeyLen))

	got, err := idempotency.FromHeaders(h)
	require.NoError(t, err)
	assert.Len(t, got, idempotency.MaxKeyLen)
}
