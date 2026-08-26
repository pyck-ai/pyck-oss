package main

import (
	"context"
	"fmt"
	"io"
)

// maxRepairRounds bounds the bottom-up peel: a chain of depth d needs d rounds,
// and create_item_movement_proc caps the repository walk at 64.
const maxRepairRounds = 64

// repairPlan is the outcome of one repair pass over a tenant.
type repairPlan struct {
	Initial    []RollupViolation  // violations seen before the first correction
	Corrective []CorrectiveRow    // rows inserted, children before their parents
	Unfixable  map[stockPair]bool // pairs with no current row to correct; left alone
}

// repairTenant corrects every fixable rollup violation through q, which MUST be
// a transaction: each round re-reads the invariant and has to see the rows the
// previous one inserted.
//
// A parent's expected quantity is the sum of its children's CURRENT quantities,
// so correcting a child moves its parent's target. Pricing every row from one
// pre-repair snapshot would write parents against stale children; instead each
// round corrects only the pairs with no fixable violation left below them.
func repairTenant(ctx context.Context, q querier, schema, tenantID string, out io.Writer) (*repairPlan, error) {
	parents, err := repoParents(ctx, q, schema, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load repository parents: %w", err)
	}
	versions, err := maxVersions(ctx, q, schema, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load max versions: %w", err)
	}

	plan := &repairPlan{Unfixable: map[stockPair]bool{}}

	for round := 0; ; round++ {
		if round >= maxRepairRounds {
			return nil, fmt.Errorf("repair did not converge after %d rounds", maxRepairRounds)
		}

		violations, err := rollupViolations(ctx, q, schema, tenantID)
		if err != nil {
			return nil, fmt.Errorf("rollup check: %w", err)
		}
		if round == 0 {
			plan.Initial = violations
			for _, v := range violations {
				if !v.HasCurrentRow {
					plan.Unfixable[v.Pair()] = true
					fmt.Fprintf(out, "WARNING: %s/%s has no current stock row — cannot auto-fix, manual intervention required\n",
						v.RepoID, v.ItemID)
				}
			}
		}

		fixable := make([]RollupViolation, 0, len(violations))
		for _, v := range violations {
			if v.HasCurrentRow && !plan.Unfixable[v.Pair()] {
				fixable = append(fixable, v)
			}
		}
		if len(fixable) == 0 {
			return plan, nil
		}

		ready := readyToCorrect(fixable, parents)
		if len(ready) == 0 {
			// Only reachable if parent_id describes a cycle rather than a tree.
			return nil, fmt.Errorf("repair stalled: %d violation(s) all wait on a descendant", len(fixable))
		}

		for _, v := range ready {
			pair := v.Pair()
			versions[pair]++
			row := CorrectiveRow{
				ID:          newUUID(),
				TenantID:    v.TenantID,
				RepoID:      v.RepoID,
				ItemID:      v.ItemID,
				Version:     versions[pair],
				Quantity:    v.ExpectedQty,
				OwnQuantity: v.OwnQty,
				Incoming:    v.IncomingQty,
				Outgoing:    v.OutgoingQty,
				OwnIncoming: v.OwnIncomingQty,
				OwnOutgoing: v.OwnOutgoingQty,
				CreatedBy:   v.CreatedBy,
			}
			if err := insertCorrectiveRow(ctx, q, schema, row); err != nil {
				return nil, fmt.Errorf("insert %s/%s: %w", row.RepoID, row.ItemID, err)
			}
			plan.Corrective = append(plan.Corrective, row)
		}
	}
}

// readyToCorrect returns the violations with no fixable violation anywhere
// below them, so a later round cannot invalidate their expected quantity.
//
// Every ancestor is blocked, not just the direct parent: a node in between can
// be consistent today and turn violated once its own child is corrected, and
// pricing the top before that lands costs it a second, wrong row in the ledger.
func readyToCorrect(fixable []RollupViolation, parents map[string]string) []RollupViolation {
	blocked := make(map[stockPair]bool, len(fixable))
	for _, v := range fixable {
		// Depth-capped like the proc's ancestor walk, so a parent_id cycle
		// cannot spin here.
		repo := parents[v.RepoID]
		for depth := 0; repo != "" && depth < maxRepairRounds; depth++ {
			blocked[stockPair{RepoID: repo, ItemID: v.ItemID}] = true
			repo = parents[repo]
		}
	}

	ready := make([]RollupViolation, 0, len(fixable))
	for _, v := range fixable {
		if !blocked[v.Pair()] {
			ready = append(ready, v)
		}
	}
	return ready
}
