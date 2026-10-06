//go:build integration

package idempotencykeyencoding_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// KeyEncodingSuite provisions one fresh tenant with a writer.
type KeyEncodingSuite struct {
	tests.Base
}

// deleteMutations is one mutation per service. The idempotency check runs
// before the resolver, so a delete of a random id is enough to reach it.
var deleteMutations = map[string]string{
	"file":       "deleteFile",
	"main-data":  "deleteCustomer",
	"management": "deleteLocation",
	"picking":    "deletePickingOrder",
	"receiving":  "deleteReceivingInbound",
	"workflow":   "deleteWorkflow",
	"inventory":  "deleteInventoryItem",
}

// invalidKeys are not valid UTF-8 but pass Go's header validation.
var invalidKeys = map[string]string{
	"lone high byte":     "ike-\xff",
	"truncated sequence": "ike-\xc3",
}

type gqlError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions"`
}

// TestInvalidUTF8KeyIsAClientError sends every service a keyed mutation with
// an invalid UTF-8 key and expects 400 INVALID_IDEMPOTENCY_KEY, never the
// store error.
func (s *KeyEncodingSuite) TestInvalidUTF8KeyIsAClientError() {
	r := s.Require()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)
	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err)

	// Control: a valid key passes the idempotency check (the delete itself may
	// fail, a random id does not exist).
	for _, e := range s.send(p.PAT, rt.ID, "deleteInventoryItem", "ike-valid-"+uuid.NewString()) {
		r.NotContains(fmt.Sprint(e.Extensions["code"]), "IDEMPOTENCY", "a valid key must pass the check: %s", e.Message)
	}

	for service, mutation := range deleteMutations {
		for name, key := range invalidKeys {
			s.Run(service+"/"+name, func() {
				errs := s.send(p.PAT, rt.ID, mutation, key)
				s.Require().NotEmpty(errs, "%s: an invalid UTF-8 key was accepted", service)
				e := errs[0]
				s.Equalf("INVALID_IDEMPOTENCY_KEY", e.Extensions["code"],
					"%s: an invalid UTF-8 key must be a client error, got %v %q", service, e.Extensions["code"], e.Message)
				s.InDelta(float64(http.StatusBadRequest), e.Extensions["httpStatus"], 0)
				s.NotContains(e.Message, key, "the invalid bytes must not be echoed")
			})
		}
	}
}

// send posts one named mutation with the given Idempotency-Key and returns
// the GraphQL errors.
func (s *KeyEncodingSuite) send(pat, tenant, mutation, key string) []gqlError {
	r := s.Require()
	op := "IkeProbe"
	body, err := json.Marshal(map[string]any{
		"operationName": op,
		"query":         fmt.Sprintf(`mutation %s($id: ID!) { %s(id: $id) { deletedID } }`, op, mutation),
		"variables":     map[string]any{"id": uuid.NewString()},
	})
	r.NoError(err)
	req, err := http.NewRequestWithContext(s.Ctx, http.MethodPost, s.Cfg.GatewayURL, bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("X-Pyck-Tenant-Id", tenant)
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close() //nolint:errcheck // read-only body
	raw, err := io.ReadAll(resp.Body)
	r.NoError(err)
	var env struct {
		Errors []gqlError `json:"errors"`
	}
	r.NoError(json.Unmarshal(raw, &env), "decode (HTTP %d): %.300s", resp.StatusCode, raw)
	return env.Errors
}
