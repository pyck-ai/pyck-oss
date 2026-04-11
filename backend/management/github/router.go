package github

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/pyck-ai/pyck/backend/management/github/oauth"
	"github.com/rs/cors"
)

func Router(clientID, clientSecret string) http.Handler {
	r := chi.NewRouter()

	c := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{http.MethodPost},
		AllowedHeaders: []string{"Content-Type"},
	})
	r.Use(c.Handler)

	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self';")
			next.ServeHTTP(w, r)
		})
	})

	r.Post("/oauth/access_token", oauth.AccessTokenHandler(clientID, clientSecret))

	return r
}
