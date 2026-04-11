package mocks

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/stretchr/testify/mock"
)

// MockZitadelClient is a mock implementation of zitadel.Client for testing
type MockZitadelClient struct {
	mock.Mock
}

// IntrospectToken mocks the IntrospectToken method
func (m *MockZitadelClient) IntrospectToken(ctx context.Context, token string) (*zitadel.IntrospectionResult, error) {
	args := m.Called(ctx, token)
	result, _ := args.Get(0).(*zitadel.IntrospectionResult)
	return result, args.Error(1)
}

// Close mocks the Close method
func (m *MockZitadelClient) Close() {
	m.Called()
}

// NewMockZitadelClient creates a new mock Zitadel client
func NewMockZitadelClient() *MockZitadelClient {
	return &MockZitadelClient{}
}
