package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonhttp "github.com/pyck-ai/pyck/backend/common/http"
	pyckLog "github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/requestid"
)

func TestRequestIDMiddlewareGeneratesAndExposesID(t *testing.T) {
	t.Parallel()

	var observedID string

	handler := commonhttp.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedID = requestid.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	headerID := rec.Header().Get(requestid.HTTPHeader)
	require.NotEmpty(t, headerID, "middleware must set X-Request-ID response header")
	assert.Equal(t, headerID, observedID, "context request-id must match the response header")

	parsed, err := uuid.Parse(headerID)
	require.NoError(t, err, "request id must be a valid uuid")
	assert.Equal(t, uuid.Version(7), parsed.Version(), "request id must be uuid v7")
}

func TestRequestIDMiddlewareIgnoresClientSuppliedHeader(t *testing.T) {
	t.Parallel()

	const clientSupplied = "client-supplied-id"

	var observedID string

	handler := commonhttp.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedID = requestid.FromContext(r.Context())
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set(requestid.HTTPHeader, clientSupplied)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.NotEqual(t, clientSupplied, observedID, "client-supplied header must be ignored")
	assert.NotEqual(t, clientSupplied, rec.Header().Get(requestid.HTTPHeader), "response must not echo client-supplied header value")
}

func TestRequestIDMiddlewareGeneratesUniqueIDsPerRequest(t *testing.T) {
	t.Parallel()

	handler := commonhttp.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	assert.NotEqual(t, first.Header().Get(requestid.HTTPHeader), second.Header().Get(requestid.HTTPHeader))
}

// TestRequestIDPropagatesIntoLogger pins the end-to-end behavior the feature
// promises: when the HTTP middleware injects request-id into baggage and the
// downstream handler logs via pyckLog.ForContext(ctx), the structured field
// appears in the JSON log output without any explicit `.Ctx(ctx)` plumbing
// at the call site.
func TestRequestIDPropagatesIntoLogger(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	base := zerolog.New(&buf).Hook(zerolog.HookFunc(func(e *zerolog.Event, _ zerolog.Level, _ string) {
		ctx := e.GetCtx()
		if ctx == nil {
			return
		}
		id := requestid.FromContext(ctx)
		if id == "" {
			return
		}
		e.Str(requestid.LogField, id)
	}))

	var handlerObservedID string
	handler := commonhttp.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := base.WithContext(r.Context())
		handlerObservedID = requestid.FromContext(ctx)
		pyckLog.ForContext(ctx).Info().Msg("served")
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	headerID := rec.Header().Get(requestid.HTTPHeader)
	require.NotEmpty(t, headerID)
	assert.Equal(t, headerID, handlerObservedID)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, headerID, got[requestid.LogField], "X-Request-ID header value must equal the structured log field value")
}
