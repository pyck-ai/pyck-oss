//go:build integration

// Package auth exercises the Zitadel-side machine user / PAT lifecycle
// as observed through pyck's auth path (management.tenants): mint a PAT
// and wait for it to become usable, add a second PAT and prove the two
// are independent, revoke one and prove only the survivor keeps working,
// then delete the machine user and prove its remaining token is rejected
// once Zitadel propagates the deletion.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/machine-user-lifecycle/...
package auth

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestMachineUser is the runner for the PAT lifecycle suite.
func TestMachineUser(t *testing.T) {
	suite.Run(t, new(MachineUserSuite))
}
