package gqlserver

import (
	"context"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// presentError hides Postgres integrity-constraint violations behind the
// generic message of db.HideConstraintViolation, which also logs the raw
// error. Every other error is presented as gqlgen would. The original error
// stays wrapped, so the transaction middleware can still classify it.
func presentError(ctx context.Context, err error) *gqlerror.Error {
	gqlErr := graphql.DefaultErrorPresenter(ctx, err)
	if message, ok := db.HideConstraintViolation(ctx, err); ok {
		gqlErr.Message = message
	}
	return gqlErr
}
