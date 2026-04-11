package mocks

import (
	"context"
	"net/http"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/stretchr/testify/mock"
)

type MockAuthProvider struct {
	mock.Mock
}

var _ authn.Authenticator = (*MockAuthProvider)(nil)

func (m *MockAuthProvider) Authorize() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockAuthProvider) GetAccessToken() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockAuthProvider) Authenticate(ctx context.Context, token string) (authn.User, error) {
	args := m.Called(ctx, token)
	return args.Get(0).(authn.User), args.Error(1)
}

func (m *MockAuthProvider) HTTPMiddleware() func(http.Handler) http.Handler {
	args := m.Called()
	handler, _ := args.Get(0).(func(http.Handler) http.Handler)
	return handler
}

func HTTPMiddleware(user *authn.User) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := authn.Context(r.Context(), user)

			h.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
