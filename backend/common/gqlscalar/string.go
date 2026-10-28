// Package gqlscalar overrides built-in gqlgen scalars with pyck-specific
// behavior.
//
// Every GraphQL String maps to NormalizedString, which applies Unicode NFC
// normalization on the way in and out so that exact-match predicates
// (NameEQ, NameContains, …) behave predictably regardless of whether a
// client emits NFC or NFD bytes (issue #824).
//
// Secrets are the exception: a field typed with a scalar mapped to RawString
// (the Password scalar of registerTenant) reaches the resolver as the exact
// bytes the client sent. A secret is never compared against stored text; it
// is hashed, or handed to a system that compares bytes. Zitadel verifies a
// password byte for byte, so a password normalized before hashing would
// lock its owner out whenever the client's login form sends the original
// decomposed bytes.
package gqlscalar

import (
	"github.com/99designs/gqlgen/graphql"
	"golang.org/x/text/unicode/norm"
)

// NormalizedString is an alias for string. It exists so gqlgen can map the
// GraphQL String scalar to a Go type whose marshal/unmarshal helpers apply
// NFC normalization. Because it's an alias (not a defined type), every
// resolver continues to receive a plain string with no conversions required.
type NormalizedString = string

// MarshalNormalizedString serializes a string into GraphQL after NFC
// normalization. Outbound values are normalized so clients receive a
// canonical form regardless of how they were stored.
func MarshalNormalizedString(s string) graphql.Marshaler { //nolint:ireturn // graphql.Marshaler is the required return type for gqlgen scalar marshalers.
	return graphql.MarshalString(norm.NFC.String(s))
}

// UnmarshalNormalizedString parses an inbound GraphQL String value and
// normalizes it to NFC. This guarantees that two visually identical strings
// (e.g. macOS-NFD vs Linux-NFC) compare equal under byte-exact predicates.
func UnmarshalNormalizedString(v any) (string, error) {
	s, err := graphql.UnmarshalString(v)
	if err != nil {
		return "", err
	}
	return norm.NFC.String(s), nil
}

// RawString is an alias for string for scalars whose value must keep the
// exact bytes the client sent. Unlike NormalizedString it applies no NFC
// normalization in either direction: it is meant for secrets (passwords),
// which are hashed or compared byte for byte by another system, where a
// normalized value is a different secret than the one the user typed.
type RawString = string

// MarshalRawString serializes a string into GraphQL unchanged.
func MarshalRawString(s string) graphql.Marshaler { //nolint:ireturn // graphql.Marshaler is the required return type for gqlgen scalar marshalers.
	return graphql.MarshalString(s)
}

// UnmarshalRawString parses an inbound GraphQL string value and returns it
// byte for byte as sent.
func UnmarshalRawString(v any) (string, error) {
	return graphql.UnmarshalString(v)
}
