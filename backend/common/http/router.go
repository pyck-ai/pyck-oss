package http

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httplog"
	"github.com/pyck-ai/pyck/backend/common/otel"
	"github.com/pyck-ai/pyck/backend/common/requestid"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/rs/zerolog"
)

var (
	routerSkipPaths = []string{"/health", "/metrics"}
)

type Middleware func(nethttp.Handler) nethttp.Handler

type RouterConfig struct {
	ServiceName string
	Logger      *zerolog.Logger
	Middlewares []Middleware
}

func NewRouter(config RouterConfig) *chi.Mux {
	mx := chi.NewRouter()

	// Add tracing middleware
	mx.Use(otel.HTTPMiddleware(config.ServiceName, mx))

	// Generate request ID and inject into OTel baggage; must run after the
	// OTel middleware so the span context is available, and before the request
	// logger so the field appears on the access log line.
	mx.Use(RequestIDMiddleware)

	// Add request logger middleware
	mx.Use(httplog.RequestLogger(*config.Logger, routerSkipPaths))

	// Add custom middlewares
	for _, mw := range config.Middlewares {
		mx.Use(mw)
	}

	// Add recovery middleware last — returns JSON errors on panic
	mx.Use(jsonRecoverer)

	return mx
}

// RequestIDMiddleware generates a server-side UUID v7 request ID for every
// inbound request, stores it in OTel baggage under requestid.BaggageKey, and
// echoes it back in the X-Request-ID response header. Client-supplied values
// are intentionally ignored to guarantee the ID is server-controlled.
func RequestIDMiddleware(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		id := uuidgql.GenerateV7UUID().String()

		ctx, err := requestid.WithRequestID(r.Context(), id)
		if err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).
				Msg("failed to inject request-id into baggage")
			ctx = r.Context()
		}

		w.Header().Set(requestid.HTTPHeader, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// jsonRecoverer is a middleware that recovers from panics and returns a JSON error response.
func jsonRecoverer(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		defer func() {
			if rvr := recover(); rvr != nil {
				if err, ok := rvr.(error); ok && errors.Is(err, nethttp.ErrAbortHandler) {
					panic(rvr)
				}

				zerolog.Ctx(r.Context()).Error().
					Str("stack", string(debug.Stack())).
					Msgf("panic recovered: %v", rvr)

				if r.Header.Get("Connection") != "Upgrade" {
					JSONError(w, fmt.Sprintf("%v", rvr), nethttp.StatusInternalServerError)
				}
			}
		}()

		next.ServeHTTP(w, r)
	})
}
