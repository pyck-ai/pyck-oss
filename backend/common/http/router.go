package http

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httplog"
	"github.com/pyck-ai/pyck/backend/common/otel"
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
