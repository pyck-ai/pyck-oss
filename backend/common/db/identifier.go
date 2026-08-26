package db

import "regexp"

// safeIdentifier is the shape a table or column name must have before it can be
// spliced into raw SQL: every character in it means the same thing to Go and to
// Postgres, so the name round-trips as the identifier it names rather than
// closing a quote and starting an expression.
var safeIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// IsSafeIdentifier reports whether name may be interpolated into SQL as an
// identifier. It is the single gate for that decision: a second copy of the rule
// is a second place for it to drift.
func IsSafeIdentifier(name string) bool {
	return safeIdentifier.MatchString(name)
}
