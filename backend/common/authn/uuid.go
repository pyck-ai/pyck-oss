package authn

import (
	"github.com/google/uuid"
)

// ComputeUUID generates a deterministic UUID v5 based on a namespace and value.
// It uses SHA-1 hashing to create a UUID that will always be the same for the
// same input parameters. This is useful for generating stable identifiers
// from existing data like organization IDs or user IDs.
//
// The function first creates a namespace UUID from the provided namespace string,
// then uses that namespace to generate the final UUID from the value string.
func ComputeUUID(ns string, s string) uuid.UUID {
	tokenNamespace := uuid.NewSHA1(uuid.NameSpaceOID, []byte(ns))
	return uuid.NewSHA1(tokenNamespace, []byte(s))
}
