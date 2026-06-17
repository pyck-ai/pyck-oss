package idempotency_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

func TestSerializeResponse_RoundTrip(t *testing.T) {
	t.Parallel()

	resp := &graphql.Response{
		Data: json.RawMessage(`{"hello":"world"}`),
		Errors: gqlerror.List{
			&gqlerror.Error{Message: "warn"},
		},
		Extensions: map[string]any{"k": float64(1)},
	}

	b, err := idempotency.SerializeResponse(resp, 0)
	require.NoError(t, err)

	got, err := idempotency.DeserializeResponse(b)
	require.NoError(t, err)

	assert.JSONEq(t, string(resp.Data), string(got.Data))
	require.Len(t, got.Errors, 1)
	assert.Equal(t, "warn", got.Errors[0].Message)
	assert.InDelta(t, float64(1), got.Extensions["k"], 0)
}

func TestSerializeResponse_NilErrors(t *testing.T) {
	t.Parallel()

	_, err := idempotency.SerializeResponse(nil, 0)
	assert.Error(t, err)
}

func TestDeserializeResponse_EmptyErrors(t *testing.T) {
	t.Parallel()

	_, err := idempotency.DeserializeResponse(nil)
	assert.Error(t, err)
}

func TestDeserializeResponse_InvalidJSON_Errors(t *testing.T) {
	t.Parallel()

	_, err := idempotency.DeserializeResponse([]byte(`{not json`))
	assert.Error(t, err)
}

func TestSerializeResponse_OverLimit_ReturnsErrResponseTooLarge(t *testing.T) {
	t.Parallel()

	// Build a Data field bigger than DefaultMaxResponseBytes so the
	// serialized envelope is guaranteed to exceed the cap.
	big := strings.Repeat("x", idempotency.DefaultMaxResponseBytes+1)
	payload, err := json.Marshal(map[string]string{"big": big})
	require.NoError(t, err)

	_, err = idempotency.SerializeResponse(&graphql.Response{Data: payload}, 0)
	require.ErrorIs(t, err, idempotency.ErrResponseTooLarge)
	// m1-style: error should carry both the limit and the actual size
	// for ops debugging.
	assert.Contains(t, err.Error(), "exceeds limit of")
}

func TestSerializeResponse_AtLimit_OK(t *testing.T) {
	t.Parallel()

	// A small response well under the cap — the success path baseline.
	_, err := idempotency.SerializeResponse(&graphql.Response{
		Data: json.RawMessage(`{"ok":true}`),
	}, 0)
	require.NoError(t, err)
}

// TestSerializeResponse_CustomLimit covers the PR-review finding that the
// cap must be configurable per service: a tiny custom limit rejects a body
// the default would accept, while a generous custom limit accepts a body the
// default would reject.
func TestSerializeResponse_CustomLimit(t *testing.T) {
	t.Parallel()

	small := &graphql.Response{Data: json.RawMessage(`{"ok":true}`)}

	// A 1-byte ceiling rejects even the tiny baseline response.
	_, err := idempotency.SerializeResponse(small, 1)
	require.ErrorIs(t, err, idempotency.ErrResponseTooLarge)

	// A ceiling above the default accepts a body that the default rejects.
	big := strings.Repeat("x", idempotency.DefaultMaxResponseBytes+1)
	payload, err := json.Marshal(map[string]string{"big": big})
	require.NoError(t, err)
	_, err = idempotency.SerializeResponse(&graphql.Response{Data: payload}, idempotency.DefaultMaxResponseBytes*4)
	require.NoError(t, err)
}
