//go:build integration

package tenants

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// service identifies a backend subgraph that wires the revocation NATS
// subscriber into its auth provider (authn.SubscribeRevocations). query
// is the smallest valid federated query that touches that subgraph —
// only the auth outcome (accept / reject) is asserted, the response
// body is discarded.
type service struct {
	name  string
	query string
}

// probedServices is the list of subgraphs whose cmd/pyck-<svc>/main.go
// wires authn.SubscribeRevocations into the auth provider so that
// disabled tenants' tokens are evicted from the introspection cache.
var probedServices = []service{
	{name: "inventory", query: `{ inventoryItems(first: 1) { totalCount } }`},
	{name: "file", query: `{ files(first: 1) { totalCount } }`},
	{name: "main-data", query: `{ customers(first: 1) { totalCount } }`},
	{name: "management", query: `{ dataTypeEntities }`},
	{name: "picking", query: `{ pickingOrders(first: 1) { totalCount } }`},
	{name: "receiving", query: `{ receivingInbounds(first: 1) { totalCount } }`},
	{name: "workflow", query: `{ workflows(first: 1) { totalCount } }`},
}

// probeOutcome is the three-way classification of a single gateway probe.
// The split matters because the waiters below latch on definitive outcomes
// only: a gateway 502, a transport error, or a malformed response says
// nothing about the token and must be retried, never counted as an auth
// verdict.
type probeOutcome int

const (
	// probeAccepted: HTTP 200 with no top-level GraphQL errors.
	probeAccepted probeOutcome = iota
	// probeRejected: the request was definitively denied on auth grounds
	// (see tests.IsAuthDenial for the discriminator).
	probeRejected
	// probeInfraError: any outcome that is neither — transport failure,
	// unexpected HTTP status, decode failure, or a non-auth GraphQL error.
	probeInfraError
)

func (o probeOutcome) String() string {
	switch o {
	case probeAccepted:
		return "accepted"
	case probeRejected:
		return "rejected"
	default:
		return "infra-error"
	}
}

// probe sends a single GraphQL query through the gateway with the given
// bearer token and classifies the outcome. The returned error carries
// detail for the non-accepted outcomes; it is nil iff the probe was
// accepted.
func probe(ctx context.Context, cfg *config.Config, token, query string) (probeOutcome, error) {
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return probeInfraError, err
	}
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodPost, cfg.GatewayURL, bytes.NewReader(body))
	if err != nil {
		return probeInfraError, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := nethttp.DefaultClient.Do(req)
	if err != nil {
		return probeInfraError, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == nethttp.StatusUnauthorized || resp.StatusCode == nethttp.StatusForbidden:
		return probeRejected, fmt.Errorf("status %d", resp.StatusCode)
	case resp.StatusCode != nethttp.StatusOK:
		raw, _ := io.ReadAll(resp.Body)
		return probeInfraError, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}

	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return probeInfraError, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Errors) == 0 {
		return probeAccepted, nil
	}
	for _, e := range out.Errors {
		if tests.IsAuthDenial(e.Message) {
			return probeRejected, fmt.Errorf("%s", e.Message)
		}
	}
	return probeInfraError, fmt.Errorf("%s", out.Errors[0].Message)
}

// revocationTimeout bounds how long a disable/expiry may take to be
// rejected by every service. The NATS fast path evicts caches sub-second,
// but if a service misses that message the token stays accepted until the
// org-active verdict cache expires — bounded by ZitadelOrganizationCacheTTL
// (default 1m, see backend/common/env/config). A bound of exactly the TTL
// races that backstop; 90s clears it with margin so the backstop path still
// passes deterministically rather than flaking on the 60s boundary.
const revocationTimeout = 90 * time.Second

// waitUntilAll polls every probed service with the given bearer token
// until each one has produced the wanted definitive outcome, or the
// timeout fires. A service latches the first time it reports want and is
// not probed again; every other outcome — including infra errors — keeps
// it in the pending set for the next round. Returns per-service latency
// from the start of the wait to the latch.
func waitUntilAll(ctx context.Context, cfg *config.Config, token string, timeout time.Duration, want probeOutcome) (map[string]time.Duration, error) {
	start := time.Now()
	latchedAt := make(map[string]time.Duration, len(probedServices))
	err := tests.PollUntil(ctx, timeout, 100*time.Millisecond, func() error {
		var pending []string
		var lastErr error
		for _, s := range probedServices {
			if _, done := latchedAt[s.name]; done {
				continue
			}
			outcome, perr := probe(ctx, cfg, token, s.query)
			if outcome == want {
				latchedAt[s.name] = time.Since(start)
				continue
			}
			pending = append(pending, s.name)
			if perr != nil {
				lastErr = fmt.Errorf("%s: %s: %w", s.name, outcome, perr)
			} else {
				lastErr = fmt.Errorf("%s: still accepted", s.name)
			}
		}
		if len(pending) > 0 {
			return fmt.Errorf("services not yet %s: %v; last: %w", want, pending, lastErr)
		}
		return nil
	})
	return latchedAt, err
}

// waitUntilAllReject polls every probed service until each one
// definitively rejects the bearer token. Latching a service on its first
// definitive rejection is sound because revocation is monotonic once
// propagated: after a service has evicted the token and re-introspection
// failed against the inactive org, no later request within the phase can
// be accepted again. Infra faults and still-accepted responses retry.
func waitUntilAllReject(ctx context.Context, cfg *config.Config, token string, timeout time.Duration) (map[string]time.Duration, error) {
	return waitUntilAll(ctx, cfg, token, timeout, probeRejected)
}

// waitUntilAllAccept is the restore-path mirror of waitUntilAllReject:
// each service latches only on definitive acceptance; rejection (the
// restore has not propagated to it yet) and infra faults retry.
func waitUntilAllAccept(ctx context.Context, cfg *config.Config, token string, timeout time.Duration) (map[string]time.Duration, error) {
	return waitUntilAll(ctx, cfg, token, timeout, probeAccepted)
}
