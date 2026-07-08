package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// maxCreatedAtFn abstracts the DB call so checkQuiescence is testable without postgres.
type maxCreatedAtFn func(ctx context.Context) (time.Time, error)

// checkQuiescence reads max(created_at) twice with a settle delay.
// Returns an error if the value changed between reads (concurrent writes detected).
func checkQuiescence(ctx context.Context, fn maxCreatedAtFn, settle time.Duration, out io.Writer) error {
	t1, err := fn(ctx)
	if err != nil {
		return fmt.Errorf("quiescence check (read 1): %w", err)
	}
	fmt.Fprintf(out, "quiescence: first max(created_at) = %s; waiting %v…\n",
		t1.UTC().Format(time.RFC3339), settle)
	select {
	case <-time.After(settle):
	case <-ctx.Done():
		return ctx.Err()
	}
	t2, err := fn(ctx)
	if err != nil {
		return fmt.Errorf("quiescence check (read 2): %w", err)
	}
	if !t2.Equal(t1) {
		return fmt.Errorf(
			"concurrent writes detected: max(created_at) moved %s → %s; stop workers before retrying (stockfix workers stop)",
			t1.UTC().Format(time.RFC3339), t2.UTC().Format(time.RFC3339),
		)
	}
	fmt.Fprintln(out, "quiescence: stable ✓")
	return nil
}

// newUUID generates a random UUID v4.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// confirmInteractive asks the operator to re-type the tenant ID before mutating.
func confirmInteractive(tenantID string, out io.Writer, in io.Reader) error {
	fmt.Fprintf(out, "\nAbout to INSERT corrective rows for tenant %s.\n", tenantID)
	fmt.Fprint(out, "Type the tenant ID to confirm: ")
	scanner := bufio.NewScanner(in)
	scanner.Scan()
	typed := strings.TrimSpace(scanner.Text())
	if typed != tenantID {
		return fmt.Errorf("confirmation failed: got %q, expected %q", typed, tenantID)
	}
	return nil
}

// renderFixPlan writes the corrective row plan and verification queries to w.
func renderFixPlan(w io.Writer, rows []CorrectiveRow) {
	fmt.Fprintln(w, "=== FIX PLAN (movement_id=NULL marks manual correction) ===")
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO_ID\tITEM_ID\tNEW_VER\tCORRECTED_QTY\tOWN_QTY")
	fmt.Fprintln(tw, "-------\t-------\t-------\t-------------\t-------")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\n",
			r.RepoID, r.ItemID, r.Version, r.Quantity, r.OwnQuantity)
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Verification queries (run after fix):")
	for _, r := range rows {
		fmt.Fprintf(w, "  SELECT version, quantity, own_quantity FROM %s.stocks\n", "inventory")
		fmt.Fprintf(w, "    WHERE repository_id='%s' AND item_id='%s'\n", r.RepoID, r.ItemID)
		fmt.Fprintf(w, "    AND tenant_id='%s' ORDER BY version DESC LIMIT 3;\n\n", r.TenantID)
	}
	fmt.Fprintln(w, "IMPORTANT: Corrective rows have movement_id=NULL. A future RebuildStockTable")
	fmt.Fprintln(w, "replays the ledger without them; permanent fix requires the write-path hotfix.")
}

func cmdFix(args []string) int {
	fs := flag.NewFlagSet("fix", flag.ExitOnError)
	var g globalFlags
	addGlobal(fs, &g)
	var (
		execute        bool
		dryRunExplicit bool
		skipConfirm    bool
		settleS        string
		assumeQuiesced bool
		flyApp         string
		k8sDeploy      string
	)
	fs.BoolVar(&execute, "execute", false, "Perform mutations (default is dry-run)")
	fs.BoolVar(&dryRunExplicit, "dry-run", false, "Explicit dry-run no-op confirmer")
	fs.BoolVar(&skipConfirm, "yes", false, "Skip interactive tenant-ID confirmation prompt")
	fs.StringVar(&settleS, "settle", "20s", "Quiescence settle interval between two max(created_at) reads")
	fs.BoolVar(&assumeQuiesced, "assume-quiesced", false, "Skip quiescence check (dangerous — log loudly)")
	fs.StringVar(&flyApp, "fly-app", "", "Fly.io app name for automatic worker stop/start")
	fs.StringVar(&k8sDeploy, "k8s-deploy", "", "K8s namespace/deploy for automatic worker stop/start (e.g. prod/inventory-worker)")
	fs.Parse(args)

	g.dbURL = resolveDBURL(g.dbURL)
	if g.dbURL == "" {
		fmt.Fprintln(os.Stderr, "error: --db-url or PYCK_DATABASE_URL / PYCK_DATABASE_MASTER_URL required")
		return 1
	}
	if g.tenantID == "" {
		fmt.Fprintln(os.Stderr, "error: --tenant is required for fix")
		return 1
	}
	if err := validateSchema(g.schema); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	settle, err := time.ParseDuration(settleS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: --settle:", err)
		return 1
	}

	hasWorkerBackend := flyApp != "" || k8sDeploy != ""
	if execute && !assumeQuiesced && !hasWorkerBackend {
		fmt.Fprintln(os.Stderr, "error: --execute requires --fly-app/--k8s-deploy for automatic worker management, or --assume-quiesced to skip (dangerous)")
		return 1
	}

	ctx := context.Background()
	db, err := openDB(ctx, g.dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: connect:", err)
		return 1
	}
	defer db.Close()

	// Always run the read-only analysis (safe regardless of --execute).
	violations, err := rollupViolations(ctx, db, g.schema, g.tenantID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: rollup check:", err)
		return 1
	}
	var fixable []RollupViolation
	for _, v := range violations {
		if v.HasCurrentRow {
			fixable = append(fixable, v)
		} else {
			fmt.Fprintf(os.Stdout, "WARNING: %s/%s has no current stock row — cannot auto-fix, manual intervention required\n",
				v.RepoID, v.ItemID)
		}
	}
	if len(fixable) == 0 {
		printRollupTable(os.Stdout, violations)
		fmt.Fprintln(os.Stdout, "\nNo fixable violations found.")
		return 0
	}

	// Build corrective rows (requires max version per pair — one query each).
	corrective := make([]CorrectiveRow, 0, len(fixable))
	for _, v := range fixable {
		maxVer, err := maxVersion(ctx, db, g.schema, v.TenantID, v.RepoID, v.ItemID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: max version for %s/%s: %v\n", v.RepoID, v.ItemID, err)
			return 1
		}
		corrective = append(corrective, CorrectiveRow{
			ID:          newUUID(),
			TenantID:    v.TenantID,
			RepoID:      v.RepoID,
			ItemID:      v.ItemID,
			Version:     nextVersion(maxVer),
			Quantity:    v.ExpectedQty,
			OwnQuantity: v.OwnQty,
			Incoming:    v.IncomingQty,
			Outgoing:    v.OutgoingQty,
			OwnIncoming: v.OwnIncomingQty,
			OwnOutgoing: v.OwnOutgoingQty,
			CreatedBy:   v.CreatedBy,
		})
	}

	printRollupTable(os.Stdout, violations)
	fmt.Fprintln(os.Stdout)
	renderFixPlan(os.Stdout, corrective)

	if !execute {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "=== DRY RUN — use --execute to apply ===")
		if hasWorkerBackend {
			wb := workerBackend{flyApp: flyApp, k8sDeploy: k8sDeploy}
			fmt.Fprintln(os.Stdout, "\n[DRY RUN] Would stop workers:")
			wb.printStopCmds(os.Stdout)
			fmt.Fprintf(os.Stdout, "[DRY RUN] Would wait %v for quiescence\n", settle)
			fmt.Fprintf(os.Stdout, "[DRY RUN] Would insert %d corrective row(s)\n", len(corrective))
			fmt.Fprintln(os.Stdout, "[DRY RUN] Would start workers:")
			wb.printStartCmds(os.Stdout)
		}
		return 0
	}

	// ── execute path ────────────────────────────────────────────────────────

	wb := workerBackend{flyApp: flyApp, k8sDeploy: k8sDeploy}

	if hasWorkerBackend {
		fmt.Fprintln(os.Stdout, "\nStopping workers…")
		if err := wb.stop(ctx, false, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error: stop workers:", err)
			return 1
		}
	}

	if assumeQuiesced {
		fmt.Fprintln(os.Stdout, "WARNING: --assume-quiesced set — skipping quiescence check (dangerous)")
	} else {
		quiesceFn := func(ctx context.Context) (time.Time, error) {
			return maxCreatedAt(ctx, db, g.schema, g.tenantID)
		}
		if err := checkQuiescence(ctx, quiesceFn, settle, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			if hasWorkerBackend {
				fmt.Fprintln(os.Stderr, "Workers were stopped — restarting before aborting…")
				_ = wb.start(ctx, false, os.Stderr)
			}
			return 1
		}
	}

	if !skipConfirm {
		if err := confirmInteractive(g.tenantID, os.Stdout, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "aborted:", err)
			if hasWorkerBackend {
				fmt.Fprintln(os.Stderr, "Workers were stopped — restarting…")
				_ = wb.start(ctx, false, os.Stderr)
			}
			return 1
		}
	}

	// Build the touched key set for post-insert verification.
	touched := make(map[string]bool, len(corrective))
	for _, r := range corrective {
		touched[r.RepoID+"/"+r.ItemID] = true
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: begin tx:", err)
		return 1
	}

	for _, r := range corrective {
		if err := insertCorrectiveRow(ctx, tx, g.schema, r); err != nil {
			_ = tx.Rollback()
			fmt.Fprintf(os.Stderr, "error: insert %s/%s: %v\n", r.RepoID, r.ItemID, err)
			return 1
		}
	}

	// Verify inside the transaction before committing.
	remaining, err := verifyAfterFix(ctx, tx, g.schema, g.tenantID, touched)
	if err != nil {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stderr, "error: post-insert verify:", err)
		return 1
	}
	if len(remaining) > 0 {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stderr, "\nROLLBACK: invariant still violated after corrective rows:")
		printRollupTable(os.Stderr, remaining)
		fmt.Fprintln(os.Stderr, "No changes were committed. Investigate before retrying.")
		if hasWorkerBackend {
			fmt.Fprintln(os.Stderr, "Restarting workers…")
			_ = wb.start(ctx, false, os.Stderr)
		}
		return 3
	}

	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "error: commit:", err)
		return 1
	}

	fmt.Fprintf(os.Stdout, "\nCOMMITTED: %d corrective row(s) inserted successfully.\n", len(corrective))

	if hasWorkerBackend {
		fmt.Fprintln(os.Stdout, "Starting workers…")
		if err := wb.start(ctx, false, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error: start workers:", err)
			fmt.Fprintln(os.Stderr, "Fix succeeded but workers not restarted — run: stockfix workers start")
			return 1
		}
	}
	return 0
}
