package checkstock

import (
	"time"

	"github.com/google/uuid"
)

type WorkflowInput struct {
	ApiURL    string
	AuthToken string
}

type WorkflowOutput struct {
	CheckedAt          time.Time
	TotalChecked       int
	TotalDiscrepancies int
	Discrepancies      []Discrepancy
	Failed             bool
}

type TimeRange struct {
	StartOfDay     time.Time
	EndOfDay       time.Time
	EndOfYesterday time.Time
}

type Transaction struct {
	ItemID       uuid.UUID
	RepositoryID uuid.UUID
	Quantity     int
	Type         string
}

type Discrepancy struct {
	ItemID       uuid.UUID
	RepositoryID uuid.UUID
	Expected     int
	Actual       int
}

type FetchTransactionsInput struct {
	ApiURL    string
	AuthToken string
	TimeRange TimeRange
}

type ComputeExpectedStockInput struct {
	ApiURL         string
	AuthToken      string
	Transactions   []Transaction
	EndOfYesterday time.Time
}

type CompareStockInput struct {
	ApiURL       string
	AuthToken    string
	Expectations []StockExpectation
	EndOfDay     time.Time
}

type StockKey struct {
	ItemID       uuid.UUID
	RepositoryID uuid.UUID
}

type StockExpectation struct {
	ItemID       uuid.UUID `json:"item_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
	Expected     int       `json:"expected"`
}
