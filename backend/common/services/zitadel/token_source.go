package zitadel

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	httpclient "github.com/pyck-ai/pyck/backend/common/http_client"
	"github.com/pyck-ai/pyck/backend/common/std"
)

// jwtProfileTokenSource implements oauth2.TokenSource using JWT profile
// credentials, exchanging them directly at the token endpoint without OIDC
// discovery. This is useful when the internal API URL differs from the
// external issuer URL (e.g. http://zitadel:8080 vs https://auth.example.com:8080).
type jwtProfileTokenSource struct {
	mu       sync.Mutex
	tokenURL string // e.g. "http://zitadel:8080/oauth/v2/token"
	audience string // external issuer URL for JWT aud claim
	loadKey  func() ([]byte, error)
	cached   *oauth2.Token
}

// NewJWTProfileTokenSource creates an oauth2.TokenSource that exchanges JWT
// profile assertions at the given token URL. The audience is set as the JWT
// aud claim (must match the Zitadel issuer).
func NewJWTProfileTokenSource(apiURL, audience, keyFilePath string) *jwtProfileTokenSource {
	return newTokenSource(apiURL, audience, func() ([]byte, error) {
		return os.ReadFile(keyFilePath)
	})
}

// NewJWTProfileTokenSourceFromData is like NewJWTProfileTokenSource but uses
// in-memory key data instead of reading from a file.
func NewJWTProfileTokenSourceFromData(apiURL, audience string, keyData []byte) *jwtProfileTokenSource {
	return newTokenSource(apiURL, audience, func() ([]byte, error) {
		return keyData, nil
	})
}

func newTokenSource(apiURL, audience string, loadKey func() ([]byte, error)) *jwtProfileTokenSource {
	tokenURL := strings.TrimRight(apiURL, "/") + pathOAuthToken
	return &jwtProfileTokenSource{
		tokenURL: tokenURL,
		audience: strings.TrimRight(audience, "/"),
		loadKey:  loadKey,
	}
}

func (ts *jwtProfileTokenSource) Token() (*oauth2.Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.cached != nil && ts.cached.Valid() {
		return ts.cached, nil
	}

	accountBody, err := ts.loadKey()
	if err != nil {
		return nil, fmt.Errorf("read service key: %w", err)
	}

	account, err := std.UnmarshalJson[keyFile](accountBody)
	if err != nil {
		return nil, fmt.Errorf("decode service key: %w", err)
	}

	keyFileID := account.UserID
	if account.Type == keyFileTypeApplication {
		keyFileID = account.ClientID
	}

	now := time.Now().UTC()
	jwtClaims := jwt.MapClaims{
		"iss": keyFileID,
		"sub": keyFileID,
		"aud": ts.audience,
		"exp": now.Add(time.Hour).Unix(),
		"iat": now.Unix(),
	}
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwtClaims)
	jwtToken.Header["kid"] = account.KeyID

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(account.Key))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	assertion, err := jwtToken.SignedString(privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign jwt: %w", err)
	}

	form := url.Values{
		"grant_type": {grantType},
		"assertion":  {assertion},
		"scope":      {scopeZitadelAPI},
	}

	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}
	response, err := httpclient.MakeRequest("POST", ts.tokenURL, headers, nil, []byte(form.Encode()), false)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}

	tokenResp, err := std.UnmarshalJson[accessTokenResponse](response)
	if err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	tokenType := tokenResp.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}

	const skewSeconds = 10
	ts.cached = &oauth2.Token{
		AccessToken: tokenResp.AccessToken,
		TokenType:   tokenType,
		Expiry:      now.Add(time.Duration(tokenResp.ExpiresIn-skewSeconds) * time.Second),
	}

	return ts.cached, nil
}
