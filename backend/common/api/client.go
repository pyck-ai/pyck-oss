package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"entgo.io/contrib/entgql"
	"github.com/google/uuid"
	"github.com/gqlgo/gqlgenc/clientv2"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/request"
)

type Config struct {
	envconfig.GatewayConfig

	APIToken    string `env:"PYCK_API_TOKEN,notEmpty"`
	APITenantID string `env:"PYCK_API_TENANT_ID,notEmpty"`
}

func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	var errResp *clientv2.ErrorResponse
	if errors.As(err, &errResp) {
		if errResp.NetworkError != nil {
			switch errResp.NetworkError.Code {
			case http.StatusConflict, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
				return false
			default:
				return true
			}
		}
	}

	return true
}

func AuthnMiddleware(cfg Config) clientv2.RequestInterceptor {
	return func(ctx context.Context, r *http.Request, gqlInfo *clientv2.GQLRequestInfo, res any, next clientv2.RequestInterceptorFunc) error {
		req := request.ForContext(ctx)
		user := req.User()

		if cfg.APIToken != "" {
			r.Header.Set("Authorization", "Bearer "+cfg.APIToken)
		} else if user.Token != "" {
			r.Header.Set("Authorization", "Bearer "+user.Token)

			for _, tid := range req.TenantIDs() {
				r.Header.Add("X-Pyck-Tenant-Id", tid.String())
			}
		}

		if cfg.APITenantID != "" {
			r.Header.Add("X-Pyck-Tenant-Id", cfg.APITenantID)
		}

		return next(ctx, r, gqlInfo, res)
	}
}

type ClientFactory[T any] func(cli clientv2.HttpClient, baseURL string, options *clientv2.Options, interceptors ...clientv2.RequestInterceptor) T

func NewDefaultClient[T any](ctx context.Context, factory ClientFactory[T]) (T, error) {
	var zero T

	_, cfg, err := env.Load[Config](ctx)
	if err != nil {
		return zero, err
	}

	clientOptions := clientv2.Options{
		ParseDataAlongWithErrors: false,
	}

	return factory(http.DefaultClient, cfg.GatewayUrl, &clientOptions, AuthnMiddleware(cfg)), nil
}

func ConvertStringToCursor(cursorStr string) (*entgql.Cursor[uuid.UUID], error) {
	var cursor entgql.Cursor[uuid.UUID]

	// Ent's UnmarshalGQL expects the raw string and decodes it internally
	if err := cursor.UnmarshalGQL(cursorStr); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cursor: %w", err)
	}

	return &cursor, nil
}

func ConvertCursorToString(id uuid.UUID) (string, error) {
	// 1. Create the cursor object with the ID
	cursor := entgql.Cursor[uuid.UUID]{
		ID: id,
		// Value: optional, used if sorting by a non-ID field
	}

	// 2. Marshal to a buffer (this performs the Gob + Base64 encoding)
	var buf bytes.Buffer
	cursor.MarshalGQL(&buf)

	// 3. The buffer now contains the quoted string, e.g., "gaFpx..."
	// We trim the quotes if we want just the raw token
	out := buf.String()
	if len(out) >= 2 && out[0] == '"' {
		out = out[1 : len(out)-1]
	}

	return out, nil
}
