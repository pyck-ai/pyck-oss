package zitadel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
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
	pathOAuthToken      = "/oauth/v2/token"
	pathOAuthIntrospect = "/oauth/v2/introspect"
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
	oauth2URL       string
	audience        string
	accountFilePath string
	accessToken     *accessToken
	tlsSecure       bool
}

func HttpClient(baseURL string, audience string, serviceKeyPath string, tlsSecure bool) *ZitadelHttpClient {
	return &ZitadelHttpClient{
		oauth2URL:       strings.TrimRight(baseURL, "/"),
		audience:        strings.TrimRight(audience, "/"),
		accountFilePath: serviceKeyPath,
		tlsSecure:       tlsSecure,
	}
}

// Introspect verifies an OAuth2 token using JWT profile client authentication.
func (cli *ZitadelHttpClient) Introspect(ctx context.Context, token string) (*IntrospectionResponse, error) {
	reqUrl := cli.oauth2URL + pathOAuthIntrospect

	assertion, err := cli.generateJwt()
	if err != nil {
		return nil, fmt.Errorf("generate jwt for introspection: %w", err)
	}

	form := url.Values{
		"client_assertion_type": {introspectAssertionType},
		"client_assertion":      {assertion},
		"token":                 {token},
	}

	response, err := cli.postForm(ctx, reqUrl, form)
	if err != nil {
		return nil, fmt.Errorf("introspect request failed: %w", err)
	}

	result, err := std.UnmarshalJson[IntrospectionResponse](response)
	if err != nil {
		return nil, fmt.Errorf("decode introspection response: %w", err)
	}

	return &result, nil
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
		"aud": cli.audience,
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

func (cli *ZitadelHttpClient) postForm(ctx context.Context, reqUrl string, form url.Values) ([]byte, error) {
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}
	response, err := httpclient.MakeRequest(ctx, "POST", reqUrl, headers, nil, []byte(form.Encode()), cli.tlsSecure)
	if err != nil {
		return nil, err
	}

	return response, nil
}
