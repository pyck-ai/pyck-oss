package std

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"math/rand"
	"time"
)

// Title upper-cases the first letter of every word.
//
// The Caser is built per call rather than kept in a package variable: cases.Title
// returns a stateful transformer that "should not be shared between goroutines",
// and a shared one corrupts its own buffer — a data race that surfaces as a slice
// bounds panic once two callers overlap.
func Title(s string) string {
	return cases.Title(language.English).String(s)
}

func GenerateRandomString(length int) string {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	b := make([]byte, length)
	for j := range b {
		b[j] = byte(rng.Intn(26) + 'A')
	}

	return string(b)
}
