package idempotency

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	// HeaderName is the canonical HTTP header used to convey an idempotency key.
	HeaderName = "Idempotency-Key"

	// MaxKeyLen is the maximum accepted length of an idempotency key, matching
	// the limit documented in issue #1123 and Stripe's convention.
	MaxKeyLen = 255
)

// ErrKeyTooLong is returned when the header value exceeds [MaxKeyLen] bytes.
var ErrKeyTooLong = errors.New("idempotency key exceeds 255 characters")

// ErrKeyNotUTF8 is returned when the header value is not valid UTF-8. Go's
// HTTP server passes bytes 0x80-0xff through, and the idempotency_keys
// varchar column cannot store them: Postgres refuses the statement, which the
// check would report as a server error.
var ErrKeyNotUTF8 = errors.New("idempotency key is not valid UTF-8")

// FromHeaders returns the (trimmed) idempotency key value from the request
// headers, or "" if no header is present. A header value that is non-empty
// but exceeds [MaxKeyLen] returns ErrKeyTooLong, and one that is not valid
// UTF-8 returns ErrKeyNotUTF8. A header value that is
// present but contains only whitespace is treated as absent ("" returned).
func FromHeaders(h http.Header) (string, error) {
	raw := h.Get(HeaderName)
	if raw == "" {
		return "", nil
	}

	key := strings.TrimSpace(raw)
	if key == "" {
		return "", nil
	}

	if len(key) > MaxKeyLen {
		return "", fmt.Errorf("%w (max %d, got %d)", ErrKeyTooLong, MaxKeyLen, len(key))
	}

	if !utf8.ValidString(key) {
		return "", ErrKeyNotUTF8
	}

	return key, nil
}
