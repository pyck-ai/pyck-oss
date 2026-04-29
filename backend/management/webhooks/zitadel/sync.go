package zitadel

import (
	"io"
	"net/http"

	"go.temporal.io/sdk/client"

	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/std"

	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

// SyncHandler returns an HTTP handler that triggers the TenantSyncWorkflow.
// This webhook is called by Zitadel when user registration or other events occur.
func SyncHandler(temporalClient client.Client, taskQueue string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			httputil.JSONError(w, "error reading body", http.StatusBadRequest)
			return
		}

		input, err := std.UnmarshalJson[SyncInput](reqBody)
		if err != nil {
			httputil.JSONError(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		if input.OrganizationID == "" {
			httputil.JSONError(w, "orgID is required", http.StatusBadRequest)
			return
		}

		workflowID := zitadelsync.TenantWorkflowIDPrefix + input.OrganizationID
		_, err = temporalClient.ExecuteWorkflow(
			r.Context(),
			client.StartWorkflowOptions{
				ID:        workflowID,
				TaskQueue: taskQueue,
			},
			zitadelsync.TenantSyncWorkflow,
			zitadelsync.TenantSyncWorkflowInput{
				TenantID: input.OrganizationID,
			},
		)
		if err != nil {
			httputil.JSONError(w, "failed to start workflow", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
