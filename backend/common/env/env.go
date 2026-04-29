package env

import (
	"context"
	"errors"
	"fmt"

	"github.com/caarlos0/env/v11"
)

// ErrLoadConfig is returned when the configuration cannot be loaded.
var ErrLoadConfig = errors.New("load configuration")

// Load reads .env files from DefaultDir(), then parses environment
// variables into the provided config type T.
func Load[T any](ctx context.Context) (context.Context, T, error) {
	var config T

	config, err := env.ParseAs[T]()
	if err != nil {
		return ctx, config, fmt.Errorf("%w: %w", ErrLoadConfig, err)
	}

	ctx = Context(ctx, &config)

	return ctx, config, nil
}
