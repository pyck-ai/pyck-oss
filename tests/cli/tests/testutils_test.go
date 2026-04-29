package tests_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Yamashou/gqlgenc/clientv2"

	"github.com/pyck-ai/pyck/backend/common/importexport"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	maindataapi "github.com/pyck-ai/pyck/backend/main-data/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	receivingapi "github.com/pyck-ai/pyck/backend/receiving/api"
)

const (
	defaultGatewayURL = "http://localhost:4000"
	bootstrapEnvPath  = "../../../config/keys/bootstrap.env"
	healthCheckPath   = "/.well-known/apollo/server-health"
)

// authInterceptor adds a Bearer token to each GraphQL request.
type authInterceptor struct {
	token string
}

func (a *authInterceptor) intercept(
	ctx context.Context,
	req *http.Request,
	gqlInfo *clientv2.GQLRequestInfo,
	res any,
	next clientv2.RequestInterceptorFunc,
) error {
	req.Header.Set("Authorization", "Bearer "+a.token)
	return next(ctx, req, gqlInfo, res)
}

// requireGateway fails the test if the gateway is not reachable.
func requireGateway(t *testing.T) string {
	t.Helper()

	url := envOr("PYCK_GATEWAY_URL", defaultGatewayURL)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+healthCheckPath, nil)
	if err != nil {
		t.Fatalf("build health check request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gateway not reachable at %s (run 'task init up' first): %v", url, err)
	}
	resp.Body.Close()
	return url
}

// buildTestRegistry creates a registry with all service clients pointed at the gateway.
func buildTestRegistry(t *testing.T, gatewayURL, token string) *importexport.Registry {
	t.Helper()

	interceptor := &authInterceptor{token: token}
	intercept := interceptor.intercept
	reg := importexport.NewRegistry()

	if err := managementapi.RegisterEntities(reg,
		managementapi.NewClient(http.DefaultClient, gatewayURL, nil, intercept)); err != nil {
		t.Fatalf("register management: %v", err)
	}
	if err := inventoryapi.RegisterEntities(reg,
		inventoryapi.NewClient(http.DefaultClient, gatewayURL, nil, intercept)); err != nil {
		t.Fatalf("register inventory: %v", err)
	}
	if err := maindataapi.RegisterEntities(reg,
		maindataapi.NewClient(http.DefaultClient, gatewayURL, nil, intercept)); err != nil {
		t.Fatalf("register main-data: %v", err)
	}
	if err := pickingapi.RegisterEntities(reg,
		pickingapi.NewClient(http.DefaultClient, gatewayURL, nil, intercept)); err != nil {
		t.Fatalf("register picking: %v", err)
	}
	if err := receivingapi.RegisterEntities(reg,
		receivingapi.NewClient(http.DefaultClient, gatewayURL, nil, intercept)); err != nil {
		t.Fatalf("register receiving: %v", err)
	}

	return reg
}

// loadAuthToken reads the auth token from config/keys/bootstrap.env.
func loadAuthToken(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(bootstrapEnvPath)
	if err != nil {
		t.Skipf("%s not found (run 'task init' first): %v", bootstrapEnvPath, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "PYCK_TEST_AUTH_TOKEN" {
			return strings.TrimSpace(v)
		}
	}

	t.Skipf("PYCK_TEST_AUTH_TOKEN not found in %s", bootstrapEnvPath)
	return ""
}

// countExportedLines counts non-empty lines in a JSONL byte slice.
func countExportedLines(data []byte) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) > 0 {
			count++
		}
	}
	return count
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// formatResult returns a human-readable summary of an import result.
func formatResult(r *importexport.ImportResult) string {
	return fmt.Sprintf("created=%d updated=%d skipped=%d errors=%d",
		r.Created, r.Updated, r.Skipped, len(r.Errors))
}
