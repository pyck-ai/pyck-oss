package webhooks

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"

	zitadel "github.com/pyck-ai/pyck/backend/management/webhooks/zitadel"
)

// Config bundles the values the webhook handlers need from the calling
// service. Kept as a single struct so adding new webhooks doesn't churn
// Router's signature.
type Config struct {
	TemporalClient       client.Client
	TenantSyncTaskQueue  string
	ZitadelAudience      string
	ZitadelActionSignKey string

	// Login-event webhook wiring. The route is mounted only when a publisher
	// and a signing key are both set; otherwise publishing is off (no
	// unverified route is served).
	Publisher                 events.Publisher
	NatsStreamName            string
	ZitadelLoginActionSignKey string
}

// Router creates an HTTP router for webhook endpoints.
func Router(cfg Config) http.Handler {
	router := chi.NewRouter()
	router.Post("/zitadel/sync", zitadel.SyncHandler(cfg.TemporalClient, cfg.TenantSyncTaskQueue))
	router.Post("/zitadel/actions/pre-token", zitadel.PreTokenHandler(cfg.ZitadelAudience, cfg.ZitadelActionSignKey))

	if cfg.Publisher != nil && cfg.ZitadelLoginActionSignKey != "" {
		router.Post("/zitadel/login", zitadel.LoginHandler(
			cfg.ZitadelAudience, cfg.ZitadelLoginActionSignKey, cfg.NatsStreamName, cfg.Publisher))
	} else {
		logger := log.DefaultLogger()
		logger.Info().Msg("zitadel login-event webhook disabled (missing publisher or signing key)")
	}

	return router
}
