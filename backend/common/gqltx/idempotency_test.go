package gqltx_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

// fakeStore is a programmable [idempotency.Store] used by the
// middleware tests below. It tracks every call so assertions can verify
// ordering.
type fakeStore struct {
	mu sync.Mutex

	lookupErr error
	record    *idempotency.Record

	insertErr error
	markErr   error

	// insertErrs / markErrs are one-shot error queues: each call pops
	// the head; once drained the static insertErr / markErr applies.
	// Lets tests model "fail with a retryable PG error once, then
	// succeed" for the OCC-retry paths.
	insertErrs []error
	markErrs   []error

	insertCalls []idempotency.Record
	markCalls   []markCall
	lookupCalls []lookupCall
}

type markCall struct {
	Key      string
	TenantID uuid.UUID
	UserID   uuid.UUID
	Response []byte
}

type lookupCall struct {
	Key      string
	TenantID uuid.UUID
	UserID   uuid.UUID
}

// raceStore models the two lookup vantage points around a key race:
// Lookup (PreCheck, replica-servable) returns ErrNotFound while
// firstMiss is set; LookupForResolve (race resolver, writer-only)
// returns secondHit — the winner's committed row as the primary sees
// it, regardless of replica staleness.
type raceStore struct {
	fakeStore
	secondHit  *idempotency.Record
	firstMiss  bool
	lookupHits int
}

func (s *fakeStore) Lookup(_ context.Context, key string, tenantID, userID uuid.UUID) (*idempotency.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookupCalls = append(s.lookupCalls, lookupCall{key, tenantID, userID})
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	if s.record == nil {
		return nil, idempotency.ErrNotFound
	}
	cp := *s.record
	return &cp, nil
}

func (s *fakeStore) InsertInFlight(_ context.Context, rec idempotency.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insertCalls = append(s.insertCalls, rec)
	if len(s.insertErrs) > 0 {
		err := s.insertErrs[0]
		s.insertErrs = s.insertErrs[1:]
		return err
	}
	return s.insertErr
}

func (s *fakeStore) MarkCommitted(_ context.Context, key string, tenantID, userID uuid.UUID, response []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markCalls = append(s.markCalls, markCall{key, tenantID, userID, response})
	if len(s.markErrs) > 0 {
		err := s.markErrs[0]
		s.markErrs = s.markErrs[1:]
		return err
	}
	return s.markErr
}

// LookupForResolve delegates to Lookup: the fake has no replica, so
// reader and writer views are identical.
func (s *fakeStore) LookupForResolve(ctx context.Context, key string, tenantID, userID uuid.UUID) (*idempotency.Record, error) {
	return s.Lookup(ctx, key, tenantID, userID)
}

func (s *fakeStore) Prune(context.Context, time.Time) (int, error) { return 0, nil }

func (s *raceStore) Lookup(ctx context.Context, key string, tenantID, userID uuid.UUID) (*idempotency.Record, error) {
	s.mu.Lock()
	s.lookupHits++
	hit := s.lookupHits
	s.mu.Unlock()
	if hit == 1 && s.firstMiss {
		return nil, idempotency.ErrNotFound
	}
	if s.secondHit != nil {
		cp := *s.secondHit
		return &cp, nil
	}
	return nil, idempotency.ErrNotFound
}

// LookupForResolve is the writer-side view: it always sees the winner's
// committed row (secondHit), independent of how stale the replica-side
// Lookup is. Defined explicitly on raceStore — the embedded
// fakeStore.LookupForResolve would statically dispatch to
// fakeStore.Lookup and bypass the race semantics above.
func (s *raceStore) LookupForResolve(_ context.Context, _ string, _, _ uuid.UUID) (*idempotency.Record, error) {
	if s.secondHit != nil {
		cp := *s.secondHit
		return &cp, nil
	}
	return nil, idempotency.ErrNotFound
}

func staticAuth(tenantID, userID uuid.UUID) idempotency.AuthLookup {
	return func(context.Context) (uuid.UUID, uuid.UUID, bool) { return tenantID, userID, true }
}

// mustChecksum computes the checksum for the synthetic test operation +
// vars; tests always pass clean inputs so an error here means a test bug,
// not a runtime edge. It builds the operation via testMutationOp so the
// checksum matches the one PreCheck derives from the same operation.
func mustChecksum(t *testing.T, vars map[string]any) [32]byte {
	t.Helper()
	c, err := idempotency.OperationChecksum(testMutationOp(), nil, vars)
	require.NoError(t, err)
	return c
}

// testOpName is the synthetic operation name every test uses. Centralized
// so the helper signature stays parameter-free (every existing caller
// passed the same literal) — extract a parameter again when a test needs
// a different name.
const testOpName = "Mut"

// testMutationOp is the single operation definition shared by the request
// context (mutationCtxWithHeader) and the expected-checksum helper
// (mustChecksum) so the two cannot drift now that the checksum covers the
// selection set.
func testMutationOp() *ast.OperationDefinition {
	return &ast.OperationDefinition{
		Operation:    ast.Mutation,
		Name:         testOpName,
		SelectionSet: ast.SelectionSet{&ast.Field{Name: "doThing"}},
	}
}

func mutationCtxWithHeader(t *testing.T, key string) context.Context {
	t.Helper()
	oc := &graphql.OperationContext{
		Operation: testMutationOp(),
		Variables: map[string]any{"x": 1},
	}
	if key != "" {
		oc.Headers = map[string][]string{idempotency.HeaderName: {key}}
	}
	return graphql.WithOperationContext(context.Background(), oc)
}

func TestIdempotency_NoStore_FallsThroughToNormalFlow(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0) // no WithIdempotency

	ctx := mutationCtxWithHeader(t, "k-1") // header present, but no store

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.True(t, client.tx.committed, "tx should commit normally when idempotency is disabled")
}

func TestIdempotency_NoHeader_TxRunsButStoreUntouched(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(uuid.New(), uuid.New())))

	ctx := mutationCtxWithHeader(t, "") // no key

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.True(t, client.tx.committed)
	assert.Empty(t, store.insertCalls, "InsertInFlight must not be called without a key")
	assert.Empty(t, store.markCalls, "MarkCommitted must not be called without a key")
	assert.Empty(t, store.lookupCalls, "Lookup must not be called without a key")
}

func TestIdempotency_FreshKey_InsertsAndMarksCommitted(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	store := &fakeStore{} // lookup miss → ActionProceed
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	ctx := mutationCtxWithHeader(t, "k-1")

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	require.Empty(t, resp.Errors)

	require.Len(t, store.insertCalls, 1)
	assert.Equal(t, "k-1", store.insertCalls[0].Key)
	assert.Equal(t, tenantID, store.insertCalls[0].TenantID)
	assert.Equal(t, userID, store.insertCalls[0].UserID)
	assert.Equal(t, idempotency.StatusInFlight, store.insertCalls[0].Status)

	require.Len(t, store.markCalls, 1)
	assert.Equal(t, "k-1", store.markCalls[0].Key)
	assert.NotEmpty(t, store.markCalls[0].Response, "cached response body must be non-empty")

	assert.True(t, client.tx.committed)
}

func TestIdempotency_CommittedHit_ReplaysWithoutOpeningTx(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	checksum := mustChecksum(t, map[string]any{"x": 1})

	cachedBody := []byte(`{"data":{"cached":true}}`)
	store := &fakeStore{
		record: &idempotency.Record{
			Key:               "k-1",
			TenantID:          tenantID,
			UserID:            userID,
			OperationName:     "Mut",
			OperationChecksum: checksum,
			Status:            idempotency.StatusCommitted,
			Response:          cachedBody,
		},
	}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	ctx := mutationCtxWithHeader(t, "k-1")

	executed := false
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		executed = true
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"fresh":true}`)})
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	assert.JSONEq(t, `{"cached":true}`, string(resp.Data))
	assert.False(t, executed, "mutation must not execute on cache replay")
	assert.False(t, client.tx.committed, "no transaction may open on cache replay")
	assert.False(t, client.tx.rolledBack)
	assert.Empty(t, store.insertCalls, "InsertInFlight must not run on cache replay")
	assert.Empty(t, store.markCalls)
}

// Safety-net path: see TestPreCheck_InFlightHit_Conflict and §G10 of
// the knowledge file. status='in_flight' is never visible to a
// concurrent reader in production; this test uses a synthetic fakeStore
// row to prove that if the situation ever arose, the middleware would
// short-circuit cleanly.
func TestIdempotency_InFlightHit_ShortCircuits(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	checksum := mustChecksum(t, map[string]any{"x": 1})

	store := &fakeStore{
		record: &idempotency.Record{
			Key:               "k-1",
			TenantID:          tenantID,
			UserID:            userID,
			OperationName:     "Mut",
			OperationChecksum: checksum,
			Status:            idempotency.StatusInFlight,
		},
	}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	ctx := mutationCtxWithHeader(t, "k-1")

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		t.Fatal("mutation must not run when an in-flight record exists")
		return nil
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "IDEMPOTENCY_IN_FLIGHT", resp.Errors[0].Extensions["code"])
	assert.False(t, client.tx.committed)
}

func TestIdempotency_InsertUniqueViolation_ResolvesViaLookup(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	checksum := mustChecksum(t, map[string]any{"x": 1})

	// First the store sees no record (PreCheck), so PreCheck returns
	// Proceed. Then the InsertInFlight call returns ErrUniqueViolation
	// because a concurrent request inserted in the meantime. The next
	// Lookup call (the race resolver) sees the winner's committed row.
	cachedBody := []byte(`{"data":{"winner":true}}`)
	winnerRecord := &idempotency.Record{
		Key:               "k-1",
		TenantID:          tenantID,
		UserID:            userID,
		OperationName:     "Mut",
		OperationChecksum: checksum,
		Status:            idempotency.StatusCommitted,
		Response:          cachedBody,
	}

	// We need the store to behave differently across calls: first lookup
	// → miss, insert → unique violation, second lookup → winner.
	store := &raceStore{
		fakeStore:  fakeStore{insertErr: idempotency.ErrUniqueViolation},
		secondHit:  winnerRecord,
		firstMiss:  true,
		lookupHits: 0,
	}

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	ctx := mutationCtxWithHeader(t, "k-1")

	executed := false
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		executed = true
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"loser":true}`)})
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	assert.False(t, executed, "loser must not execute its own mutation")
	assert.JSONEq(t, `{"winner":true}`, string(resp.Data), "loser must replay winner's response")
	assert.False(t, client.tx.committed, "loser tx must not commit")
	assert.True(t, client.tx.rolledBack, "loser tx must be rolled back")
}

// TestIdempotency_OCCRetry_ReinsertsInFlightRow closes acceptance
// criterion #15 "rollback retry": a retryable Postgres error on the
// first attempt must roll back the in-flight row along with the
// mutation, and the next attempt must INSERT cleanly under the same
// idempotency lease (proving the rolled-back row doesn't shadow it
// at the DB layer, which here we approximate with a fakeStore that
// records every InsertInFlight call).
//
// Mirrors the OCC-retry plumbing tested by TestPostCommitHook_DoubleFireOnRetry,
// but the assertions are about the idempotency store rather than the
// post-commit hooks.
func TestIdempotency_OCCRetry_ReinsertsInFlightRow(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	store := &fakeStore{} // lookup miss → ActionProceed on every attempt
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "occidemprobe", 2,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	// Retryable Postgres serialization failure (same class as ErrOCCConflict).
	retryableErr := gqlerror.WrapPath(nil, &pq.Error{Code: "40001"})

	var attempt atomic.Int64
	op := func(ctx context.Context) graphql.ResponseHandler {
		return func(callCtx context.Context) *graphql.Response {
			if n := attempt.Add(1); n == 1 {
				return &graphql.Response{Errors: gqlerror.List{retryableErr}}
			}
			return &graphql.Response{Data: json.RawMessage(`{"ok":true}`)}
		}
	}

	ctx := mutationCtxWithHeader(t, "k-occ")

	resp := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, op)(ctx)
	require.NotNil(t, resp)
	require.Empty(t, resp.Errors, "final response must succeed after retry")
	assert.JSONEq(t, `{"ok":true}`, string(resp.Data))

	// Retry actually happened.
	require.EqualValues(t, 2, attempt.Load(), "resolver must run twice (failed attempt + retry)")
	assert.GreaterOrEqual(t, client.beginCnt, 2, "tx must have been begun at least twice")

	// THE ASSERTION UNDER TEST: the rolled-back in-flight row from attempt 1
	// did not block attempt 2's insert.
	require.Len(t, store.insertCalls, 2,
		"InsertInFlight must run on every attempt; got %d, which means either "+
			"the retry skipped the insert or the lease was lost on the recursive "+
			"call to handleMutationWithTx", len(store.insertCalls))

	// MarkCommitted must only fire on the successful attempt.
	require.Len(t, store.markCalls, 1, "MarkCommitted must run exactly once (on the successful retry)")
	assert.True(t, client.tx.committed, "tx must commit on the final successful attempt")
}

func TestIdempotency_MarkCommittedError_RollsBack(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	store := &fakeStore{
		markErr: errors.New("storage down"),
	}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	ctx := mutationCtxWithHeader(t, "k-1")

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Errors, "MarkCommitted failure must surface as an error")
	assert.True(t, client.tx.rolledBack, "tx must be rolled back when MarkCommitted fails")
	assert.False(t, client.tx.committed, "tx must NOT commit when MarkCommitted fails")
}

// TestIdempotency_InsertInFlightRetryablePG_RetriesTx covers the PR-review
// finding that a retryable PG error (40001 / 40P01) from the idempotency
// INSERT bypassed the OCC retry pipeline and surfaced as a hard 500. The
// first InsertInFlight fails with a wrapped serialization failure; the
// retry must re-run the whole transaction and succeed.
func TestIdempotency_InsertInFlightRetryablePG_RetriesTx(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		insertErrs: []error{fmt.Errorf("idempotency insert in-flight: %w", &pq.Error{Code: "40001"})},
	}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2,
		gqltx.WithIdempotency(store, staticAuth(uuid.New(), uuid.New())))

	ctx := mutationCtxWithHeader(t, "k-ins-retry")

	resp := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})(ctx)

	require.NotNil(t, resp)
	require.Empty(t, resp.Errors, "retryable InsertInFlight failure must be retried, not surfaced as 500")
	require.Len(t, store.insertCalls, 2, "InsertInFlight must run on the failed attempt AND the retry")
	require.Len(t, store.markCalls, 1, "MarkCommitted must fire once on the successful retry")
	assert.True(t, client.tx.committed)
}

// TestIdempotency_InsertInFlightRetryableExhausted_500 proves the retry
// budget is bounded: a PERSISTENT 40001 from InsertInFlight retries
// maxRetries times and then surfaces the structured 500 instead of
// looping forever.
func TestIdempotency_InsertInFlightRetryableExhausted_500(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		insertErr: fmt.Errorf("idempotency insert in-flight: %w", &pq.Error{Code: "40001"}),
	}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2,
		gqltx.WithIdempotency(store, staticAuth(uuid.New(), uuid.New())))

	ctx := mutationCtxWithHeader(t, "k-ins-exhaust")

	resp := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})(ctx)

	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Errors, "exhausted retries must surface an error")
	assert.Equal(t, "IDEMPOTENCY_STORE_ERROR", resp.Errors[0].Extensions["code"])
	// initial attempt + 2 budgeted retries = 3 inserts, then stop.
	require.Len(t, store.insertCalls, 3, "retry budget must be bounded (initial + maxRetries)")
	assert.False(t, client.tx.committed)
}

// TestIdempotency_MarkCommittedRetryablePG_RetriesTx mirrors the
// InsertInFlight case for the UPDATE side: a 40001 from MarkCommitted
// must retry the whole transaction (the rolled-back attempt's in-flight
// row is gone, so the retry re-inserts cleanly) instead of returning a
// hard 500.
func TestIdempotency_MarkCommittedRetryablePG_RetriesTx(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		markErrs: []error{fmt.Errorf("idempotency mark committed: %w", &pq.Error{Code: "40001"})},
	}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2,
		gqltx.WithIdempotency(store, staticAuth(uuid.New(), uuid.New())))

	ctx := mutationCtxWithHeader(t, "k-mark-retry")

	resp := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})(ctx)

	require.NotNil(t, resp)
	require.Empty(t, resp.Errors, "retryable MarkCommitted failure must be retried, not surfaced as 500")
	require.Len(t, store.insertCalls, 2, "retry must re-run InsertInFlight inside the fresh tx")
	require.Len(t, store.markCalls, 2, "MarkCommitted must run on the failed attempt AND the retry")
	assert.True(t, client.tx.committed)
}

// TestIdempotency_RaceWithReplicaLag_ReplaysFromWriter covers the
// PR-review finding that ResolveRace used the replica-servable Lookup:
// a client retry inside the replication-lag window saw ErrNotFound on
// the replica even though the winner's row was committed on the
// primary, and got a RACE_GHOST 500 instead of the cached 200 replay.
// Here Lookup is PERMANENTLY stale (always NotFound) and only
// LookupForResolve sees the committed row — the retry must still
// replay the winner's response.
func TestIdempotency_RaceWithReplicaLag_ReplaysFromWriter(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	checksum := mustChecksum(t, map[string]any{"x": 1})

	store := &raceStore{
		fakeStore: fakeStore{insertErr: idempotency.ErrUniqueViolation},
		secondHit: &idempotency.Record{
			Key:               "k-lag",
			TenantID:          tenantID,
			UserID:            userID,
			OperationName:     "Mut",
			OperationChecksum: checksum,
			Status:            idempotency.StatusCommitted,
			Response:          []byte(`{"data":{"winner":true}}`),
		},
		firstMiss: true, // replica never catches up within the request
	}

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	ctx := mutationCtxWithHeader(t, "k-lag")

	executed := false
	resp := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		executed = true
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"loser":true}`)})
	})(ctx)

	require.NotNil(t, resp)
	require.Empty(t, resp.Errors, "replica lag must not surface as RACE_GHOST 500")
	assert.False(t, executed, "retry must not re-execute the mutation")
	assert.JSONEq(t, `{"winner":true}`, string(resp.Data), "retry must replay the winner's cached body from the writer")
	assert.False(t, client.tx.committed)
}
