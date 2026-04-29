package oauth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/rs/zerolog/log"
)

func AccessTokenHandler(clientID, clientSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httputil.JSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			httputil.JSONError(w, "error while reading body", http.StatusUnprocessableEntity)
			return
		}

		input, err := std.UnmarshalJson[accessTokenInput](reqBody)
		if err != nil {
			httputil.JSONError(w, "unable to unmarshal", http.StatusUnprocessableEntity)
			return
		}

		if input.Code == "" {
			httputil.JSONError(w, "missing code", http.StatusBadRequest)
			return
		}
		log.Info().Msgf("processing code: %s", input.Code)

		ghTokenInput := githubTokenInput{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Code:         input.Code,
		}
		ghTokenBodyBytes, err := std.MarshalJson(ghTokenInput)
		if err != nil {
			httputil.JSONError(w, "unable to marshal request", http.StatusInternalServerError)
			return
		}

		ghTokenReq, err := http.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(ghTokenBodyBytes))
		if err != nil {
			httputil.JSONError(w, "unable to request github", http.StatusInternalServerError)
			return
		}

		ghTokenReq.Header.Set("Accept", "application/json")
		ghTokenReq.Header.Set("Content-Type", "application/json")

		ghResp, err := http.DefaultClient.Do(ghTokenReq)
		if err != nil {
			log.Error().Err(err).Msg("github token exchange error")
			httputil.JSONError(w, "github token exchange error", http.StatusInternalServerError)
			return
		}
		defer func() {
			if err := ghResp.Body.Close(); err != nil {
				log.Error().Err(err).Msg("failed to close github response body")
			}
		}()

		ghRespBytes, err := io.ReadAll(ghResp.Body)
		if err != nil {
			httputil.JSONError(w, "unable to decode github response", http.StatusInternalServerError)
			return
		}

		ghTokenResp, err := std.UnmarshalJson[githubTokenResponse](ghRespBytes)
		if err != nil {
			httputil.JSONError(w, "unable to unmarshal github response", http.StatusInternalServerError)
			return
		}

		if ghTokenResp.Error != "" {
			httputil.JSONError(w, ghTokenResp.Error, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		handlerResp := map[string]string{
			"access_token": ghTokenResp.AccessToken,
		}

		if err := json.NewEncoder(w).Encode(handlerResp); err != nil {
			httputil.JSONError(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
