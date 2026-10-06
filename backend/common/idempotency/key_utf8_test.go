package idempotency_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

// invalidUTF8Keys are header values Go's HTTP server passes through (bytes
// 0x80-0xff) but Postgres cannot store in the idempotency_keys varchar column.
var invalidUTF8Keys = map[string]string{
	"lone high byte":     "probe-\xff",
	"truncated sequence": "key-\xc3",
	"encoded surrogate":  "\xed\xa0\x80",
	"overlong slash":     "a\xc0\xafb",
}

// TestFromHeaders_InvalidUTF8_Errors pins that a key the store cannot hold is
// refused here, not by the database.
func TestFromHeaders_InvalidUTF8_Errors(t *testing.T) {
	t.Parallel()

	for name, key := range invalidUTF8Keys {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := http.Header{}
			h.Set(idempotency.HeaderName, key)
			got, err := idempotency.FromHeaders(h)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "UTF-8")
			assert.Empty(t, got)
		})
	}
}

func TestFromHeaders_NonASCIIUTF8_OK(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set(idempotency.HeaderName, "bestellung-größe-✓")
	got, err := idempotency.FromHeaders(h)
	require.NoError(t, err)
	assert.Equal(t, "bestellung-größe-✓", got)
}

// TestPreCheck_InvalidUTF8Key_BadRequest pins the status a client gets: 400
// before any store access. Before, the store lookup failed in Postgres and
// the check answered 500.
func TestPreCheck_InvalidUTF8Key_BadRequest(t *testing.T) {
	t.Parallel()

	for name, key := range invalidUTF8Keys {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A store that fails like Postgres does on these bytes: if the key
			// reached it, the check would answer 500 instead of 400.
			store := &stubStore{lookupErr: errors.New(`pq: invalid byte sequence for encoding "UTF8"`)}
			got := idempotency.PreCheck(t.Context(), headerWithKey(key),
				mutationOpCtx(t, "X", []string{"x"}, nil), store, authYes(uuid.New(), uuid.New()))

			require.Equal(t, idempotency.ActionShortCircuit, got.Action)
			assert.Equal(t, http.StatusBadRequest, got.Status)
			require.NotNil(t, got.Response)
			require.Len(t, got.Response.Errors, 1)
			assert.Equal(t, idempotency.CodeInvalidKey, got.Response.Errors[0].Extensions["code"])
			assert.NotContains(t, got.Response.Errors[0].Message, key, "the invalid bytes must not be echoed")
		})
	}
}

// FuzzFromHeaders checks the invariant the store relies on: an accepted key
// is trimmed, within MaxKeyLen and valid UTF-8.
func FuzzFromHeaders(f *testing.F) {
	for _, seed := range []string{"abc", "  x  ", "probe-\xff", "key-\xc3", "größe", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		h := http.Header{}
		h[idempotency.HeaderName] = []string{raw}
		key, err := idempotency.FromHeaders(h)
		if err != nil || key == "" {
			return
		}
		if key != strings.TrimSpace(key) || len(key) > idempotency.MaxKeyLen || !utf8.ValidString(key) {
			t.Fatalf("FromHeaders(%q) accepted %q", raw, key)
		}
	})
}
