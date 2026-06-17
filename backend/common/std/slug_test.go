package std_test

import (
	"testing"

	"github.com/pyck-ai/pyck/backend/common/std"
)

type caseType struct {
	input    string
	expected string
}

// slugTestCases covers both ASCII basics and the Unicode behavior added for
// issue #824. Two notable behavior pins to be aware of when reading:
//
//  1. gosimple/slug expands a few English symbols: "@" -> "at", "&" -> "and".
//     More informative than the legacy strip-everything behavior.
//  2. Non-ASCII letters are transliterated to ASCII via gosimple/unidecode
//     (Greek "ράφι" -> "raphi", German "ö" -> "o", French "é" -> "e", etc.).
//     Pre-#824 the regex `[^a-z0-9\\s-]` stripped them, collapsing whole
//     strings to empty.
//  3. Pure-emoji inputs have no transliteration; the fallback returns the
//     first 8 hex chars of sha256(input) so distinct inputs still produce
//     distinct slugs (preserves the (tenant_id, slug) unique-index contract).
var slugTestCases = []caseType{
	// --- ASCII basics ---
	{"", ""},
	{"Hello World", "hello-world"},
	{"  Hello  World! ", "hello-world"},
	{"--Hello --World--", "hello-world"},
	{"pyck is Awesome", "pyck-is-awesome"},
	{"  Multiple   Spaces  ", "multiple-spaces"},
	{"Special@#%Characters", "specialat-characters"},
	{"Trailing-and-leading--", "trailing-and-leading"},
	{"Numbers 123", "numbers-123"},
	{"123 Numbers", "123-numbers"},
	{"Mix3d C4se", "mix3d-c4se"},
	{" 123 456 ", "123-456"},
	{"Non-Alpha-Numeric!@#$%^&*()_+", "non-alpha-numeric-at-and"},
	{"Symbols *&^%$#@!~`", "symbols-and-at"},
	{"Punctuation.,:;?!", "punctuation"},
	{"Mixed -- Characters!!", "mixed-characters"},
	{"Combination of 123 and !@#", "combination-of-123-and-at"},

	// Underscores must be treated as separators (legacy contract that the
	// Shopify integration depends on — `shopify_item_v1` → `shopify-item-v1`).
	// gosimple/slug preserves underscores by default; we post-process to
	// match pyck's SlugRegex which disallows them.
	{"shopify_item_v1", "shopify-item-v1"},
	{"multiple___underscores", "multiple-underscores"},
	{"mixed_-_separators", "mixed-separators"},
	{"_leading_and_trailing_", "leading-and-trailing"},

	// --- Unicode (issue #824) ---
	// Greek — gosimple/unidecode uses classical transliteration: φ → ph, υ → u.
	{"ράφι ένα", "raphi-ena"},
	{"ράφι δύο", "raphi-duo"},
	// German diacritics — default transliteration strips the umlaut
	// (ö→o, ü→u, ß→ss). "oe/ue/ss" expansion needs MakeLang("de"); we don't
	// invoke language-specific transliteration because the resolver has no
	// language context at slug-creation time.
	{"Schöne Grüße", "schone-grusse"},
	{"Müller", "muller"},
	// French accents.
	{"café résumé", "cafe-resume"},
	// Cyrillic.
	{"стеллаж один", "stellazh-odin"},
	// Mixed Latin + Greek + ASCII numerals.
	{"Hello ράφι 123", "hello-raphi-123"},
	// Pure-emoji input. Pinned to sha256("🎉🎊")[:4] in hex to lock the
	// deterministic fallback used when transliteration would yield an empty
	// string.
	{"\U0001F389\U0001F38A", "965a4f77"},
}

// TestToSlug runs the full case table — both ASCII and Unicode.
func TestToSlug(t *testing.T) {
	t.Parallel()
	for _, tc := range slugTestCases {
		got := std.ToSlug(tc.input)
		if got != tc.expected {
			t.Errorf("ToSlug(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

// TestToSlug_Distinctness is the user-visible failure mode from #824: two
// distinct Greek-named DataTypes pre-fix collided on the (tenant_id, slug)
// unique index because both ToSlug results were "". After the fix they must
// differ.
func TestToSlug_Distinctness(t *testing.T) {
	t.Parallel()
	a := std.ToSlug("ράφι ένα")
	b := std.ToSlug("ράφι δύο")
	if a == "" || b == "" {
		t.Fatalf("ToSlug produced empty slug for Greek input: a=%q b=%q", a, b)
	}
	if a == b {
		t.Fatalf("ToSlug produced colliding slugs for distinct inputs: %q == %q", a, b)
	}
}

// TestToSlug_AlwaysValid guards the invariant that ToSlug output always
// passes IsValidSlug. The Ent field validator on slug fields uses the same
// ASCII regex, so this invariant must hold for any future change to ToSlug.
func TestToSlug_AlwaysValid(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"ράφι ένα",
		"Schöne Grüße",
		"café résumé",
		"стеллаж один",
		"\U0001F389\U0001F38A",
		"Hello World",
		"",
	}
	for _, in := range inputs {
		out := std.ToSlug(in)
		if out == "" {
			// Empty-in produces empty-out by design; skip.
			continue
		}
		if !std.IsValidSlug(out) {
			t.Errorf("ToSlug(%q) = %q which fails IsValidSlug", in, out)
		}
	}
}
