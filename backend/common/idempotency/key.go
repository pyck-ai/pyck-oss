package idempotency

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// FromHeaders returns the (trimmed) idempotency key value from the request
// headers, or "" if no header is present. A header value that is non-empty
// but exceeds [MaxKeyLen] returns ErrKeyTooLong. A header value that is
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

	return key, nil
}
