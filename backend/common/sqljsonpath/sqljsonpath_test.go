package sqljsonpath_test

import (
	"regexp"
	"strings"
	"testing"
	"unicode"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/sqljsonpath"
)

// safeQuery is the ONLY shape ent may emit on Postgres for a path we accept:
// the "data" column, then a chain of -> / ->> operators each followed by a
// single-quoted segment containing no quote or backslash, or a bare numeric
// index, terminated by the bound value. The property this encodes is
// structural rather than an alphabet: no accepted segment can contain a
// character that lets it escape the single-quoted literal it is placed
// inside. Anything else — an unescaped quote reaching the literal, a
// mismatched operator, extra SQL — means an accepted path broke out of its
// string literal, i.e. a real injection. The fuzzer asserts every accepted
// path lands inside this shape.
var safeQuery = regexp.MustCompile(`^SELECT "id" FROM "items" WHERE "data"((->>|->)('[^'\\]*'|[0-9]+))* = \$1$`)

// emitPG returns the exact Postgres SQL ent generates when the given path is
// handed to sqljson.DotPath. All five real sinks (ValueEQ / HasKey / ValueIn /
// ValueContains / OrderValue) build the path portion through the same
// PathOptions.pgTextPath, so ValueEQ is a faithful stand-in for the whole
// path-injection surface.
func emitPG(path string) string {
	sel := sql.Dialect(dialect.Postgres).Select("id").From(sql.Table("items"))
	sel.Where(sqljson.ValueEQ("data", "v", sqljson.DotPath(path)))
	q, _ := sel.Query()
	return q
}

func TestValidate_AcceptsSafePaths(t *testing.T) {
	t.Parallel()

	safe := []string{
		// simple keys
		"type", "meta", "sum", "A", "_", "_x", "a1", "x9",
		"camelCase", "snake_case", "UPPER_CASE", "_internal",
		// nested object paths
		"meta.name", "meta.weight", "a.b", "a.b.c.d.e", "a1.b2.c3",
		// array indices
		"[0]", "[10]", "tags[0]", "a[0].b", "a[10][20]", "items[999].sku",
		// mixed nesting + index
		"a.b[2].c",
		// long key
		"a_very_long_but_perfectly_legitimate_json_key_name_1234567890",
		// non-leading dashes
		"a-b", "Artikel-Nr", "order-id", "meta.sub-key",
		// ASCII whitespace, punctuation, and symbols (tier-4 widening)
		"a b", "a;b", "a(b", "f()", "a=b", "a+b", "a*b", "a/b", "a,b", "a:b",
		"a|b", "a&b", "a@b", "a#b", "a$b", "a%b", "a<b", "a>b", "a!b", "a~b",
		"a^b",
		// leading/trailing ASCII space is PINNED as accepted: isSafeKeyRune
		// treats ' ' as a safe rune anywhere in the segment, not just
		// mid-key, so there is no positional special case.
		" a", "a ",
		// all-digit segments: ent emits these as a quoted key (`->>'9'`),
		// not a bare index (`->9`), because bare-index detection only
		// applies to segments ParsePath itself recognises as `[digits]`.
		// Verified empirically via emitPG. A leading digit is therefore
		// just an ordinary safe key, not a parser error.
		"1abc", "9",
		// "1.2" parses to TWO segments, "1" and "2", each all-digit and
		// each rendered as its own quoted key lookup.
		"1.2",
		// unicode letters, marks, and symbols
		"m\u00e9t\u00e9o", "\u65e5\u672c\u8a9e",
		"Gr\u00f6\u00dfe", "Ma\u00dfe", "weight (kg)", "Net Weight",
		"50% margin", "Preis EUR", "L x B x H", "part#",
		"\u0410\u0440\u0442\u0438\u043a\u0443\u043b",
		"e\u0301", // "e" + combining acute accent (NFD-decomposed)
	}

	for _, path := range safe {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, sqljsonpath.Validate(path))
			assert.Regexp(t, safeQuery, emitPG(path),
				"accepted path must produce only the safe lookup shape")
		})
	}
}

func TestValidate_RejectsInjectionAndUnsafeChars(t *testing.T) {
	t.Parallel()

	// Tier-4 accepts nearly all printable characters (see isSafeKeyRune in
	// sqljsonpath.go); what remains rejected is exactly what could let a
	// segment escape its single-quoted SQL literal, be reinterpreted as path
	// structure or an array index by ent's own parser, or masquerade as a
	// non-printable/confusable character with no legitimate use in a key.
	payloads := map[string]string{
		// --- quote break-outs (the actual defect, #1382) ---
		"single quote":            "x'",
		"leading quote":           "'x",
		"quote only":              "'",
		"doubled quote":           "x''",
		"quote mid-key":           "a'b",
		"value-eq breakout":       "x' = 'x' OR (SELECT true FROM pg_sleep(2)) OR 'z",
		"haskey breakout":         "x' IS NOT NULL OR (SELECT true FROM pg_sleep(2)) OR 'z",
		"tenant bypass":           "x' OR '1'='1",
		"issue pg_sleep":          "x' OR (SELECT 1 FROM pg_sleep(5)) IS NOT NULL OR 'a'='a",
		"quote in nested segment": "meta.name'--",
		"stacked-ish":             "x'; DROP TABLE items;--",
		"comment":                 "x'--",
		"double quote":            `x" OR "1"="1`,
		"quoted segment":          `"abc"`,
		// --- backslash: defence in depth for standard_conforming_strings=off ---
		"backslash": `a\b`,
		// --- control characters ---
		"tab":              "a\tb",
		"newline":          "a\nb",
		"trailing newline": "a\n",
		"carriage return":  "a\rb",
		"raw backspace":    "a\bb",
		"null byte":        "a\x00b",
		"vertical tab":     "a\x0bb",
		"form feed":        "a\x0cb",
		// --- Cf (format/bidi/zero-width) characters ---
		"zero-width space": "a\u200bb",
		// --- non-ASCII whitespace: only the plain ASCII space is accepted ---
		"nbsp":              "a\u00a0b",
		"ideographic space": "a\u3000b",
		// --- array-index shapes ent's parser would otherwise treat
		//     specially, or that could confuse index vs. key detection ---
		"unicode digit idx": "[\u0665]",
		"hash index":        "[#0]",
		"negative index":    "[-1]",
		"fractional index":  "[1.5]",
		"alpha index":       "[a]",
		"hex index":         "[0x1]",
	}

	for name, path := range payloads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := sqljsonpath.Validate(path)
			require.Error(t, err, "must be rejected: %q", path)
			assert.ErrorIs(t, err, sqljsonpath.ErrInvalidJSONPath)
		})
	}
}

// TestValidate_AcceptedHarmlessEdgeCases pins the behaviour of odd-but-safe
// inputs. "", ".", and ".." collapse to zero segments, which ent renders as a
// bare `"data" = $1` lookup. "a.", ".a", and "a..b" instead parse to real
// segments with empty components dropped ("a.", ".a" -> ["a"]; "a..b" ->
// ["a", "b"]), rendering as `"data"->>'a' = $1` and
// `"data"->>'a'->>'b' = $1` respectively — safeQuery matches both shapes. In
// every case no injection is possible, so these are accepted rather than
// errored. This documents intent so a future change that starts rejecting
// them is a conscious one.
func TestValidate_AcceptedHarmlessEdgeCases(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"", ".", "..", "a.", ".a", "a..b"} {
		t.Run("path="+path, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, sqljsonpath.Validate(path))
			assert.Regexp(t, safeQuery, emitPG(path),
				"even edge-case accepts must emit only the safe shape")
		})
	}
}

// FuzzValidate is the soundness net. The security invariant is one-directional
// and absolute: for ANY input, if Validate accepts it, ent MUST emit only the
// safe `data -> 'key'` lookup shape. If the fuzzer ever finds an input that is
// accepted yet produces SQL outside safeQuery, that is a validation bypass —
// exactly the class of bug in #1382. Rejected inputs never reach the sink, so
// their SQL is not constrained.
//
// Run continuously with:
//
//	go test ./sqljsonpath/ -run x -fuzz FuzzValidate
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzValidate(f *testing.F) {
	seeds := []string{
		// safe
		"type", "meta.name", "a.b.c", "tags[0]", "a.b[2].c", "_x9", "[0]", "",
		"Artikel-Nr", "a b", "f()", "9", "1abc", "1.2",
		"m\u00e9t\u00e9o", "Gr\u00f6\u00dfe", "weight (kg)", "part#",
		"\u0410\u0440\u0442\u0438\u043a\u0443\u043b", "e\u0301",
		// break-outs & metacharacters — the fuzzer mutates from these
		"x'", "x' OR '1'='1", "x'--", `x"`, "a;b", `a\b`,
		"a\x00b", "a\bb", "a\nb", "[#0]", "[\u0665]",
		"a\u00a0b", "a\u200bb",
		"x' = 'x' OR (SELECT true FROM pg_sleep(2)) OR 'z",
		"x')::jsonb @> ('\"x\"')::jsonb OR (SELECT true FROM pg_sleep(2)) OR ('{}",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, path string) {
		// 1. Validate must never panic on any input.
		err := sqljsonpath.Validate(path)
		if err != nil {
			return // rejected inputs never reach the sink
		}

		// 2. Accepted ⟹ ent emits ONLY the safe lookup shape.
		got := emitPG(path)
		if !safeQuery.MatchString(got) {
			t.Fatalf("BYPASS: Validate accepted %q but ent emitted unsafe SQL:\n%s", path, got)
		}

		// 3. Accepted ⟹ every parsed segment is genuinely a bare key or index
		//    (independent cross-check against ent's own parser output).
		segments, perr := sqljson.ParsePath(path)
		require.NoError(t, perr, "accepted path must parse")
		for _, seg := range segments {
			if !isBareKey(seg) && !isBareIndex(seg) {
				t.Fatalf("BYPASS: accepted %q has unsafe segment %q", path, seg)
			}
		}
	})
}

// isBareKey is an independent cross-check of isSafeKeyRune in sqljsonpath.go,
// deliberately written in a different style (accept-first, using
// strings.ContainsRune for the excluded set instead of a rune switch) so that
// a bug shared by both implementations is unlikely, rather than a copy of the
// code under test.
func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	const excluded = `'\"[]`
	for _, r := range s {
		accepted := r == ' ' ||
			unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) ||
			unicode.IsPunct(r) || unicode.IsSymbol(r)
		if !accepted {
			return false
		}
		if strings.ContainsRune(excluded, r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func isBareIndex(s string) bool {
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' {
		return false
	}
	for _, r := range s[1 : len(s)-1] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
