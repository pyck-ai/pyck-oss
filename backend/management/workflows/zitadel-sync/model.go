package zitadel_sync

import "time"

// ZitadelSyncWorkflowInput is the input for the orchestrator workflow.
type ZitadelSyncWorkflowInput struct {
	Period time.Duration
}

// TenantSyncWorkflowInput is the input for the per-tenant sync workflow.
type TenantSyncWorkflowInput struct {
	TenantID string
}

// ReconcileUsersActivityInput carries Zitadel and DB users for reconciliation.
type ReconcileUsersActivityInput struct {
	ZitadelUsers []User
	DbUsers      []User
	TenantID     string
}

// FetchDbTenantsInput triggers fetching the org IDs of all live, fully
// registered DB tenants.
type FetchDbTenantsInput struct{}

// FetchDbUsersActivityInput triggers fetching DB users for a tenant.
type FetchDbUsersActivityInput struct {
	TenantID                      string
	ReconcileUsersActivityInputID string
}

// FetchZitadelUsersActivityInput triggers fetching Zitadel users for a tenant.
type FetchZitadelUsersActivityInput struct {
	TenantID                         string
	FetchZitadelUsersActivityInputID string
}

// User represents a user unified across Zitadel and the local DB.
type User struct {
	ID        string
	Username  string
	Email     string
	FirstName string
	LastName  string
	TenantID  string
	IsOwner   bool
}

type StartTenantSyncActivityInput struct {
	TenantID         string
	TaskQueue        string
	WorkflowIDPrefix string
}
