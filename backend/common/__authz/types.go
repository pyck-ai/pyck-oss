package authz

import (
	"github.com/google/uuid"
)

// PolicyData represents a policy from the management service
type PolicyData struct {
	ID       uuid.UUID `json:"id"`
	Resource string    `json:"resource"`
	Action   string    `json:"action"`
	Effect   string    `json:"effect"`
	RoleID   uuid.UUID `json:"roleId"`
	TenantID uuid.UUID `json:"tenantId"`
}

// RoleData represents a role from the management service
type RoleData struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	TenantID    uuid.UUID   `json:"tenantId"`
	UserIDs     []uuid.UUID `json:"userIds"`
	GroupIDs    []uuid.UUID `json:"groupIds"`
}

// GroupData represents a group from the management service
type GroupData struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	TenantID    uuid.UUID   `json:"tenantId"`
	UserIDs     []uuid.UUID `json:"userIds"`
	RoleIDs     []uuid.UUID `json:"roleIds"`
}

// AuthzCacheOptions contains configuration for the authorization cache
type AuthzCacheOptions struct {
	GatewayURL  string
	JwtToken    string
	Stream      string
	ServiceName string
	
	// ResourcePrefixes only load policies for these resource prefixes (e.g., ["inventory", "picking"])
	ResourcePrefixes []string
	
	// MaxCacheSize is the maximum number of tenant enforcers to keep in memory (default: 100)
	MaxCacheSize int
	
	// EventSubjects are custom event subjects to monitor (overrides default if provided)
	EventSubjects []string
	
	// IncludeManagementEvents indicates whether to include standard management events (default: true)
	IncludeManagementEvents bool
}