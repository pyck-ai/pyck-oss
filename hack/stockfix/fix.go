package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// quiescenceFn abstracts the DB call so checkQuiescence is testable without postgres.
type quiescenceFn func(ctx context.Context) (quiescenceMark, error)

// checkQuiescence reads the append-only fingerprint twice with a settle delay.
// Returns an error if it changed between reads (concurrent writes detected).
func checkQuiescence(ctx context.Context, fn quiescenceFn, settle time.Duration, out io.Writer) error {
	first, err := fn(ctx)
	if err != nil {
		return fmt.Errorf("quiescence check (read 1): %w", err)
	}
	fmt.Fprintf(out, "quiescence: first read %s; waiting %v…\n", first, settle)
	select {
	case <-time.After(settle):
	case <-ctx.Done():
		return ctx.Err()
	}
	second, err := fn(ctx)
	if err != nil {
		return fmt.Errorf("quiescence check (read 2): %w", err)
	}
	if second != first {
		return fmt.Errorf(
			"concurrent writes detected: %s → %s; stop workers before retrying (stockfix workers stop)",
			first, second,
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
// Rows come out in repair order: children before the parents whose sums they feed.
func renderFixPlan(w io.Writer, schema string, rows []CorrectiveRow) {
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
		fmt.Fprintf(w, "  SELECT version, quantity, own_quantity FROM %s.stocks\n", schema)
		fmt.Fprintf(w, "    WHERE repository_id='%s' AND item_id='%s'\n", r.RepoID, r.ItemID)
		fmt.Fprintf(w, "    AND tenant_id='%s' ORDER BY version DESC LIMIT 3;\n\n", r.TenantID)
	}
	fmt.Fprintln(w, "IMPORTANT: Corrective rows have movement_id=NULL. A future RebuildStockTable")
	fmt.Fprintln(w, "replays the ledger without them; permanent fix requires the write-path hotfix.")
}

func cmdFix(args []string) (code int) {
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
	fs.StringVar(&settleS, "settle", "20s", "Settle interval between the two quiescence reads")
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
	// An operator who adds --dry-run to a line that already carries --execute
	// means "do not write"; letting one flag win would write anyway.
	if dryRunExplicit && execute {
		fmt.Fprintln(os.Stderr, "error: --dry-run and --execute are mutually exclusive")
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

	if !execute {
		// The repair itself, rolled back: the plan is then the exact rows
		// --execute would insert, parents already priced off their children.
		plan, perr := previewRepair(ctx, db, g.schema, g.tenantID)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "error:", perr)
			return 1
		}
		printRollupTable(os.Stdout, plan.Initial)
		fmt.Fprintln(os.Stdout)
		if len(plan.Corrective) == 0 {
			fmt.Fprintln(os.Stdout, "No fixable violations found.")
			return 0
		}
		renderFixPlan(os.Stdout, g.schema, plan.Corrective)

		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "=== DRY RUN — use --execute to apply ===")
		if hasWorkerBackend {
			wb := workerBackend{flyApp: flyApp, k8sDeploy: k8sDeploy}
			fmt.Fprintln(os.Stdout, "\n[DRY RUN] Would stop workers:")
			wb.printStopCmds(os.Stdout)
			fmt.Fprintf(os.Stdout, "[DRY RUN] Would wait %v for quiescence\n", settle)
			fmt.Fprintf(os.Stdout, "[DRY RUN] Would insert %d corrective row(s)\n", len(plan.Corrective))
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
		// Every exit from here restarts them: workers left down after an
		// aborted repair are worse than the drift. A failed restart surfaces in
		// the exit code even when the repair itself committed.
		defer func() {
			fmt.Fprintln(os.Stdout, "Starting workers…")
			if serr := wb.start(ctx, false, os.Stdout); serr != nil {
				fmt.Fprintln(os.Stderr, "error: start workers:", serr)
				fmt.Fprintln(os.Stderr, "Workers are still stopped — run: stockfix workers start")
				if code == 0 {
					code = 1
				}
			}
		}()
	}

	if assumeQuiesced {
		fmt.Fprintln(os.Stdout, "WARNING: --assume-quiesced set — skipping quiescence check (dangerous)")
	} else {
		quiesceFn := func(ctx context.Context) (quiescenceMark, error) {
			return quiescence(ctx, db, g.schema, g.tenantID)
		}
		if err := checkQuiescence(ctx, quiesceFn, settle, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}

	if !skipConfirm {
		if err := confirmInteractive(g.tenantID, os.Stdout, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "aborted:", err)
			return 1
		}
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: begin tx:", err)
		return 1
	}

	// Computed and applied here, after the stop: a plan built against the live
	// database before it would already be stale.
	plan, err := repairTenant(ctx, tx, g.schema, g.tenantID, os.Stdout)
	if err != nil {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stderr, "error: repair:", err)
		return 1
	}

	printRollupTable(os.Stdout, plan.Initial)
	fmt.Fprintln(os.Stdout)
	if len(plan.Corrective) == 0 {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stdout, "No fixable violations found.")
		return 0
	}
	renderFixPlan(os.Stdout, g.schema, plan.Corrective)

	// Verify the whole tenant inside the transaction before committing: the
	// repair may only leave behind violations that were already unfixable.
	remaining, err := verifyAfterFix(ctx, tx, g.schema, g.tenantID, plan.Unfixable)
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
		return 3
	}

	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "error: commit:", err)
		return 1
	}

	fmt.Fprintf(os.Stdout, "\nCOMMITTED: %d corrective row(s) inserted successfully.\n", len(plan.Corrective))
	return 0
}

// previewTimeout caps a dry run, which holds locks on the version index entries
// it writes until it rolls back — the same (tenant, repo, item, version) a
// concurrent create_item_movement_proc takes, since both use MAX(version) + 1.
// Blocking live movements is acceptable briefly and not at all for long.
const previewTimeout = 60 * time.Second

// previewRepair runs the repair in a transaction it always rolls back.
func previewRepair(ctx context.Context, db *sql.DB, schema, tenantID string) (*repairPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, previewTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	plan, err := repairTenant(ctx, tx, schema, tenantID, os.Stdout)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("dry run exceeded %v and was rolled back; run it "+
			"off-peak, against a replica, or go straight to --execute with the "+
			"workers stopped (which is not capped): %w", previewTimeout, err)
	}
	return plan, err
}
