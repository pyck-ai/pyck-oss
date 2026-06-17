package idempotency

import (
	"encoding/json"
	"fmt"

	"github.com/99designs/gqlgen/graphql"
)

// DefaultMaxResponseBytes is the fallback cap on the size of a serialized
// response we are willing to cache when a service does not configure its
// own ceiling. Mutations whose response would exceed the effective limit
// surface as [ErrResponseTooLarge] and roll back the surrounding
// transaction, because letting the idempotency_keys row grow unbounded
// would let a single noisy client fill the heap and outrun the 24h janitor.
//
// 1 MiB is a generous default — typical create mutations return a single
// entity (sub-kilobyte). A service with legitimately larger responses can
// raise its ceiling via PYCK_IDEMPOTENCY_MAX_RESPONSE_BYTES, which the
// gqltx middleware threads into [SerializeResponse] per request. The cap
// stays tied to response size because that is what directly drives row
// growth; exceeding it never degrades to a non-idempotent commit.
const DefaultMaxResponseBytes = 1 << 20

// SerializeResponse returns a stable JSON encoding of the GraphQL response
// that can be replayed verbatim on a subsequent request with the same
// idempotency key. maxBytes bounds the encoded body; a value <= 0 falls
// back to [DefaultMaxResponseBytes]. Returns [ErrResponseTooLarge] if the
// encoded body exceeds the effective limit.
func SerializeResponse(r *graphql.Response, maxBytes int) ([]byte, error) {
	if r == nil {
		return nil, ErrNilResponse
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(b) > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds limit of %d",
			ErrResponseTooLarge, len(b), maxBytes)
	}
	return b, nil
}

// DeserializeResponse reverses [SerializeResponse]. Returns an error if the
// stored payload is corrupted. The sole caller, resolveExisting, fails
// closed on a decode error — it surfaces a 500 [CodeCacheCorrupt] rather
// than re-executing the mutation, since silently re-running under load
// would violate the at-most-once guarantee.
func DeserializeResponse(b []byte) (*graphql.Response, error) {
	if len(b) == 0 {
		return nil, ErrEmptyResponse
	}
	var r graphql.Response
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("deserialize cached idempotency response: %w", err)
	}
	return &r, nil
}
