package fixtures

import (
	"fmt"
	"strings"

	"github.com/brianvoe/gofakeit/v6"
)

// MachineUser carries the inputs to provisioning a Zitadel machine user
// in a tenant sub-org.
type MachineUser struct {
	Username string
}

// NewMachineUser returns a MachineUser with a random username. Uses an
// 8-byte random nonce so parallel suites don't collide on usernames.
func NewMachineUser() *MachineUser {
	nonce := strings.ToLower(gofakeit.LetterN(8))
	return &MachineUser{
		Username: fmt.Sprintf("machine-%s-%s", strings.ToLower(gofakeit.Word()), nonce),
	}
}
