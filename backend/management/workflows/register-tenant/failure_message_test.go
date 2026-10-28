package registertenant_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// registerTenant answers anonymous callers, so a failed step is answered
// with a fixed text per error type only the caller's own input produces and
// a generic one for everything else. The answer is built from the type
// only: whatever the dependency said never enters it. The literals are the
// contract the resolver's answer and the end-to-end suites read.
func TestFailureMessage(t *testing.T) {
	t.Parallel()

	known := []struct{ errType, want string }{
		{"organization-name-taken", "organization-name-taken: an organization with this tenant name already exists"},
		{"organization-invalid", "organization-invalid: the tenant name was refused"},
		// A taken username is answered as a refused account, type and all:
		// an answer of its own would be an instance-wide username oracle.
		{"admin-username-taken", "admin-account-invalid: the admin account was refused"},
		{"admin-account-invalid", "admin-account-invalid: the admin account was refused"},
		{"org-metadata-invalid", "org-metadata-invalid: the tenant data was refused"},
	}
	for _, tc := range known {
		assert.Equal(t, tc.want, registertenant.FailureMessage(tc.errType), tc.errType)
	}

	// Every other type (a dependency fault, an operator-side refusal
	// (org-metadata-rejected: a missing grant, a Zitadel without the API, a
	// value this binary cannot render; admin-hash-unsupported: a Zitadel
	// that does not accept pyck's bcrypt hash), a defect, an untyped error,
	// a type that differs from a known one only in case or spacing) gets
	// the generic text and is not named in the answer.
	for _, errType := range []string{
		"", "worker-api-rejected", "tenant-sync-rejected", "organization-rejected",
		"temporal-namespace-rejected", "org-metadata-rejected", "admin-hash-unsupported",
		"Org-Metadata-Invalid", "org-metadata-invalid ",
		"*errors.errorString", "Organization-Name-Taken",
		" organization-name-taken", "organization-name-taken ", "org-rejected",
	} {
		assert.Equal(t, registertenant.FailureGeneric, registertenant.FailureMessage(errType), "%q", errType)
	}
	assert.Equal(t, "provisioning failed", registertenant.FailureGeneric)
}
