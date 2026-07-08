package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"
)

func cmdAnalyze(args []string) int {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	var g globalFlags
	addGlobal(fs, &g)
	var (
		repoID  string
		itemID  string
		sinceS  string
		asJSON  bool
	)
	fs.StringVar(&repoID, "repo", "", "Specific repository UUID for ledger anomaly scan (requires --item)")
	fs.StringVar(&itemID, "item", "", "Specific item UUID for ledger anomaly scan (requires --repo)")
	fs.StringVar(&sinceS, "since", "7d", "Lookback window for ledger anomaly scan (e.g. 7d, 168h)")
	fs.BoolVar(&asJSON, "json", false, "Emit JSON instead of human-readable tables")
	fs.Parse(args)

	g.dbURL = resolveDBURL(g.dbURL)
	if g.dbURL == "" {
		fmt.Fprintln(os.Stderr, "error: --db-url or PYCK_DATABASE_URL / PYCK_DATABASE_MASTER_URL required")
		return 1
	}
	if err := validateSchema(g.schema); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if (repoID == "") != (itemID == "") {
		fmt.Fprintln(os.Stderr, "error: --repo and --item must be specified together")
		return 1
	}

	sinceD, err := parseDuration(sinceS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: --since:", err)
		return 1
	}
	sinceTime := time.Now().Add(-sinceD)

	ctx := context.Background()
	db, err := openDB(ctx, g.dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: connect:", err)
		return 1
	}
	defer db.Close()

	// Check 1: rollup invariant.
	violations, err := rollupViolations(ctx, db, g.schema, g.tenantID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: rollup check:", err)
		return 1
	}

	// Determine which (repo, item) pairs to scan in check 2.
	type pair struct{ tenantID, repo, item string }
	var scanPairs []pair
	if repoID != "" {
		scanPairs = []pair{{g.tenantID, repoID, itemID}}
	} else {
		for _, v := range violations {
			if v.HasCurrentRow {
				scanPairs = append(scanPairs, pair{v.TenantID, v.RepoID, v.ItemID})
			}
		}
	}

	// Check 2: ledger anomaly scan.
	var anomalies []AnomalyRow
	for _, p := range scanPairs {
		if p.tenantID == "" {
			fmt.Fprintf(os.Stderr, "warning: skipping anomaly scan for %s/%s: tenant unknown\n", p.repo, p.item)
			continue
		}
		rows, err := ledgerAnomalies(ctx, db, g.schema, p.tenantID, p.repo, p.item, sinceTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: anomaly scan %s/%s: %v\n", p.repo, p.item, err)
			return 1
		}
		anomalies = append(anomalies, rows...)
	}

	if asJSON {
		printAnalysisJSON(os.Stdout, violations, anomalies)
	} else {
		printRollupTable(os.Stdout, violations)
		fmt.Fprintln(os.Stdout)
		printAnomalyTable(os.Stdout, anomalies)
	}

	if len(violations) > 0 || len(anomalies) > 0 {
		return 3
	}
	return 0
}

func printRollupTable(w io.Writer, violations []RollupViolation) {
	fmt.Fprintln(w, "=== ROLLUP INVARIANT CHECK ===")
	if len(violations) == 0 {
		fmt.Fprintln(w, "  OK — no violations found.")
		return
	}
	fmt.Fprintf(w, "  FAIL — %d violation(s) found:\n\n", len(violations))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TENANT\tREPO_ID\tREPO_NAME\tITEM_ID\tSTORED\tEXPECTED\tDIFF\tHAS_ROW")
	fmt.Fprintln(tw, "------\t-------\t---------\t-------\t------\t--------\t----\t-------")
	for _, v := range violations {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%+d\t%v\n",
			v.TenantID, v.RepoID, v.RepoName, v.ItemID,
			v.StoredQty, v.ExpectedQty, v.Discrepancy(), v.HasCurrentRow,
		)
	}
	tw.Flush()
}

func printAnomalyTable(w io.Writer, anomalies []AnomalyRow) {
	fmt.Fprintln(w, "=== LEDGER ANOMALY SCAN ===")
	if len(anomalies) == 0 {
		fmt.Fprintln(w, "  OK — no anomalies found.")
		return
	}
	fmt.Fprintf(w, "  FAIL — %d anomaly row(s) found:\n\n", len(anomalies))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tPREV_QTY\tQTY\tDQ\tDINC\tDOUTG\tCLASS\tMOV_KIND\tMOV_ID\tCREATED_AT")
	fmt.Fprintln(tw, "-------\t--------\t---\t--\t----\t-----\t-----\t--------\t------\t----------")
	for _, a := range anomalies {
		movID := a.MovementID
		if len(movID) > 18 {
			movID = movID[:8] + "…"
		}
		fmt.Fprintf(tw, "%d\t%d\t%d\t%+d\t%+d\t%+d\t%s\t%s\t%s\t%s\n",
			a.Version, a.PrevQty, a.Qty, a.DQ, a.DINC, a.DOUTG,
			a.Class, a.MovKind, movID, a.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	tw.Flush()

	for _, a := range anomalies {
		if a.Class == DeltaClobber {
			fmt.Fprintf(w, "\n[!] CLOBBER DETECTED version=%d movement=%s (%s) created_at=%s\n",
				a.Version, a.MovementID, a.MovKind, a.CreatedAt.UTC().Format(time.RFC3339))
			if a.MovCreatedAt != nil {
				fmt.Fprintf(w, "    movement.created_at  = %s\n", a.MovCreatedAt.UTC().Format(time.RFC3339))
			}
			if a.MovExecutedAt != nil {
				fmt.Fprintf(w, "    movement.executed_at = %s\n", a.MovExecutedAt.UTC().Format(time.RFC3339))
			}
		}
	}
}

func printAnalysisJSON(w io.Writer, violations []RollupViolation, anomalies []AnomalyRow) {
	out := analysisOutput{
		RollupViolations: violations,
		LedgerAnomalies:  anomalies,
	}
	if out.RollupViolations == nil {
		out.RollupViolations = []RollupViolation{}
	}
	if out.LedgerAnomalies == nil {
		out.LedgerAnomalies = []AnomalyRow{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
