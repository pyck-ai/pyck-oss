package handlers

import (
	"context"
	"net/http"
)

type HealthCheckInterface interface {
	HealthCheck(ctx context.Context) error
}

func NewHealthCheckHandler(checkComponents ...HealthCheckInterface) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errorsHappened := []error{}
		for _, component := range checkComponents {
			err := component.HealthCheck(r.Context())
			if err != nil {
				errorsHappened = append(errorsHappened, err)
			}
		}

		if len(errorsHappened) == 0 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}

		errorMessage := "Health check failed:"
		for _, err := range errorsHappened {
			errorMessage += "\n\t" + err.Error()
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(errorMessage))
	})
}
