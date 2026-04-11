package http

import (
	nethttp "net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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

	// Add recovery middleware last
	mx.Use(middleware.Recoverer)

	return mx
}
