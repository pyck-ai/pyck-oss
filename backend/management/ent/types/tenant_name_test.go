package types_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/management/ent/types"
)

// TenantNameKey is the fold Zitadel applies to an org name for its
// uniqueness constraint (strings.ToLower in its eventstore). Two names that
// fold to the same key are one organization to Zitadel, so they must be
// one key here; the circled letters are the case Postgres' libc lower()
// leaves alone and the reason the key is computed in Go.
func TestTenantNameKey_FoldsTheWayZitadelFoldsOrgNames(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, want string }{
		{"Acme", "acme"},
		{"ACME", "acme"},
		{"aCmE", "acme"},
		{"Ärzte GmbH", "ärzte gmbh"},
		{"Ⓐcme", "ⓐcme"},         // CIRCLED LATIN CAPITAL LETTER A → small
		{"ⓐcme", "ⓐcme"},         // already folded
		{"İstanbul", "istanbul"}, // simple lowercase mapping of dotted capital I, as Zitadel's Go does it
		{"Straße", "straße"},     // ß has no simple lowercase change
		{"ΣΑΣ", "σασ"},
		{"  Acme  ", "  acme  "}, // not trimmed: registerTenant refuses surrounding whitespace
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, types.TenantNameKey(tc.name))
		})
	}
}

// Pairs Postgres' lower() under a libc locale treats as different names,
// while Zitadel's Go fold makes them one org: they must share a key.
func TestTenantNameKey_CollidesWherePostgresLowerDoesNot(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"Ⓐcme-x", "ⓐcme-x"},
		{"ⓉⒺⓈⓉ", "ⓣⓔⓢⓣ"},
	} {
		assert.Equal(t, types.TenantNameKey(pair[0]), types.TenantNameKey(pair[1]), "%q and %q are one org to Zitadel", pair[0], pair[1])
	}
}

// The key is a per-rune fold: folding twice changes nothing, and the key of
// a concatenation is the concatenation of the keys. The operator rename in
// docs/tenant-registration.md relies on the second property: appending a
// suffix to name and to name_key keeps the row's key consistent with its
// name.
func TestTenantNameKey_IsIdempotentAndCompositional(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Acme", "Ⓐcme", "Ärzte GmbH", "İstanbul", "MiXeD-123"} {
		key := types.TenantNameKey(name)
		assert.Equal(t, key, types.TenantNameKey(key), "folding %q twice must not change it", name)
		suffix := "-retired-0199C1D2"
		assert.Equal(t, key+types.TenantNameKey(suffix), types.TenantNameKey(name+suffix))
	}
}

// The key is exactly strings.ToLower over the name, rune for rune, for
// every code point: anything else would disagree with Zitadel somewhere.
func TestTenantNameKey_IsStringsToLowerOverEveryCodePoint(t *testing.T) {
	t.Parallel()

	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		s := string(r)
		if got, want := types.TenantNameKey(s), strings.ToLower(s); got != want {
			t.Fatalf("TenantNameKey(%U) = %q, strings.ToLower = %q", r, got, want)
		}
	}
}
