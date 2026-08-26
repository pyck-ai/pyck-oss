// Package sqljsonpath validates user-supplied JSON path expressions before they
// reach ent's sqljson.DotPath.
//
// On the Postgres dialect ent builds a JSON path predicate by concatenating
// each path segment into raw SQL wrapped in single quotes, without escaping the
// segment (entgo.io/ent/dialect/sql/sqljson emits `"'" + segment + "'"`). A
// segment containing a single quote therefore breaks out of the string literal
// and injects arbitrary SQL into the WHERE / ORDER BY clause — bypassing the
// bound `$n` value argument and the tenant-isolation filter (see issue #1382).
// The value argument is always safely bound; only the path is unsafe.
//
// Upgrading ent does not fix this: the sink itself does not escape, so the
// sanitisation must live in pyck code. Validate parses the path exactly as
// DotPath does (via sqljson.ParsePath) and rejects any segment outside the
// safe key alphabet, so a validated path can only ever produce the intended
// `data ->> 'key'` lookup.
//
// Callers do not invoke Validate directly. They use this package's DotPath,
// which validates and then builds the option, so the checked string is the
// only one that can be emitted; a forbidigo rule in .golangci.toml rejects the
// raw sqljson.DotPath everywhere outside this package. That makes the guard
// structural rather than a convention each call site has to remember — the
// earlier design paired a separate Validate call with a raw DotPath call and
// depended on review to keep the two arguments in agreement.
//
// The exception is generated code: `backend/*/ent/gen/gql_pagination_jsonb.go`
// still reaches the raw sink, and is fed only by the order-by choke point in
// backend/common/cmd/entc/templates/gql/jsonb_pagination.tmpl, which validates
// the path before it is stored on the pager.
//
// Validate also surfaces sqljson.ParsePath's own parse errors instead of
// swallowing them. Without this, ent's DotPath discards the error
// (`path, _ := ParsePath(dotpath)`), leaving PathOptions.Path empty; the
// predicate then silently falls through to comparing the entire `data`
// column instead of the intended key, returning wrong (usually empty)
// results with a 200 rather than an error. Requests that would have hit that
// silent-fallback path now fail loudly at Validate instead — this is a
// deliberate, user-visible behaviour change. Note that "", ".", and ".." do
// not trigger this: they parse successfully to zero segments and are
// accepted (see TestValidate_AcceptedHarmlessEdgeCases).
package sqljsonpath

import (
	"errors"
	"fmt"
	"regexp"
	"unicode"

	"entgo.io/ent/dialect/sql/sqljson"
)

// ErrInvalidJSONPath is returned when a JSON path (or one of its segments)
// contains characters that could alter the generated SQL.
var ErrInvalidJSONPath = errors.New("invalid JSON path")

// isKeySegment reports whether segment is safe to interpolate, unescaped,
// into the single-quoted SQL literal ent emits for an object-key segment
// (`"data"->>'segment'`). Free-form customer JSON keys in a German-language
// warehouse product routinely contain accented letters, punctuation, and
// spaces (e.g. "Artikel-Nr", "Größe", "weight (kg)"), so the alphabet here is
// deliberately much wider than backend/common/validator's sqlSafeRegexp
// (`^[a-zA-Z_][a-zA-Z0-9_]*$`), which gates SQL identifiers (table/column
// names) rather than JSON object keys and has no such requirement. A segment
// is safe iff it is non-empty and every rune is accepted by isSafeKeyRune.
func isKeySegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		if !isSafeKeyRune(r) {
			return false
		}
	}
	return true
}

// isSafeKeyRune classifies a single rune of an object-key segment. The reject
// cases are checked first because several of them (', ", [, ]) fall inside
// unicode.IsPunct and would otherwise be swallowed by the broad accept arm
// below.
//
//   - '\” breaks out of the single-quoted SQL literal ent emits; this is the
//     actual defect behind #1382 and is the one character that must never be
//     allowed regardless of any other rule.
//   - '\\' is inert while Postgres runs with standard_conforming_strings=on
//     (the default), but is rejected anyway as cheap defence in depth against
//     that setting being off.
//   - '"' is not part of ent's DotPath quoting, but ent's ParsePath has a
//     separate quoted-segment syntax (`a."b.c"`); a quote inside a segment
//     would be consumed as path structure rather than as a literal character,
//     producing a lookup that silently differs from what the caller meant.
//   - '[' and ']' would let a segment masquerade as an array index. Indices
//     are validated separately by arrayIndex (ASCII digits only) and are
//     emitted unquoted; ent's own index detection uses unicode.IsNumber, so a
//     non-ASCII digit such as U+0665 (ARABIC-INDIC DIGIT FIVE) could pass as a
//     key (bracket + IsNumber rune) yet be treated by ent as a bare, unquoted
//     index — escaping this validator's safe-shape guarantee entirely.
//     Excluding brackets from the key alphabet closes that gap.
//   - unicode.IsControl and the Cf (format/bidi/zero-width) category are
//     rejected as confusable characters with no legitimate use in a key.
//   - Only the plain ASCII space (U+0020) is accepted as whitespace; every
//     other Unicode space (e.g. NBSP U+00A0, ideographic space U+3000) is
//     rejected by falling through to the default case below.
func isSafeKeyRune(r rune) bool {
	switch r {
	case '\'', '\\', '"', '[', ']':
		return false
	}
	if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
		return false
	}
	if r == ' ' {
		return true
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// arrayIndex matches a numeric array-index segment such as "[0]". ParsePath
// returns these verbatim and pgTextPath emits them unquoted, so digits-only
// indices are safe.
var arrayIndex = regexp.MustCompile(`^\[[0-9]+\]$`)

// Validate returns nil when dotpath is safe to hand to sqljson.DotPath and
// ErrInvalidJSONPath otherwise. It parses dotpath with sqljson.ParsePath — the
// same parser DotPath uses — so validation sees exactly the segments that will
// be emitted into SQL, leaving no room for a parser-mismatch bypass.
func Validate(dotpath string) error {
	segments, err := sqljson.ParsePath(dotpath)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrInvalidJSONPath, dotpath, err)
	}

	for _, segment := range segments {
		if arrayIndex.MatchString(segment) {
			continue
		}
		if !isKeySegment(segment) {
			return fmt.Errorf("%w: segment %q in %q", ErrInvalidJSONPath, segment, dotpath)
		}
	}

	return nil
}

// DotPath validates dotpath and returns the ent path option built from it, so
// a client-supplied path can only reach sqljson.DotPath after passing
// Validate. Call sites must use this rather than sqljson.DotPath directly; the
// forbidigo rule in .golangci.toml forbids the raw call outside this package.
//
// Fusing validation and construction closes a bug class that two separate
// calls cannot: with Validate and sqljson.DotPath written side by side it was
// possible to validate one variable and interpolate another, leaving the guard
// looking present but inert. Here the only string that can be emitted is the
// one that was checked.
//
// The returned Option is nil when err is non-nil, so callers must propagate
// the error rather than use the option; errcheck enforces that the error is
// not silently dropped.
func DotPath(dotpath string) (sqljson.Option, error) {
	if err := Validate(dotpath); err != nil {
		return nil, err
	}

	return sqljson.DotPath(dotpath), nil
}
