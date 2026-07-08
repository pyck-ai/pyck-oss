package main

import "fmt"

// DeltaClass categorises a stock ledger row's quantity delta vs the previous version.
type DeltaClass int

const (
	DeltaLegal        DeltaClass = iota // matches one of the four expected movement patterns
	DeltaLegalFlagged                    // combined repo-exit + mark-release; legal but flag for manual review
	DeltaAnomaly                         // does not match any known pattern
	DeltaClobber                         // anomaly on an item-movement create-mark row (definitive write-path bug)
)

func (d DeltaClass) String() string {
	switch d {
	case DeltaLegal:
		return "LEGAL"
	case DeltaLegalFlagged:
		return "LEGAL-FLAGGED"
	case DeltaAnomaly:
		return "ANOMALY"
	case DeltaClobber:
		return "CLOBBER"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(d))
	}
}

// classifyDelta classifies a stock ledger row's delta against expected movement semantics.
//
//   - dq    = quantity change vs previous version
//   - dinc  = incoming_stock change
//   - doutg = outgoing_stock change
//   - movKind is "item" for item_movements, "repo" for repository_movements, "" if no movement.
func classifyDelta(dq, dinc, doutg int64, movKind string) DeltaClass {
	switch {
	case dq == 0:
		// Pure mark: reserves changed but quantity unchanged.
		return DeltaLegal
	case dq > 0 && dinc == -dq:
		// Incoming execution: goods received, incoming reserve consumed.
		return DeltaLegal
	case dq < 0 && doutg == dq:
		// Outgoing execution: goods dispatched, outgoing reserve consumed.
		return DeltaLegal
	case dq != 0 && dinc == 0 && doutg == 0:
		// Bare repo-entry/exit: quantity changes but no reserves move.
		return DeltaLegal
	case dq < 0 && doutg < 0 && dinc == 0:
		// Combined repo-exit + mark-release; outgoing moved but doesn't equal dq.
		// Legal movement type but verify manually against the source movement.
		return DeltaLegalFlagged
	default:
		// Remaining rows are anomalies. A create-mark on an item_movement that
		// changes quantity is the definitive clobber pattern from the write-path bug.
		if movKind == "item" && dq != 0 {
			return DeltaClobber
		}
		return DeltaAnomaly
	}
}

// nextVersion returns maxVersion + 1 for a corrective stock row.
func nextVersion(maxVersion int64) int64 { return maxVersion + 1 }
