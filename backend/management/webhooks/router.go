package webhooks

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.temporal.io/sdk/client"

	zitadel "github.com/pyck-ai/pyck/backend/management/webhooks/zitadel"
)

// Router creates an HTTP router for webhook endpoints.
func Router(temporalClient client.Client, taskQueue string) http.Handler {
	router := chi.NewRouter()
	router.Post("/zitadel/sync", zitadel.SyncHandler(temporalClient, taskQueue))
	return router
}
