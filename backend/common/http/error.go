package http

import (
	"encoding/json"
	nethttp "net/http"

	"github.com/rs/zerolog/log"
)

type errorResponse struct {
	Error string `json:"error"`
}

// JSONError replies to the request with the specified error message and HTTP code.
// It sets the Content-Type header to application/json.
func JSONError(w nethttp.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(errorResponse{Error: message}); err != nil {
		log.Error().Err(err).Msg("failed to write JSON error response")
	}
}
