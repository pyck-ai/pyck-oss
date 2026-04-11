package zitadel

import "time"

const (
	keyFileTypeApplication = "application"
)

type keyFile struct {
	Type     string `json:"type"`
	UserID   string `json:"userId"`
	ClientID string `json:"clientId"`
	KeyID    string `json:"keyId"`
	Key      string `json:"key"`
}

type accessTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type IntrospectionResponse struct {
	Active              bool                         `json:"active"`
	Aud                 []string                     `json:"aud"`
	Exp                 int64                        `json:"exp"`
	Iat                 int64                        `json:"iat"`
	Iss                 string                       `json:"iss"`
	Jti                 string                       `json:"jti"`
	Name                string                       `json:"name"`
	Nbf                 int64                        `json:"nbf"`
	PreferredUsername   string                       `json:"preferred_username"`
	Scope               string                       `json:"scope"`
	Sub                 string                       `json:"sub"`
	TokenType           string                       `json:"token_type"`
	UpdatedAt           int64                        `json:"updated_at"`
	ProjectRoles        map[string]map[string]string `json:"urn:zitadel:iam:org:project:roles"`
	ResourceOwnerID     string                       `json:"urn:zitadel:iam:user:resourceowner:id"`
	ResourceOwnerName   string                       `json:"urn:zitadel:iam:user:resourceowner:name"`
	ResourceOwnerDomain string                       `json:"urn:zitadel:iam:user:resourceowner:primary_domain"`
	Username            string                       `json:"username"`
}

type featureConfig struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

type featuresSettings struct {
	LoginDefaultOrg                     featureConfig `json:"loginDefaultOrg"`
	OidcTriggerIntrospectionProjections featureConfig `json:"oidcTriggerIntrospectionProjections"`
	OidcLegacyIntrospection             featureConfig `json:"oidcLegacyIntrospection"`
	UserSchema                          featureConfig `json:"userSchema"`
	OidcTokenExchange                   featureConfig `json:"oidcTokenExchange"`
	Actions                             featureConfig `json:"actions"`
	ImprovedPerformance                 featureConfig `json:"improvedPerformance"`
	WebKey                              featureConfig `json:"webKey"`
	DebugOidcParentError                featureConfig `json:"debugOidcParentError"`
	OidcSingleV1SessionTermination      featureConfig `json:"oidcSingleV1SessionTermination"`
}

type updateActionsFeaturesRequest struct {
	Actions bool `json:"actions"`
}

type targetDetails struct {
	ID       string      `json:"id"`
	CreateAt time.Time   `json:"created"`
	Changed  time.Time   `json:"changed"`
	Owner    targetOwner `json:"owner"`
}

type searchTargetNameFilter struct {
	Name string `json:"targetName"`
}

type searchTargetIdsFilter struct {
	TargetIds []string `json:"targetIds"`
}

// Legacy v3alpha search request (kept for fallback compatibility)
type searchTargetsRequest struct {
	NameFilter searchTargetNameFilter `json:"targetNameFilter"`
	IdsFilter  searchTargetIdsFilter  `json:"targetIdsFilter"`
}

type searchTargetsResponseDetails struct {
	AppliedLimit string    `json:"appliedLimit"`
	TotalCount   string    `json:"totalCount"`
	Timestamp    time.Time `json:"timestamp"`
}

type targetConfig struct {
	Name        string           `json:"name"`
	RestWebhook targetHooksSetup `json:"restWebhook"`
	Endpoint    string           `json:"endpoint"`
	Timeout     string           `json:"timeout"`
}

type searchTargetsTarget struct {
	Details    targetDetails `json:"details"`
	Config     targetConfig  `json:"config"`
	SigningKey string        `json:"signingKey"`
}

// Response shape normalization helper:
// Some builds return the list under "result", others under "targets" (v2beta).
type searchTargetsResponse struct {
	Details searchTargetsResponseDetails `json:"details"`
	Targets []searchTargetsTarget        `json:"result"`  // legacy
	Alt     []searchTargetsTarget        `json:"targets"` // v2beta
}

func (r *searchTargetsResponse) normalize() {
	if len(r.Targets) == 0 && len(r.Alt) > 0 {
		r.Targets = r.Alt
	}
}

type targetHooksSetup struct {
	InterruptOnError bool `json:"interruptOnError"`
}

type targetOwner struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type createTargetsResponse struct {
	Details    targetDetails `json:"details"`
	SigningKey string        `json:"signingKey"`
}

type responseCondition struct {
	Method string `json:"method"`
}

type executionCondition struct {
	Request  *responseCondition `json:"request,omitempty"`
	Response *responseCondition `json:"response,omitempty"`
}

type executionTargets struct {
	Target string `json:"target"`
}

type executionReq struct {
	Targets []executionTargets `json:"targets"`
}

type createExecutionRequest struct {
	Condition executionCondition `json:"condition"`
	Execution executionReq       `json:"execution"`
}
