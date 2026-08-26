//go:build integration

package tenantclaim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client/profile"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// tenantClaimKey is the plain top-level OIDC claim the Actions v2 webhook
// splices onto machine-user tokens.
const tenantClaimKey = "pyck_tenant_id"

// mintMachineToken mints an OIDC access token for the machine user via the
// private_key_jwt grant, using the JSON keyfile bytes returned by
// zitadelclient.AddMachineKey. The request scopes in the pyck project
// audience so the introspection client can validate the token — and it is
// this token request that fires Zitadel's Actions v2 Execution → the pyck
// pre-token webhook.
func mintMachineToken(ctx context.Context, cfg *config.Config, keyfile []byte) (string, error) {
	src, err := profile.NewJWTProfileTokenSourceFromKeyFileData(
		ctx,
		cfg.ZitadelIssuer,
		keyfile,
		[]string{
			oidc.ScopeOpenID,
			zitadel.ScopeZitadelAPI(),
			zitadel.ScopeProjectID(cfg.ZitadelProjectID),
		},
	)
	if err != nil {
		return "", fmt.Errorf("build JWT-profile token source: %w", err)
	}
	tok, err := src.Token()
	if err != nil {
		return "", fmt.Errorf("mint OIDC access token: %w", err)
	}
	return tok.AccessToken, nil
}

// introspect posts the access token to Zitadel's introspection endpoint
// using the temporal-web client's basic-auth credentials and returns the
// decoded claims. An active=false response is treated as an error.
func introspect(ctx context.Context, cfg *config.Config, accessToken string) (map[string]any, error) {
	body := url.Values{"token": {accessToken}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.ZitadelIssuer, "/")+"/oauth/v2/introspect",
		strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(cfg.IntrospectClientID, cfg.IntrospectClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspect HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, fmt.Errorf("decode introspect response: %w (raw=%s)", err, string(raw))
	}
	if active, _ := claims["active"].(bool); !active {
		return nil, fmt.Errorf("introspect active=false (raw=%s)", string(raw))
	}
	return claims, nil
}

// waitForTenantClaim mints a fresh token and introspects it until the
// pyck_tenant_id claim equals want, or the timeout elapses. Polls because a
// freshly-minted machine key and its project grant are eventually
// consistent in Zitadel — the first token request after provisioning can
// race the key/grant projection.
//
// want is a canonical UUID string (authn.ComputeUUID(...).String()), so the
// exact-equality wait is also the claim-shape check: a regression to the
// legacy base64 urn:zitadel:iam:user:metadata encoding can never compare
// equal and surfaces as a timeout whose wrapped error reports the last
// claim value seen.
func waitForTenantClaim(ctx context.Context, cfg *config.Config, keyfile []byte, want string, timeout time.Duration) error {
	return tests.PollUntil(ctx, timeout, 300*time.Millisecond, func() error {
		got, err := mintIntrospectClaim(ctx, cfg, keyfile)
		switch {
		case err != nil:
			return err
		case got == "":
			return fmt.Errorf("no top-level %q claim yet (webhook not fired?)", tenantClaimKey)
		case got != want:
			return fmt.Errorf("claim %s=%q, want %q", tenantClaimKey, got, want)
		}
		return nil
	})
}

// mintIntrospectClaim does one mint→introspect round and returns the
// pyck_tenant_id claim value ("" if absent).
func mintIntrospectClaim(ctx context.Context, cfg *config.Config, keyfile []byte) (string, error) {
	tok, err := mintMachineToken(ctx, cfg, keyfile)
	if err != nil {
		return "", err
	}
	claims, err := introspect(ctx, cfg, tok)
	if err != nil {
		return "", err
	}
	got, _ := claims[tenantClaimKey].(string)
	return got, nil
}
