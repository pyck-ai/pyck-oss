// Package fixtures produces randomised inputs for integration suites. Every
// constructor call returns fresh values so re-runs against the same stack
// don't collide on names — even when two suites start within the same
// second.
package fixtures

import (
	"fmt"
	"strings"

	"github.com/brianvoe/gofakeit/v6"
)

// Tenant carries the inputs to the registerTenant mutation.
type Tenant struct {
	Name           string
	AdminUsername  string
	AdminEmail     string
	AdminFirstName string
	AdminLastName  string
	AdminPassword  string
}

// NewTenant returns a Tenant filled with random values. Re-runs and
// parallel suites are both unique because the nonce is an 8-byte
// random string (~10^12 collision space) rather than a unix-second
// timestamp.
func NewTenant() *Tenant {
	nonce := strings.ToLower(gofakeit.LetterN(8))
	name := fmt.Sprintf("integrations-test-%s", nonce)
	username := fmt.Sprintf("admin-%s-%s", strings.ToLower(gofakeit.Word()), nonce)
	return &Tenant{
		Name:           name,
		AdminUsername:  username,
		AdminEmail:     fmt.Sprintf("%s@%s.test", username, name),
		AdminFirstName: gofakeit.FirstName(),
		AdminLastName:  gofakeit.LastName(),
		// Zitadel default policy requires ≥8 chars with at least one
		// upper, lower, digit, and symbol. gofakeit.Password enables
		// those character sets but doesn't guarantee one of each lands
		// in the output, so we anchor a known-good prefix and append
		// random tail bytes for length and entropy.
		AdminPassword: "Aa1!" + gofakeit.Password(true, true, true, false, false, 12),
	}
}
