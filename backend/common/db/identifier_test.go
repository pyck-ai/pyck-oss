package db_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/db"
)

func TestQuoteIdentifier(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"inbounds", "inbound-items", "order-items", "_x", "A1_b-2"} {
		got, ok := db.QuoteIdentifier(name)
		assert.True(t, ok, name)
		assert.Equal(t, `"`+name+`"`, got)
	}

	for _, name := range []string{"", "1items", "-items", `a"b`, `items" WHERE 1=1 --`, "a b", "a;b", "a.b", "a'b", "a\x00b", "ä"} {
		_, ok := db.QuoteIdentifier(name)
		assert.False(t, ok, "%q", name)
	}
}

// FuzzQuoteIdentifier checks the invariant the SQL splice relies on: an accepted
// name comes back wrapped in exactly one pair of double quotes with nothing
// inside that could end the identifier or start an expression.
func FuzzQuoteIdentifier(f *testing.F) {
	for _, seed := range []string{"inbound-items", `a"b`, "a b", "", "x--y", "a\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got, ok := db.QuoteIdentifier(name)
		if !ok {
			return
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(got, `"`), `"`)
		if inner != name || len(got) != len(name)+2 {
			t.Fatalf("QuoteIdentifier(%q) = %q, want the name in one pair of quotes", name, got)
		}
		if strings.ContainsAny(name, "\"'`;. \t\r\n\x00") {
			t.Fatalf("QuoteIdentifier accepted %q", name)
		}
	})
}
