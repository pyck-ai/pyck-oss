package hooks

import (
	"context"
	"time"

	"entgo.io/ent"
	"github.com/pyck-ai/pyck/backend/common/log"
)

func LogMutation(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
		start := time.Now()
		defer func() {
			log.ForContext(ctx).Info().
				Str("op", m.Op().String()).
				Str("type", m.Type()).
				Dur("duration", time.Since(start)).
				Msg("database mutation completed")
		}()
		return next.Mutate(ctx, m)
	})
}
