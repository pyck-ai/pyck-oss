package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── classifyDelta ─────────────────────────────────────────────────────────────

func TestClassifyDelta(t *testing.T) {
	cases := []struct {
		name    string
		dq      int64
		dinc    int64
		doutg   int64
		movKind string
		want    DeltaClass
	}{
		{"pure mark (quantity unchanged)", 0, 5, 0, "", DeltaLegal},
		{"pure mark zero deltas", 0, 0, 0, "", DeltaLegal},
		{"incoming execute", 10, -10, 0, "item", DeltaLegal},
		{"outgoing execute", -10, 0, -10, "item", DeltaLegal},
		{"bare repo entry (no reserves)", 5, 0, 0, "", DeltaLegal},
		{"bare repo exit (no reserves)", -5, 0, 0, "", DeltaLegal},
		{"combined exit+release flagged", -5, 0, -3, "", DeltaLegalFlagged},
		{"clobber: item mov with quantity change", -5, 5, 0, "item", DeltaClobber},
		{"anomaly: repo mov unexpected", -5, 5, 0, "repo", DeltaAnomaly},
		{"clobber: item mov mismatched incoming reserve", 10, -5, 0, "item", DeltaClobber},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDelta(tc.dq, tc.dinc, tc.doutg, tc.movKind)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDeltaClassString(t *testing.T) {
	assert.Equal(t, "LEGAL", DeltaLegal.String())
	assert.Equal(t, "LEGAL-FLAGGED", DeltaLegalFlagged.String())
	assert.Equal(t, "ANOMALY", DeltaAnomaly.String())
	assert.Equal(t, "CLOBBER", DeltaClobber.String())
}

// ── parseDuration ────────────────────────────────────────────────────────────

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"168h", 168 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"0d", 0, false},
		{"-1d", 0, true},
		{"invalid", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseDuration(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ── resolveDBURL ─────────────────────────────────────────────────────────────

func TestResolveDBURL_Flag(t *testing.T) {
	t.Setenv("PYCK_DATABASE_URL", "postgres://env-primary")
	t.Setenv("PYCK_DATABASE_MASTER_URL", "postgres://env-master")
	assert.Equal(t, "postgres://flag", resolveDBURL("postgres://flag"))
}

func TestResolveDBURL_PrimaryEnv(t *testing.T) {
	t.Setenv("PYCK_DATABASE_URL", "postgres://env-primary")
	t.Setenv("PYCK_DATABASE_MASTER_URL", "postgres://env-master")
	assert.Equal(t, "postgres://env-primary", resolveDBURL(""))
}

func TestResolveDBURL_FallbackMaster(t *testing.T) {
	t.Setenv("PYCK_DATABASE_URL", "")
	t.Setenv("PYCK_DATABASE_MASTER_URL", "postgres://env-master")
	assert.Equal(t, "postgres://env-master", resolveDBURL(""))
}

func TestResolveDBURL_AllEmpty(t *testing.T) {
	t.Setenv("PYCK_DATABASE_URL", "")
	t.Setenv("PYCK_DATABASE_MASTER_URL", "")
	assert.Equal(t, "", resolveDBURL(""))
}

// ── checkQuiescence ──────────────────────────────────────────────────────────

func TestCheckQuiescence_Stable(t *testing.T) {
	mark := quiescenceMark{Rows: 42, VersionS: 108}
	fn := func(_ context.Context) (quiescenceMark, error) { return mark, nil }
	err := checkQuiescence(context.Background(), fn, 0, io.Discard)
	require.NoError(t, err)
}

// A straggler pod with a lagging clock appends a row whose created_at is below
// the current max — the reason the guard fingerprints the append-only counters
// instead. Row count and version sum both move, so the write is caught.
func TestCheckQuiescence_MovingWithoutAdvancingClock(t *testing.T) {
	n := 0
	fn := func(_ context.Context) (quiescenceMark, error) {
		n++
		if n == 1 {
			return quiescenceMark{Rows: 42, VersionS: 108}, nil
		}
		return quiescenceMark{Rows: 43, VersionS: 111}, nil
	}
	err := checkQuiescence(context.Background(), fn, 0, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent writes")
}

// A row deleted and reinserted keeps the count level; the version sum still moves.
func TestCheckQuiescence_MovingVersionSumOnly(t *testing.T) {
	n := 0
	fn := func(_ context.Context) (quiescenceMark, error) {
		n++
		if n == 1 {
			return quiescenceMark{Rows: 42, VersionS: 108}, nil
		}
		return quiescenceMark{Rows: 42, VersionS: 109}, nil
	}
	err := checkQuiescence(context.Background(), fn, 0, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent writes")
}

func TestCheckQuiescence_ContextCancelled(t *testing.T) {
	fn := func(ctx context.Context) (quiescenceMark, error) {
		return quiescenceMark{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := checkQuiescence(ctx, fn, 0, io.Discard)
	require.Error(t, err)
}

// ── newUUID ───────────────────────────────────────────────────────────────────

func TestNewUUID_Format(t *testing.T) {
	u := newUUID()
	assert.Len(t, u, 36)
	parts := strings.Split(u, "-")
	require.Len(t, parts, 5)
	assert.Len(t, parts[0], 8)
	assert.Len(t, parts[1], 4)
	assert.Len(t, parts[2], 4)
	assert.Len(t, parts[3], 4)
	assert.Len(t, parts[4], 12)
	// version 4 nibble
	assert.Equal(t, "4", string(parts[2][0]))
}

func TestNewUUID_Unique(t *testing.T) {
	a, b := newUUID(), newUUID()
	assert.NotEqual(t, a, b)
}

// ── renderFixPlan ─────────────────────────────────────────────────────────────

func TestRenderFixPlan_NoRows(t *testing.T) {
	var sb strings.Builder
	renderFixPlan(&sb, "inventory", nil)
	assert.Contains(t, sb.String(), "FIX PLAN")
}

func TestRenderFixPlan_WithRows(t *testing.T) {
	rows := []CorrectiveRow{
		{
			ID: newUUID(), TenantID: "t1", RepoID: "r1", ItemID: "i1",
			Version: 5, Quantity: 100, OwnQuantity: 40,
		},
		{
			ID: newUUID(), TenantID: "t1", RepoID: "r2", ItemID: "i2",
			Version: 3, Quantity: 200, OwnQuantity: 0,
		},
	}
	var sb strings.Builder
	renderFixPlan(&sb, "inventory", rows)
	out := sb.String()
	assert.Contains(t, out, "r1")
	assert.Contains(t, out, "i2")
	assert.Contains(t, out, "movement_id=NULL")
	assert.Contains(t, out, "RebuildStockTable")
}

// ── RollupViolation.Discrepancy ──────────────────────────────────────────────

func TestRollupViolationDiscrepancy(t *testing.T) {
	v := RollupViolation{StoredQty: 10, ExpectedQty: 15}
	assert.Equal(t, int64(5), v.Discrepancy())

	v2 := RollupViolation{StoredQty: 15, ExpectedQty: 10}
	assert.Equal(t, int64(-5), v2.Discrepancy())
}

// ── worker command builders ───────────────────────────────────────────────────

func TestFlyStopCmd(t *testing.T) {
	got := flyStopCmd("myapp", "m123")
	assert.Equal(t, []string{"flyctl", "machine", "stop", "m123", "--app", "myapp"}, got)
}

func TestFlyStartCmd(t *testing.T) {
	got := flyStartCmd("myapp", "m123")
	assert.Equal(t, []string{"flyctl", "machine", "start", "m123", "--app", "myapp"}, got)
}

func TestK8sScaleCmd_Zero(t *testing.T) {
	got := k8sScaleCmd("prod", "inventory-worker", 0)
	assert.Equal(t, []string{"kubectl", "scale", "deploy/inventory-worker", "-n", "prod", "--replicas=0"}, got)
}

func TestK8sScaleCmd_Restore(t *testing.T) {
	got := k8sScaleCmd("prod", "inventory-worker", 3)
	assert.Equal(t, []string{"kubectl", "scale", "deploy/inventory-worker", "-n", "prod", "--replicas=3"}, got)
}

// ── splitK8sDeploy ────────────────────────────────────────────────────────────

func TestSplitK8sDeploy(t *testing.T) {
	ns, deploy := splitK8sDeploy("prod/inventory-worker")
	assert.Equal(t, "prod", ns)
	assert.Equal(t, "inventory-worker", deploy)
}

func TestSplitK8sDeploy_NoSlash(t *testing.T) {
	ns, deploy := splitK8sDeploy("inventory-worker")
	assert.Equal(t, "default", ns)
	assert.Equal(t, "inventory-worker", deploy)
}

// ── validateSchema ────────────────────────────────────────────────────────────

func TestValidateSchema_Valid(t *testing.T) {
	assert.NoError(t, validateSchema("inventory"))
	assert.NoError(t, validateSchema("inv_01"))
}

func TestValidateSchema_Invalid(t *testing.T) {
	assert.Error(t, validateSchema(""))
	assert.Error(t, validateSchema("inventory; DROP TABLE"))
	assert.Error(t, validateSchema("Inventory"))
}

// ── confirmInteractive ────────────────────────────────────────────────────────

func TestConfirmInteractive_Correct(t *testing.T) {
	tenantID := "550e8400-e29b-41d4-a716-446655440000"
	in := strings.NewReader(tenantID + "\n")
	err := confirmInteractive(tenantID, io.Discard, in)
	require.NoError(t, err)
}

func TestConfirmInteractive_Wrong(t *testing.T) {
	tenantID := "550e8400-e29b-41d4-a716-446655440000"
	in := strings.NewReader("wrong-id\n")
	err := confirmInteractive(tenantID, io.Discard, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation failed")
}

// ── printRollupTable / printAnomalyTable ─────────────────────────────────────

func TestPrintRollupTable_Empty(t *testing.T) {
	var sb strings.Builder
	printRollupTable(&sb, nil)
	assert.Contains(t, sb.String(), "no violations")
}

func TestPrintRollupTable_WithViolations(t *testing.T) {
	vs := []RollupViolation{
		{TenantID: "t1", RepoID: "r1", RepoName: "Shelf A", ItemID: "i1",
			StoredQty: 10, ExpectedQty: 15, HasCurrentRow: true},
	}
	var sb strings.Builder
	printRollupTable(&sb, vs)
	out := sb.String()
	assert.Contains(t, out, "Shelf A")
	assert.Contains(t, out, "+5")
}

func TestPrintAnomalyTable_Empty(t *testing.T) {
	var sb strings.Builder
	printAnomalyTable(&sb, nil)
	assert.Contains(t, sb.String(), "no anomalies")
}

func TestPrintAnomalyTable_WithClobber(t *testing.T) {
	now := time.Now()
	rows := []AnomalyRow{
		{Version: 7, PrevQty: 10, Qty: 5, DQ: -5, DINC: 5, DOUTG: 0,
			CreatedAt: now, MovementID: "mov-abc", MovKind: "item", Class: DeltaClobber},
	}
	var sb strings.Builder
	printAnomalyTable(&sb, rows)
	out := sb.String()
	assert.Contains(t, out, "CLOBBER")
	assert.Contains(t, out, "[!] CLOBBER DETECTED")
}

// ── printAnalysisJSON ─────────────────────────────────────────────────────────

func TestPrintAnalysisJSON(t *testing.T) {
	var sb strings.Builder
	printAnalysisJSON(&sb, nil, nil)
	out := sb.String()
	assert.Contains(t, out, `"rollup_violations"`)
	assert.Contains(t, out, `"ledger_anomalies"`)
	assert.Contains(t, out, "[]")
}

// ── readyToCorrect ────────────────────────────────────────────────────────────

// A parent whose child is also violated must wait: its expected quantity is the
// sum of its children's CURRENT quantities, so it can only be priced once the
// child carries its corrected value.
func TestReadyToCorrect_ParentWaitsForViolatingChild(t *testing.T) {
	const item = "item-1"
	parents := map[string]string{"child": "parent", "parent": ""}
	fixable := []RollupViolation{
		{RepoID: "parent", ItemID: item},
		{RepoID: "child", ItemID: item},
	}

	ready := readyToCorrect(fixable, parents)
	require.Len(t, ready, 1)
	assert.Equal(t, "child", ready[0].RepoID)
}

// Violations in different items never block each other: the invariant is stated
// per (repository, item).
func TestReadyToCorrect_OtherItemDoesNotBlock(t *testing.T) {
	parents := map[string]string{"child": "parent", "parent": ""}
	fixable := []RollupViolation{
		{RepoID: "parent", ItemID: "item-1"},
		{RepoID: "child", ItemID: "item-2"},
	}

	ready := readyToCorrect(fixable, parents)
	assert.Len(t, ready, 2)
}

func TestReadyToCorrect_IndependentPairsAllReady(t *testing.T) {
	parents := map[string]string{"a": "", "b": ""}
	fixable := []RollupViolation{
		{RepoID: "a", ItemID: "item-1"},
		{RepoID: "b", ItemID: "item-1"},
	}

	ready := readyToCorrect(fixable, parents)
	assert.Len(t, ready, 2)
}
