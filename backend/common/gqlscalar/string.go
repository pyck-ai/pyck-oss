// Package gqlscalar overrides built-in gqlgen scalars with pyck-specific
// behavior. The String override applies Unicode NFC normalization on the way
// in and out so that exact-match predicates (NameEQ, NameContains, …) behave
// predictably regardless of whether a client emits NFC or NFD bytes.
//
// See issue #824 for the motivating bug.
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
