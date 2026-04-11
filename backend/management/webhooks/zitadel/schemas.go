package zitadel

// webhookBase contains common fields for all Zitadel webhook payloads.
type webhookBase struct {
	FullMethod     string `json:"fullMethod"`
	InstanceID     string `json:"instanceId"`
	OrganizationID string `json:"orgID"`
	CallerUserID   string `json:"userID"`
}

// SyncInput is the payload for the tenant sync webhook.
type SyncInput struct {
	webhookBase
}
