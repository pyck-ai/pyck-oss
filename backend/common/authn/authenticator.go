package authn

import (
	"context"
	"net/http"
)

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (User, error)
	HTTPMiddleware() func(http.Handler) http.Handler
}
