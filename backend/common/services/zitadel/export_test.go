package zitadel

import managementclient "github.com/zitadel/zitadel-go/v3/pkg/client/management"

// NewTestSdkClient creates a ZitadelSdkClient with the given management client for testing.
func NewTestSdkClient(mgmt *managementclient.Client) *ZitadelSdkClient {
	return &ZitadelSdkClient{managementAPI: mgmt}
}
