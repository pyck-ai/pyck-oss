package std

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"math/rand"
	"time"
)

var caser = cases.Title(language.English)

func Title(s string) string {
	return caser.String(s)
}

func GenerateRandomString(length int) string {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	b := make([]byte, length)
	for j := range b {
		b[j] = byte(rng.Intn(26) + 'A')
	}

	return string(b)
}
