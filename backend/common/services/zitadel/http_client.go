package zitadel

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	httpclient "github.com/pyck-ai/pyck/backend/common/http_client"
	"github.com/pyck-ai/pyck/backend/common/std"
)

// Scope that grants ZITADEL API audience on access tokens.
const (
	scopeZitadelAPI         = "openid profile email urn:zitadel:iam:org:project:id:zitadel:aud"
	grantType               = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	introspectAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

	FeatureLevelSystem   = "system"
	FeatureLevelInstance = "instance"
)

// REST path constants to avoid drift and typos.
const (
	//nolint:gosec // G101: not a credential; this is an OAuth endpoint path
	pathOAuthToken                   = "/oauth/v2/token"
	pathOAuthIntrospect              = "/oauth/v2/introspect"
	pathFeaturesFmt                  = "/v2/features/%s"
	pathActionsTargetsSearchV2Beta   = "/v2beta/actions/targets/search"
	pathActionsTargetsV2Beta         = "/v2beta/actions/targets"
	pathActionsTargetUpdateV2BetaFmt = "/v2beta/actions/targets/%s"
	pathActionsExecutionsV2Beta      = "/v2beta/actions/executions"

	// Legacy fallback for older deployments.
	pathActionsTargetsSearchLegacy = "/resources/v3alpha/actions/targets/_search"
)

var (
	ErrInvalidTargetParamsCreate = errors.New("invalid target parameters: name/endpoint/timeout must be set")
	ErrInvalidTargetParamsUpdate = errors.New("invalid target parameters: id/name/endpoint/timeout must be set")
	ErrInvalidExecutionParams    = errors.New("invalid execution parameters: targetID/method must be set")
)

type accessToken struct {
	Token     string
	TokenType string
	ExpireAt  int64
}

func (ac *accessToken) Expired() bool {
	return ac.ExpireAt < time.Now().UTC().Unix()
}

func (ac *accessToken) Headers() map[string]string {
	return map[string]string{
		"Authorization": fmt.Sprintf("%s %s", ac.TokenType, ac.Token),
	}
}

type ZitadelHttpClient struct {
	baseUrl         string
	accountFilePath string
	accessToken     *accessToken
	tlsSecure       bool
}

func HttpClient(audience string, serviceKeyPath string, tlsSecure bool) *ZitadelHttpClient {
	return &ZitadelHttpClient{
		baseUrl:         strings.TrimRight(audience, "/"),
		accountFilePath: serviceKeyPath,
		tlsSecure:       tlsSecure,
	}
}

// Introspect verifies an OAuth2 token using JWT profile client authentication.
func (cli *ZitadelHttpClient) Introspect(token string) (*IntrospectionResponse, error) {
	reqUrl := cli.url(pathOAuthIntrospect)

	assertion, err := cli.generateJwt()
	if err != nil {
		return nil, fmt.Errorf("generate jwt for introspection: %w", err)
	}

	form := url.Values{
		"client_assertion_type": {introspectAssertionType},
		"client_assertion":      {assertion},
		"token":                 {token},
	}

	response, err := cli.postForm(reqUrl, form)
	if err != nil {
		return nil, fmt.Errorf("introspect request failed: %w", err)
	}

	result, err := std.UnmarshalJson[IntrospectionResponse](response)
	if err != nil {
		return nil, fmt.Errorf("decode introspection response: %w", err)
	}

	return &result, nil
}

// GetFutureSettings returns system/instance feature flags.
func (cli *ZitadelHttpClient) GetFutureSettings(featureLevel string) (*featuresSettings, error) {
	if !slices.Contains([]string{FeatureLevelSystem, FeatureLevelInstance}, featureLevel) {
		return nil, fmt.Errorf("invalid feature level: %s", featureLevel)
	}
	reqUrl := cli.urlf(pathFeaturesFmt, featureLevel)

	response, err := cli.requestJSON("GET", reqUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("get features %s failed: %w", featureLevel, err)
	}

	result, err := std.UnmarshalJson[featuresSettings](response)
	if err != nil {
		return nil, fmt.Errorf("decode features response: %w", err)
	}

	return &result, nil
}

// UpdateActionFeature toggles Actions feature at system/instance level.
func (cli *ZitadelHttpClient) UpdateActionFeature(featureLevel string, enabled bool) error {
	if !slices.Contains([]string{FeatureLevelSystem, FeatureLevelInstance}, featureLevel) {
		return fmt.Errorf("invalid feature level: %s", featureLevel)
	}
	reqUrl := cli.urlf(pathFeaturesFmt, featureLevel)

	payload := updateActionsFeaturesRequest{Actions: enabled}
	reqBody, err := std.MarshalJson(payload)
	if err != nil {
		return fmt.Errorf("encode update features payload: %w", err)
	}

	if _, err := cli.requestJSON("PUT", reqUrl, reqBody); err != nil {
		return fmt.Errorf("update features %s failed: %w", featureLevel, err)
	}

	return nil
}

// GetTargetDetailsByName finds an Action Target by name (v2beta first, then legacy).
func (cli *ZitadelHttpClient) GetTargetDetailsByName(name string) (*targetDetails, error) {
	v2betaURL := cli.url(pathActionsTargetsSearchV2Beta)

	if response, err := cli.requestJSON("POST", v2betaURL, []byte("{}")); err == nil {
		result, unmarshalErr := std.UnmarshalJson[searchTargetsResponse](response)
		if unmarshalErr == nil {
			result.normalize()
			for _, target := range result.Targets {
				if target.Config.Name == name {
					return &target.Details, nil
				}
			}
			return nil, nil
		}
	}

	legacyURL := cli.url(pathActionsTargetsSearchLegacy)
	payload := []searchTargetsRequest{{NameFilter: searchTargetNameFilter{Name: name}}}
	reqBody, err := std.MarshalJson(payload)
	if err != nil {
		return nil, fmt.Errorf("encode legacy search payload: %w", err)
	}

	response, err := cli.requestJSON("POST", legacyURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("legacy targets search failed: %w", err)
	}

	legacyResult, err := std.UnmarshalJson[searchTargetsResponse](response)
	if err != nil {
		return nil, fmt.Errorf("decode legacy search response: %w", err)
	}
	legacyResult.normalize()

	for _, target := range legacyResult.Targets {
		if target.Config.Name == name {
			return &target.Details, nil
		}
	}

	return nil, nil
}

// CreateActionTarget registers a new Action Target (v2beta).
func (cli *ZitadelHttpClient) CreateActionTarget(name string, interruptWebHookOnError bool, endpoint string, timeout time.Duration) (*targetDetails, error) {
	if name == "" || endpoint == "" || timeout <= 0 {
		return nil, ErrInvalidTargetParamsCreate
	}
	reqUrl := cli.url(pathActionsTargetsV2Beta)

	payload := targetConfig{
		Name:     name,
		Endpoint: endpoint,
		Timeout:  fmt.Sprintf("%ds", int(timeout.Seconds())),
		RestWebhook: targetHooksSetup{
			InterruptOnError: interruptWebHookOnError,
		},
	}
	reqBody, err := std.MarshalJson(payload)
	if err != nil {
		return nil, fmt.Errorf("encode create target payload: %w", err)
	}

	response, err := cli.requestJSON("POST", reqUrl, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create action target failed: %w", err)
	}

	result, err := std.UnmarshalJson[createTargetsResponse](response)
	if err != nil {
		return nil, fmt.Errorf("decode create target response: %w", err)
	}
	return &result.Details, nil
}

// UpdateActionTarget updates an existing Action Target (v2beta).
func (cli *ZitadelHttpClient) UpdateActionTarget(targetID string, name string, interruptWebHookOnError bool, endpoint string, timeout time.Duration) (*targetConfig, error) {
	if targetID == "" || name == "" || endpoint == "" || timeout <= 0 {
		return nil, ErrInvalidTargetParamsUpdate
	}
	reqUrl := cli.urlf(pathActionsTargetUpdateV2BetaFmt, targetID)

	payload := targetConfig{
		Name:     name,
		Endpoint: endpoint,
		Timeout:  fmt.Sprintf("%ds", int(timeout.Seconds())),
		RestWebhook: targetHooksSetup{
			InterruptOnError: interruptWebHookOnError,
		},
	}
	reqBody, err := std.MarshalJson(payload)
	if err != nil {
		return nil, fmt.Errorf("encode update target payload: %w", err)
	}

	response, err := cli.requestJSON("POST", reqUrl, reqBody)
	if err != nil {
		return nil, fmt.Errorf("update action target failed: %w", err)
	}

	result, err := std.UnmarshalJson[targetConfig](response)
	if err != nil {
		return nil, fmt.Errorf("decode update target response: %w", err)
	}

	return &result, nil
}

// CreateOrUpdateActionTarget idempotently creates or updates a target by name.
func (cli *ZitadelHttpClient) CreateOrUpdateActionTarget(name string, interruptWebHookOnError bool, endpoint string, timeout time.Duration) (*targetDetails, error) {
	targetDetails, err := cli.GetTargetDetailsByName(name)
	if err != nil {
		return nil, err
	}
	if targetDetails == nil {
		return cli.CreateActionTarget(name, interruptWebHookOnError, endpoint, timeout)
	}
	if _, err = cli.UpdateActionTarget(targetDetails.ID, name, interruptWebHookOnError, endpoint, timeout); err != nil {
		return nil, err
	}

	return targetDetails, nil
}

// CreateExecution binds a target to an execution condition (request/response).
func (cli *ZitadelHttpClient) CreateExecution(targetID string, conditionMethod string, withResponse bool) error {
	if targetID == "" || conditionMethod == "" {
		return ErrInvalidExecutionParams
	}
	reqUrl := cli.url(pathActionsExecutionsV2Beta)

	execCondition := executionCondition{}
	if withResponse {
		execCondition.Response = &responseCondition{Method: conditionMethod}
	} else {
		execCondition.Request = &responseCondition{Method: conditionMethod}
	}

	payload := createExecutionRequest{
		Condition: execCondition,
		Execution: executionReq{
			Targets: []executionTargets{{Target: targetID}},
		},
	}
	reqBody, err := std.MarshalJson(payload)
	if err != nil {
		return fmt.Errorf("encode create execution payload: %w", err)
	}

	if _, err := cli.requestJSON("PUT", reqUrl, reqBody); err != nil {
		return fmt.Errorf("create execution failed: %w", err)
	}

	return nil
}

// getAccessToken returns a cached access token or obtains a new one via JWT profile.
func (cli *ZitadelHttpClient) getAccessToken() (*accessToken, error) {
	if cli.accessToken != nil && !cli.accessToken.Expired() {
		return cli.accessToken, nil
	}

	assertion, err := cli.generateJwt()
	if err != nil {
		return nil, fmt.Errorf("generate jwt for token: %w", err)
	}

	reqUrl := cli.url(pathOAuthToken)
	form := url.Values{
		"grant_type": {grantType},
		"assertion":  {assertion},
		"scope":      {scopeZitadelAPI},
	}

	response, err := cli.postForm(reqUrl, form)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	tokenResponse, err := std.UnmarshalJson[accessTokenResponse](response)
	if err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	now := time.Now().UTC().Unix()
	if cli.accessToken == nil {
		cli.accessToken = &accessToken{}
	}
	cli.accessToken.Token = tokenResponse.AccessToken
	if tokenResponse.TokenType == "" {
		cli.accessToken.TokenType = "Bearer"
	} else {
		cli.accessToken.TokenType = tokenResponse.TokenType
	}
	const skewSeconds int64 = 10
	cli.accessToken.ExpireAt = now + int64(tokenResponse.ExpiresIn) - skewSeconds

	return cli.accessToken, nil
}

// generateJwt builds a JWT profile assertion for token/introspection.
func (cli *ZitadelHttpClient) generateJwt() (string, error) {
	accountBody, err := os.ReadFile(cli.accountFilePath)
	if err != nil {
		return "", fmt.Errorf("read service key: %w", err)
	}

	account, err := std.UnmarshalJson[keyFile](accountBody)
	if err != nil {
		return "", fmt.Errorf("decode service key: %w", err)
	}

	keyFileId := account.UserID
	if account.Type == keyFileTypeApplication {
		keyFileId = account.ClientID
	}

	now := time.Now().UTC()
	jwtClaims := jwt.MapClaims{
		"iss": keyFileId,
		"sub": keyFileId,
		"aud": cli.baseUrl,
		"exp": now.Add(time.Hour * 1).Unix(),
		"iat": now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwtClaims)
	token.Header["kid"] = account.KeyID

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(account.Key))
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}

	bearerToken, err := token.SignedString(privateKey)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}

	return bearerToken, nil
}

// defaultHeaders returns JSON headers including Authorization.
func (cli *ZitadelHttpClient) defaultHeaders() (map[string]string, error) {
	accessToken, err := cli.getAccessToken()
	if err != nil {
		return nil, err
	}
	headers := accessToken.Headers()
	headers["Content-Type"] = "application/json"
	return headers, nil
}

// Helpers

func (cli *ZitadelHttpClient) url(path string) string {
	return cli.baseUrl + path
}

func (cli *ZitadelHttpClient) urlf(format string, a ...any) string {
	return cli.baseUrl + fmt.Sprintf(format, a...)
}

func (cli *ZitadelHttpClient) postForm(reqUrl string, form url.Values) ([]byte, error) {
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}
	response, err := httpclient.MakeRequest("POST", reqUrl, headers, nil, []byte(form.Encode()), cli.tlsSecure)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (cli *ZitadelHttpClient) requestJSON(method string, reqUrl string, body []byte) ([]byte, error) {
	headers, err := cli.defaultHeaders()
	if err != nil {
		return nil, err
	}

	response, err := httpclient.MakeRequest(method, reqUrl, headers, nil, body, cli.tlsSecure)
	if err != nil {
		return nil, err
	}

	return response, nil
}
