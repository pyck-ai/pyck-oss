//nolint:testpackage // computeDrift/driftResult are intentionally unexported; tests live alongside.
package tenantreconcile

import (
	"testing"

	"github.com/google/uuid"
)

// orgState is a test-only shorthand for placing an org in one of the
// three Zitadel states the drift logic distinguishes.
type orgState int

const (
	stateActive orgState = iota
	stateInactive
	stateDeleted // absent from both active and inactive sets
)

// orgSpec describes one org's Zitadel state and whether the DB row for it
// is soft-deleted.
type orgSpec struct {
	state    orgState
	disabled bool
}

// buildSets turns a per-org spec into the three inputs computeDrift
// consumes, plus the tenant_id assigned to each org so assertions can
// reference it.
func buildSets(t *testing.T, specs map[string]orgSpec) (
	disabledByOrg map[string]uuid.UUID,
	activeOrgIDs map[string]struct{},
	inactiveOrgIDs map[string]struct{},
	tenantIDByOrg map[string]uuid.UUID,
) {
	t.Helper()

	disabledByOrg = map[string]uuid.UUID{}
	activeOrgIDs = map[string]struct{}{}
	inactiveOrgIDs = map[string]struct{}{}
	tenantIDByOrg = map[string]uuid.UUID{}

	for orgID, spec := range specs {
		id := uuid.New()
		tenantIDByOrg[orgID] = id

		if spec.disabled {
			disabledByOrg[orgID] = id
		}
		switch spec.state {
		case stateActive:
			activeOrgIDs[orgID] = struct{}{}
		case stateInactive:
			inactiveOrgIDs[orgID] = struct{}{}
		case stateDeleted:
			// intentionally absent from both sets
		}
	}

	return disabledByOrg, activeOrgIDs, inactiveOrgIDs, tenantIDByOrg
}

func orgRefs(refs []TenantRef) map[string]uuid.UUID {
	out := make(map[string]uuid.UUID, len(refs))
	for _, r := range refs {
		out[r.IdpOrgRef] = r.TenantID
	}
	return out
}

// TestComputeDrift_Matrix exercises every cell of the decision matrix
// documented on ComputeDriftActivity (DB row state × Zitadel org state).
func TestComputeDrift_Matrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		org   string
		state orgState
		dbDis bool
		// expected bucket for this single org:
		wantDisable bool // appears in toDisable
		wantRestore bool // appears in restoreOrgIDs
		wantDeleted bool // appears in deletedDisabled
	}{
		{name: "disabled + ACTIVE → disable", org: "o1", state: stateActive, dbDis: true, wantDisable: true},
		{name: "disabled + INACTIVE → aligned", org: "o2", state: stateInactive, dbDis: true},
		{name: "disabled + DELETED → terminal, not drift", org: "o3", state: stateDeleted, dbDis: true, wantDeleted: true},
		{name: "active + ACTIVE → aligned", org: "o4", state: stateActive, dbDis: false},
		{name: "active + INACTIVE → restore", org: "o5", state: stateInactive, dbDis: false, wantRestore: true},
		{name: "active + DELETED → out of scope here", org: "o6", state: stateDeleted, dbDis: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			disabledByOrg, active, inactive, tenantIDByOrg := buildSets(t, map[string]orgSpec{
				tt.org: {state: tt.state, disabled: tt.dbDis},
			})

			res := computeDrift(disabledByOrg, active, inactive)

			gotDisable := orgRefs(res.toDisable)
			gotDeleted := orgRefs(res.deletedDisabled)
			gotRestore := map[string]struct{}{}
			for _, o := range res.restoreOrgIDs {
				gotRestore[o] = struct{}{}
			}

			if _, ok := gotDisable[tt.org]; ok != tt.wantDisable {
				t.Errorf("toDisable[%s] = %v, want %v", tt.org, ok, tt.wantDisable)
			}
			if _, ok := gotRestore[tt.org]; ok != tt.wantRestore {
				t.Errorf("restoreOrgIDs[%s] = %v, want %v", tt.org, ok, tt.wantRestore)
			}
			if _, ok := gotDeleted[tt.org]; ok != tt.wantDeleted {
				t.Errorf("deletedDisabled[%s] = %v, want %v", tt.org, ok, tt.wantDeleted)
			}

			// When disabled, the resolved tenant_id must match the DB row's.
			if tt.wantDisable {
				if gotDisable[tt.org] != tenantIDByOrg[tt.org] {
					t.Errorf("toDisable tenant_id = %s, want %s", gotDisable[tt.org], tenantIDByOrg[tt.org])
				}
			}
		})
	}
}

// TestComputeDrift_DeletedOrgNotDisabled is the regression guard for the
// original bug: a soft-deleted tenant whose Zitadel org was deleted must
// NOT be re-dispatched for disable (the org can never become INACTIVE, so
// it would loop forever). It belongs in deletedDisabled instead.
func TestComputeDrift_DeletedOrgNotDisabled(t *testing.T) {
	t.Parallel()

	disabledByOrg, active, inactive, ids := buildSets(t, map[string]orgSpec{
		"gone": {state: stateDeleted, disabled: true},
	})

	res := computeDrift(disabledByOrg, active, inactive)

	if len(res.toDisable) != 0 {
		t.Fatalf("toDisable = %+v, want empty (deleted org must not be re-disabled)", res.toDisable)
	}
	if len(res.restoreOrgIDs) != 0 {
		t.Fatalf("restoreOrgIDs = %+v, want empty", res.restoreOrgIDs)
	}
	if len(res.deletedDisabled) != 1 || res.deletedDisabled[0].IdpOrgRef != "gone" {
		t.Fatalf("deletedDisabled = %+v, want one ref for org %q", res.deletedDisabled, "gone")
	}
	if res.deletedDisabled[0].TenantID != ids["gone"] {
		t.Fatalf("deletedDisabled tenant_id = %s, want %s", res.deletedDisabled[0].TenantID, ids["gone"])
	}
}

// TestComputeDrift_Mixed verifies the buckets stay correct when every
// matrix cell is present at once — no cross-contamination between orgs.
func TestComputeDrift_Mixed(t *testing.T) {
	t.Parallel()

	disabledByOrg, active, inactive, _ := buildSets(t, map[string]orgSpec{
		"disable-me":  {state: stateActive, disabled: true},
		"aligned-off": {state: stateInactive, disabled: true},
		"deleted-off": {state: stateDeleted, disabled: true},
		"aligned-on":  {state: stateActive, disabled: false},
		"restore-me":  {state: stateInactive, disabled: false},
		"deleted-on":  {state: stateDeleted, disabled: false},
	})

	res := computeDrift(disabledByOrg, active, inactive)

	assertOrgs(t, "toDisable", orgKeys(orgRefs(res.toDisable)), []string{"disable-me"})
	assertOrgs(t, "restoreOrgIDs", res.restoreOrgIDs, []string{"restore-me"})
	assertOrgs(t, "deletedDisabled", orgKeys(orgRefs(res.deletedDisabled)), []string{"deleted-off"})
}

// TestComputeDrift_Empty: no DB rows and no orgs → no drift, no panics.
func TestComputeDrift_Empty(t *testing.T) {
	t.Parallel()

	res := computeDrift(map[string]uuid.UUID{}, map[string]struct{}{}, map[string]struct{}{})
	if len(res.toDisable) != 0 || len(res.restoreOrgIDs) != 0 || len(res.deletedDisabled) != 0 {
		t.Fatalf("expected empty drift, got %+v", res)
	}
}

func orgKeys(m map[string]uuid.UUID) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func assertOrgs(t *testing.T, label string, got, want []string) {
	t.Helper()
	gotSet := make(map[string]struct{}, len(got))
	for _, g := range got {
		gotSet[g] = struct{}{}
	}
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", label, got, want)
		return
	}
	for _, w := range want {
		if _, ok := gotSet[w]; !ok {
			t.Errorf("%s missing %q (got %v)", label, w, got)
		}
	}
}
