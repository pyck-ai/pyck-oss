package zitadel_sync

import "time"

// Tenant represents a Zitadel organization mapped to the local DB tenant.
type Tenant struct {
	ID      string
	Name    string
	Flavour string
}

// ZitadelSyncWorkflowInput is the input for the orchestrator workflow.
type ZitadelSyncWorkflowInput struct {
	Period time.Duration
}

// TenantSyncWorkflowInput is the input for the per-tenant sync workflow.
type TenantSyncWorkflowInput struct {
	TenantID string
}

// ReconcileTenantsActivityInput carries Zitadel and DB tenants for reconciliation.
type ReconcileTenantsActivityInput struct {
	ZitadelTenants []Tenant
	DbTenants      []Tenant
}

// ReconcileUsersActivityInput carries Zitadel and DB users for reconciliation.
type ReconcileUsersActivityInput struct {
	ZitadelUsers []User
	DbUsers      []User
	TenantID     string
}

// FetchZitadelTenantsActivityInput triggers fetching all Zitadel orgs.
type FetchZitadelTenantsActivityInput struct{}

// FetchDbTenantsInput triggers fetching all DB tenants.
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
