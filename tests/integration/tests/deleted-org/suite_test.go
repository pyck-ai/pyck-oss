//go:build integration

// Package deletedorg covers the "Zitadel org no longer exists" paths the
// tenant disable/restore/reconcile and zitadel-sync workflows must tolerate.
// It reproduces the production bug where an org-cleanup job removed orgs out
// of band while pyck still held tenant rows, causing
// DeactivateZitadelOrgActivity to fail NotFound and the reconcile sweeper to
// re-dispatch the disable workflow forever.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/deleted-org/...
package deletedorg

import (
	"testing"

	"github.com/stretchr/testify/suite"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// DeletedOrgSuite uses the shared Base (config + dialed Zitadel conn). Each
// Test* method provisions its own tenant so the cases stay independent.
type DeletedOrgSuite struct {
	tests.Base

	// Temporal is a workflow client on the management ("default") namespace,
	// dialed once for the whole suite. Unlike the management worker (which
	// reaches Temporal via the internal-frontend and is exempt from the claim
	// mapper), an external client hits the authorizing frontend, so it
	// presents the system service token; that maps to a system user with
	// global Reader/Writer, satisfying the visibility (ListWorkflow) reads
	// the tests perform.
	Temporal temporalclient.Client
}

func (s *DeletedOrgSuite) SetupSuite() {
	s.Base.SetupSuite()

	tc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, "default", s.Cfg.ServiceToken)
	s.Require().NoError(err, "dial temporal default namespace")
	s.Temporal = tc
}

func (s *DeletedOrgSuite) TearDownSuite() {
	if s.Temporal != nil {
		s.Temporal.Close()
	}
	s.Base.TearDownSuite()
}

// TestDeletedOrg is the runner; testify runs the suite's Test* methods in
// alphabetical order.
func TestDeletedOrg(t *testing.T) {
	suite.Run(t, new(DeletedOrgSuite))
}
