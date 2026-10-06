package db

import "regexp"

// safeIdentifier is the shape a table or column name must have before it can be
// spliced into raw SQL: every character in it means the same thing to Go and to
// Postgres, so the name round-trips as the identifier it names rather than
// closing a quote and starting an expression.
var safeIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// quotableIdentifier is safeIdentifier plus '-', the shape of the hyphenated
// table names some ent schemas declare ("inbound-items"). A hyphen is only an
// identifier character inside double quotes, so these names may reach SQL
// through QuoteIdentifier alone.
var quotableIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)

// IsSafeIdentifier reports whether name may be interpolated into SQL as an
// identifier. It is the single gate for that decision: a second copy of the rule
// is a second place for it to drift.
func IsSafeIdentifier(name string) bool {
	return safeIdentifier.MatchString(name)
}

// QuoteIdentifier returns name as a double-quoted SQL identifier, or false if
// name is not shaped like a table or column name. Double quotes are standard
// SQL identifier quoting and mean the same in Postgres and SQLite; the pattern
// excludes the quote character, so the result cannot close the quote early.
func QuoteIdentifier(name string) (string, bool) {
	if !quotableIdentifier.MatchString(name) {
		return "", false
	}
	return `"` + name + `"`, true
}
