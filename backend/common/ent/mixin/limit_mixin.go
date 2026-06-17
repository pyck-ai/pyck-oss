package mixin

import (
	"context"
	"fmt"
	"reflect"

	"entgo.io/ent"
	"entgo.io/ent/schema/mixin"
	"github.com/rs/zerolog/log"
)

var (
	// Limit is the default limit for the query.
	Limit = 200
)

// LimitMixin limits the number of results returned by a query.
type LimitMixin struct {
	mixin.Schema
}

func (LimitMixin) Interceptors() []ent.Interceptor {
	return []ent.Interceptor{
		LimitInterceptor(),
	}
}

func LimitInterceptor() ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			ctxLimit := ent.QueryFromContext(ctx).Limit
			implicitLimit := false

			if ctxLimit == nil {
				c := ent.QueryFromContext(ctx)
				c.Limit = &Limit
				ctx = ent.NewQueryContext(ctx, c)
				implicitLimit = true
			} else if *ctxLimit > Limit+1 {
				// +1 leaves room for Relay's lookahead row (paginateLimit), so the
				// caller-visible budget stays at Limit.
				return nil, fmt.Errorf("%w: the maximum accepted limit is %d", ErrLimitExceeded, Limit)
			}

			result, err := next.Query(ctx, q)
			if err != nil {
				return result, err
			}

			// Warn when the implicit default limit was applied and the result
			// set is exactly at the cap — this strongly suggests rows were
			// silently dropped.
			if implicitLimit {
				if rv := reflect.ValueOf(result); rv.Kind() == reflect.Slice && rv.Len() == Limit {
					log.Ctx(ctx).Warn().
						Int("limit", Limit).
						Msg("query returned exactly the default limit — results may be truncated; consider adding explicit pagination")
				}
			}

			return result, nil
		})
	})
}
