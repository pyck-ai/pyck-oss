package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GQLResult represents a GraphQL response with data and errors.
type GQLResult[T any] struct {
	Data   T
	Errors []GQLError
}

// GQLError represents a GraphQL error.
type GQLError struct {
	Message string
	Path    []string
}

// TemplateRenderer renders a GraphQL query template with the given data.
type TemplateRenderer interface {
	RenderTemplate(data any) string
}

// Executor defines the interface for executing GraphQL queries in tests.
type Executor[E EntTestClient] interface {
	SendQuery(t *testing.T, ctx context.Context, tpl TemplateRenderer, args any) (func(), *http.Response, error)
	ReadResponse(t *testing.T, resp *http.Response, result any) error
	Testing() *testing.T
}

// Testing returns the testing.T instance.
func (te *TestEnvironment[E]) Testing() *testing.T {
	return te.T
}

// Exec executes a GraphQL query and returns the parsed response.
func Exec[T any, E EntTestClient](te Executor[E], ctx context.Context, tpl TemplateRenderer, args any) GQLResult[T] {
	t := te.Testing()
	t.Helper()

	closeResp, resp, err := te.SendQuery(t, ctx, tpl, args)
	defer closeResp()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result GQLResult[T]
	require.NoError(t, te.ReadResponse(t, resp, &result))
	return result
}

// ExecOK executes a GraphQL query and asserts no errors.
func ExecOK[T any, E EntTestClient](te Executor[E], ctx context.Context, tpl TemplateRenderer, args any) T {
	t := te.Testing()
	t.Helper()
	result := Exec[T, E](te, ctx, tpl, args)
	require.Empty(t, result.Errors, "unexpected GraphQL errors: %v", result.Errors)
	return result.Data
}

// ExecErr executes a GraphQL query and asserts an error containing the message.
func ExecErr[E EntTestClient](te Executor[E], ctx context.Context, tpl TemplateRenderer, args any, wantErrContains string) {
	t := te.Testing()
	t.Helper()
	var result GQLResult[json.RawMessage]
	closeResp, resp, err := te.SendQuery(t, ctx, tpl, args)
	defer closeResp()
	require.NoError(t, err)
	require.NoError(t, te.ReadResponse(t, resp, &result))
	require.NotEmpty(t, result.Errors, "expected error containing %q", wantErrContains)
	if wantErrContains != "" {
		assert.Contains(t, result.Errors[0].Message, wantErrContains)
	}
}
