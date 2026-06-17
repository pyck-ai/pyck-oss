package idempotency_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

// stubStore is a minimal in-memory [idempotency.Store] for unit tests.
type stubStore struct {
	lookupErr error
	record    *idempotency.Record
}

func (s *stubStore) Lookup(_ context.Context, _ string, _, _ uuid.UUID) (*idempotency.Record, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	if s.record == nil {
		return nil, idempotency.ErrNotFound
	}
	cp := *s.record
	return &cp, nil
}

// LookupForResolve delegates to Lookup: the stub has no replica, so
// reader and writer views are identical.
func (s *stubStore) LookupForResolve(ctx context.Context, key string, tenantID, userID uuid.UUID) (*idempotency.Record, error) {
	return s.Lookup(ctx, key, tenantID, userID)
}

func (s *stubStore) InsertInFlight(_ context.Context, _ idempotency.Record) error { return nil }
func (s *stubStore) MarkCommitted(_ context.Context, _ string, _, _ uuid.UUID, _ []byte) error {
	return nil
}
func (s *stubStore) Prune(_ context.Context, _ time.Time) (int, error) { return 0, nil }

// mutationOp builds a minimal mutation operation definition with the given
// name and top-level selection fields. Shared by mutationOpCtx and
// mustChecksum so the operation a test feeds into PreCheck and the operation
// it derives the expected checksum from cannot drift apart.
func mutationOp(opName string, fields []string) *ast.OperationDefinition {
	sels := make(ast.SelectionSet, 0, len(fields))
	for _, f := range fields {
		sels = append(sels, &ast.Field{Name: f})
	}
	return &ast.OperationDefinition{
		Operation:    ast.Mutation,
		Name:         opName,
		SelectionSet: sels,
	}
}

func mutationOpCtx(t *testing.T, opName string, fields []string, vars map[string]any) *graphql.OperationContext {
	t.Helper()
	return &graphql.OperationContext{
		Operation: mutationOp(opName, fields),
		Variables: vars,
	}
}

func authYes(tenantID, userID uuid.UUID) idempotency.AuthLookup {
	return func(context.Context) (uuid.UUID, uuid.UUID, bool) { return tenantID, userID, true }
}

func authNo(context.Context) (uuid.UUID, uuid.UUID, bool) { return uuid.Nil, uuid.Nil, false }

func headerWithKey(key string) http.Header {
	h := http.Header{}
	if key != "" {
		h.Set(idempotency.HeaderName, key)
	}
	return h
}

func TestPreCheck_NoHeader_Skip(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), http.Header{}, mutationOpCtx(t, "X", []string{"x"}, nil), &stubStore{}, authYes(uuid.New(), uuid.New()))
	assert.Equal(t, idempotency.ActionSkip, got.Action)
}

func TestPreCheck_KeyTooLong_BadRequest(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), headerWithKey(strings.Repeat("x", idempotency.MaxKeyLen+1)),
		mutationOpCtx(t, "X", []string{"x"}, nil), &stubStore{}, authYes(uuid.New(), uuid.New()))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusBadRequest, got.Status)
}

func TestPreCheck_MissingOperationName_BadRequest(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "", []string{"x"}, nil), &stubStore{}, authYes(uuid.New(), uuid.New()))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	require.NotNil(t, got.Response)
	require.Len(t, got.Response.Errors, 1)
	assert.Equal(t, "MISSING_OPERATION_NAME", got.Response.Errors[0].Extensions["code"])
}

func TestPreCheck_MultipleTopLevelFields_BadRequest(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "Multi", []string{"a", "b"}, nil), &stubStore{}, authYes(uuid.New(), uuid.New()))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	require.Len(t, got.Response.Errors, 1)
	assert.Equal(t, "MULTI_FIELD_MUTATION", got.Response.Errors[0].Extensions["code"])
}

// TestPreCheck_TypenamePlusOneMutation_Proceeds covers the PR-review
// finding that Apollo Client / Relay inject __typename at the
// operation root by default: `mutation Foo { __typename createX {id} }`
// is a SINGLE-mutation request and must not be rejected as
// MULTI_FIELD_MUTATION. Introspection fields (__ prefix) are excluded
// from the field count.
func TestPreCheck_TypenamePlusOneMutation_Proceeds(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "Foo", []string{"__typename", "createX"}, nil),
		&stubStore{}, authYes(uuid.New(), uuid.New()))

	assert.Equal(t, idempotency.ActionProceed, got.Action)
	require.NotNil(t, got.Lease)
}

// TestPreCheck_TypenamePlusTwoMutations_BadRequest proves the __ skip
// does not weaken the multi-field guard: two real mutation fields are
// still rejected even with __typename in the mix.
func TestPreCheck_TypenamePlusTwoMutations_BadRequest(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "Foo", []string{"__typename", "createX", "createY"}, nil),
		&stubStore{}, authYes(uuid.New(), uuid.New()))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	require.Len(t, got.Response.Errors, 1)
	assert.Equal(t, "MULTI_FIELD_MUTATION", got.Response.Errors[0].Extensions["code"])
}

// TestPreCheck_MultipleFieldsViaInlineFragment_BadRequest reproduces
// the M1 review finding: an inline-fragment wrapper hid multiple mutation
// fields from the pre-fix count (each fragment counted as 1 regardless
// of how many fields it contained), letting multi-field mutations slip
// past the single-mutation contract. Post-fix the recursive walk in
// topLevelMutationCount sees both `a` and `b` and the request is rejected.
func TestPreCheck_MultipleFieldsViaInlineFragment_BadRequest(t *testing.T) {
	t.Parallel()

	oc := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			Name:      "FragWrapped",
			SelectionSet: ast.SelectionSet{
				&ast.InlineFragment{
					SelectionSet: ast.SelectionSet{
						&ast.Field{Name: "a"},
						&ast.Field{Name: "b"},
					},
				},
			},
		},
	}
	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"), oc, &stubStore{}, authYes(uuid.New(), uuid.New()))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	require.Len(t, got.Response.Errors, 1)
	assert.Equal(t, "MULTI_FIELD_MUTATION", got.Response.Errors[0].Extensions["code"])
}

func TestPreCheck_NoAuth_BadRequest(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "X", []string{"x"}, nil), &stubStore{}, authNo)

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	require.Len(t, got.Response.Errors, 1)
	assert.Equal(t, "UNAUTHENTICATED", got.Response.Errors[0].Extensions["code"])
}

func TestPreCheck_NotMutation_Skip(t *testing.T) {
	t.Parallel()

	oc := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation:    ast.Query,
			Name:         "GetX",
			SelectionSet: ast.SelectionSet{&ast.Field{Name: "x"}},
		},
	}
	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"), oc, &stubStore{}, authYes(uuid.New(), uuid.New()))
	assert.Equal(t, idempotency.ActionSkip, got.Action)
}

func TestPreCheck_NoOperation_BadRequest(t *testing.T) {
	t.Parallel()

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"), &graphql.OperationContext{}, &stubStore{}, authYes(uuid.New(), uuid.New()))
	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusBadRequest, got.Status)
}

func TestPreCheck_FreshKey_Proceed(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "CreateX", []string{"createX"}, map[string]any{"name": "n"}),
		&stubStore{}, authYes(tenantID, userID))

	require.Equal(t, idempotency.ActionProceed, got.Action)
	require.NotNil(t, got.Lease)
	assert.Equal(t, "k1", got.Lease.Key)
	assert.Equal(t, tenantID, got.Lease.TenantID)
	assert.Equal(t, userID, got.Lease.UserID)
	assert.Equal(t, "CreateX", got.Lease.OperationName)
	assert.Equal(t, mustChecksum(t, "CreateX", []string{"createX"}, map[string]any{"name": "n"}), got.Lease.OperationChecksum)
}

func TestPreCheck_CommittedHit_Replays(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	vars := map[string]any{"id": "1"}
	body := []byte(`{"data":{"hello":"world"}}`)

	store := &stubStore{
		record: &idempotency.Record{
			Key:               "k1",
			TenantID:          tenantID,
			UserID:            userID,
			OperationName:     "Hello",
			OperationChecksum: mustChecksum(t, "Hello", []string{"hello"}, vars),
			Status:            idempotency.StatusCommitted,
			Response:          body,
		},
	}

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "Hello", []string{"hello"}, vars),
		store, authYes(tenantID, userID))

	require.Equal(t, idempotency.ActionReplay, got.Action)
	assert.Equal(t, http.StatusOK, got.Status)
	require.NotNil(t, got.Response)
	assert.JSONEq(t, string(body), mustJSON(t, got.Response))
}

// Exercises the defensive 409 IN_FLIGHT branch. In production this is
// unreachable: the writing tx promotes status='in_flight' to 'committed'
// before COMMIT, so no concurrent reader sees in_flight. We feed the
// branch a synthetic row to prove the safety net still routes correctly
// if any future refactor or DB tampering creates an in_flight visible
// row. See knowledge file 1123 §G10 and the CodeInFlight docstring.
func TestPreCheck_InFlightHit_Conflict(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	vars := map[string]any{"x": 1}

	store := &stubStore{
		record: &idempotency.Record{
			Key:               "k1",
			TenantID:          tenantID,
			UserID:            userID,
			OperationName:     "DoThing",
			OperationChecksum: mustChecksum(t, "DoThing", []string{"doThing"}, vars),
			Status:            idempotency.StatusInFlight,
		},
	}

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "DoThing", []string{"doThing"}, vars),
		store, authYes(tenantID, userID))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusConflict, got.Status)
	require.Len(t, got.Response.Errors, 1)
	assert.Equal(t, "IDEMPOTENCY_IN_FLIGHT", got.Response.Errors[0].Extensions["code"])
}

func TestPreCheck_OperationMismatch_Unprocessable(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()

	store := &stubStore{
		record: &idempotency.Record{
			Key:               "k1",
			TenantID:          tenantID,
			UserID:            userID,
			OperationName:     "Hello",
			OperationChecksum: mustChecksum(t, "Hello", []string{"hello"}, map[string]any{"a": 1}),
			Status:            idempotency.StatusCommitted,
			Response:          []byte(`{"data":null}`),
		},
	}

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "Hello", []string{"hello"}, map[string]any{"a": 2}),
		store, authYes(tenantID, userID))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusUnprocessableEntity, got.Status)
	require.Len(t, got.Response.Errors, 1)
	assert.Equal(t, "OPERATION_MISMATCH", got.Response.Errors[0].Extensions["code"])
}

func TestPreCheck_StoreError_InternalServerError(t *testing.T) {
	t.Parallel()

	store := &stubStore{lookupErr: errors.New("boom")}

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "CreateX", []string{"createX"}, nil),
		store, authYes(uuid.New(), uuid.New()))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusInternalServerError, got.Status)
}

func TestPreCheck_CommittedHit_CorruptCache_Errors(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	vars := map[string]any{"id": "1"}

	store := &stubStore{
		record: &idempotency.Record{
			Key:               "k1",
			TenantID:          tenantID,
			UserID:            userID,
			OperationName:     "Hello",
			OperationChecksum: mustChecksum(t, "Hello", []string{"hello"}, vars),
			Status:            idempotency.StatusCommitted,
			Response:          []byte(`not valid json`),
		},
	}

	got := idempotency.PreCheck(t.Context(), headerWithKey("k1"),
		mutationOpCtx(t, "Hello", []string{"hello"}, vars),
		store, authYes(tenantID, userID))

	assert.Equal(t, idempotency.ActionShortCircuit, got.Action)
	assert.Equal(t, http.StatusInternalServerError, got.Status)
}

func mustJSON(t *testing.T, r *graphql.Response) string {
	t.Helper()
	b, err := idempotency.SerializeResponse(r, 0)
	require.NoError(t, err)
	return string(b)
}
