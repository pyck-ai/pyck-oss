package main

import "time"

// RollupViolation is a (repo, item) pair where stored quantity ≠ own_quantity + sum(children).
type RollupViolation struct {
	TenantID       string
	RepoID         string
	RepoName       string
	ItemID         string
	StoredQty      int64
	ExpectedQty    int64
	OwnQty         int64
	OwnIncomingQty int64
	OwnOutgoingQty int64
	IncomingQty    int64
	OutgoingQty    int64
	CreatedBy      string
	HasCurrentRow  bool
}

// Discrepancy returns expected − stored (positive means stored is too low).
func (v RollupViolation) Discrepancy() int64 { return v.ExpectedQty - v.StoredQty }

// AnomalyRow is a ledger row whose deltas violate expected movement semantics.
type AnomalyRow struct {
	Version       int64
	PrevQty       int64
	Qty           int64
	DQ            int64 // quantity delta vs previous version
	DINC          int64 // incoming_stock delta
	DOUTG         int64 // outgoing_stock delta
	CreatedAt     time.Time
	MovementID    string // empty string if NULL
	MovKind       string // "item" | "repo" | ""
	MovCreatedAt  *time.Time
	MovExecutedAt *time.Time
	Class         DeltaClass
}

// CorrectiveRow is the INSERT payload to repair one rollup violation.
type CorrectiveRow struct {
	ID          string
	TenantID    string
	RepoID      string
	ItemID      string
	Version     int64
	Quantity    int64 // corrected: own_quantity + sum(children current quantity)
	OwnQuantity int64 // unchanged from live row
	Incoming    int64 // unchanged from live row
	Outgoing    int64 // unchanged from live row
	OwnIncoming int64 // unchanged from live row
	OwnOutgoing int64 // unchanged from live row
	CreatedBy   string
}

// WorkerState persists replica counts / machine IDs so stop can be undone by start.
type WorkerState struct {
	FlyMachines map[string][]string `json:"fly_machines"` // app → stopped machine IDs
	K8sReplicas map[string]int      `json:"k8s_replicas"` // "ns/deploy" → original replica count
}

// flyMachineInfo is the subset of flyctl machine list --json we need.
type flyMachineInfo struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// analysisOutput is the JSON envelope for --json output.
type analysisOutput struct {
	RollupViolations []RollupViolation `json:"rollup_violations"`
	LedgerAnomalies  []AnomalyRow      `json:"ledger_anomalies"`
}
