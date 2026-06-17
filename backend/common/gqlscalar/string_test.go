package gqlscalar_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/gqlscalar"
)

// These tests are the unit-level proof for issue #824: every GraphQL String
// input is NFC-normalized at the resolver boundary so byte-exact SQL
// predicates (NameEQ, NameContains) behave the same regardless of whether the
// client emits NFC (Linux/Windows default) or NFD (macOS filesystem default).

// nfc / nfd both render as "café" but are distinct byte sequences:
//
//	NFC: c a f é              — 5 bytes
//	NFD: c a f e + U+0301          — 6 bytes
const (
	nfc = "café"
	nfd = "café"
)

// TestNFCvsNFD_BytesDiffer is the load-bearing premise of the bug: two
// strings that LOOK identical have different byte sequences, which is why
// PostgreSQL `name = $1` silently misses without normalization at the
// boundary.
func TestNFCvsNFD_BytesDiffer(t *testing.T) {
	t.Parallel()
	if nfc == nfd {
		t.Fatal("expected NFC and NFD to differ at byte level")
	}
	if len(nfc) != 5 {
		t.Fatalf("expected NFC=5 bytes, got %d", len(nfc))
	}
	if len(nfd) != 6 {
		t.Fatalf("expected NFD=6 bytes, got %d", len(nfd))
	}
}

// TestUnmarshalNormalizedString_NFD_BecomesNFC is the headline assertion:
// inbound NFD bytes are converted to NFC before any resolver sees them.
func TestUnmarshalNormalizedString_NFD_BecomesNFC(t *testing.T) {
	t.Parallel()
	out, err := gqlscalar.UnmarshalNormalizedString(nfd)
	if err != nil {
		t.Fatal(err)
	}
	if out != nfc {
		t.Errorf("UnmarshalNormalizedString(NFD %q, %d bytes) = %q (%d bytes); want NFC %q (%d bytes)",
			nfd, len(nfd), out, len(out), nfc, len(nfc))
	}
}

// TestUnmarshalNormalizedString_NFC_Identity proves NFC clients pay no cost
// and see no surprise — their bytes pass through unchanged.
func TestUnmarshalNormalizedString_NFC_Identity(t *testing.T) {
	t.Parallel()
	out, err := gqlscalar.UnmarshalNormalizedString(nfc)
	if err != nil {
		t.Fatal(err)
	}
	if out != nfc {
		t.Errorf("NFC roundtrip changed bytes: in=%q out=%q", nfc, out)
	}
}

// TestMarshalNormalizedString_NFD_BecomesNFC proves outbound responses are
// normalized too, so any client that stored NFD bytes (e.g. via a path that
// bypasses the unmarshal hook) still receives canonical NFC.
func TestMarshalNormalizedString_NFD_BecomesNFC(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	gqlscalar.MarshalNormalizedString(nfd).MarshalGQL(&buf)
	// graphql.MarshalString quotes the value; strip the surrounding quotes.
	got := strings.Trim(buf.String(), `"`)
	if got != nfc {
		t.Errorf("MarshalNormalizedString(NFD) = %q; want NFC %q", got, nfc)
	}
}

// TestUnmarshalNormalizedString_GermanUmlauts is a sanity check on a real
// pyck case (umlauts roundtrip unchanged because they're already NFC in
// most inputs).
func TestUnmarshalNormalizedString_GermanUmlauts(t *testing.T) {
	t.Parallel()
	in := "Schöne Grüße" // NFC by codepoint
	out, err := gqlscalar.UnmarshalNormalizedString(in)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("German Umlaut NFC input changed: in=%q out=%q", in, out)
	}
}

// TestUnmarshalNormalizedString_Empty pins the contract that empty input
// passes through unchanged. Belt + braces on top of graphql.UnmarshalString.
func TestUnmarshalNormalizedString_Empty(t *testing.T) {
	t.Parallel()
	out, err := gqlscalar.UnmarshalNormalizedString("")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("UnmarshalNormalizedString(\"\") = %q; want empty", out)
	}
}

// TestUnmarshalNormalizedString_Nil pins the contract that a nil JSON value
// becomes an empty string. gqlgen sends nil for null GraphQL fields, so this
// path is exercised on every nullable String argument.
func TestUnmarshalNormalizedString_Nil(t *testing.T) {
	t.Parallel()
	out, err := gqlscalar.UnmarshalNormalizedString(nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("UnmarshalNormalizedString(nil) = %q; want empty", out)
	}
}

// TestUnmarshalNormalizedString_NumericCoercion exercises the upstream
// coercion path: GraphQL allows passing a number where a String is expected,
// and gqlgen converts to its decimal representation. The NFC pass over an
// ASCII string is a no-op so the coerced value flows through unchanged.
func TestUnmarshalNormalizedString_NumericCoercion(t *testing.T) {
	t.Parallel()
	out, err := gqlscalar.UnmarshalNormalizedString(42)
	if err != nil {
		t.Fatal(err)
	}
	if out != "42" {
		t.Errorf("UnmarshalNormalizedString(42) = %q; want \"42\"", out)
	}
}

// TestUnmarshalNormalizedString_InvalidType confirms upstream errors are
// propagated unmodified (no extra wrapping that could break errors.Is).
func TestUnmarshalNormalizedString_InvalidType(t *testing.T) {
	t.Parallel()
	if _, err := gqlscalar.UnmarshalNormalizedString(struct{}{}); err == nil {
		t.Error("expected error for invalid type, got nil")
	}
}

// TestMarshalNormalizedString_Empty pins the contract that empty input
// serializes as the empty JSON string `""`, not null.
func TestMarshalNormalizedString_Empty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	gqlscalar.MarshalNormalizedString("").MarshalGQL(&buf)
	if got := buf.String(); got != `""` {
		t.Errorf("MarshalNormalizedString(\"\") = %q; want %q", got, `""`)
	}
}

// TestGreekFinalSigma documents an edge case that this fix intentionally
// does NOT solve: Greek Σ uppercases to σ at non-final positions
// and ς at final position, but Go's strings.EqualFold and
// PostgreSQL's default lower() always pick σ. NFC normalization is
// the wrong layer for that — solving it requires ICU collation in
// PostgreSQL, which is out of scope for #824. Kept here so a future reader
// doesn't expect the gqlscalar override to handle case-folding edge cases.
func TestGreekFinalSigma(t *testing.T) {
	t.Parallel()
	stored := "Σίγμας" // Σίγμας — final ς
	upperQ := "ΣΙΓΜΑΣ" // ΣΙΓΜΑΣ — both Σ
	lowerQ := "σίγμας" // σίγμας — final ς

	// NFC normalization preserves codepoints; it does not fold case.
	for _, s := range []string{stored, upperQ, lowerQ} {
		out, _ := gqlscalar.UnmarshalNormalizedString(s)
		if out != s {
			t.Errorf("NFC unexpectedly altered: %q -> %q", s, out)
		}
	}
	// Lowercase-to-stored matches under EqualFold (both have final ς).
	if !strings.EqualFold(stored, lowerQ) {
		t.Error("EqualFold should match Σίγμας vs σίγμας")
	}
	// Uppercase-to-stored MISSES under EqualFold — the Σ vs ς problem.
	if strings.EqualFold(stored, upperQ) {
		t.Log("unexpected: stdlib resolved Σ→ς contextually; this would be an improvement")
	} else {
		t.Log("expected: stdlib folds Σ→σ uniformly, never ς; ΣΙΓΜΑΣ misses Σίγμας — out of scope for NFC fix")
	}
}
