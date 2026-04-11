package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"

	"github.com/caarlos0/env/v11"
)

var (
	ErrLoadConfig = errors.New("failed to load configuration")
)

const (
	DotenvFilepath = "../../.env"
)

func Load[T any](ctx context.Context) (context.Context, T, error) {
	var config T

	envpath, err := filepath.Abs(DotenvFilepath)
	if err != nil {
		return ctx, config, fmt.Errorf("%w: %w", ErrLoadConfig, err)
	}

	if err := godotenv.Load(envpath); err != nil && !os.IsNotExist(err) {
		return ctx, config, fmt.Errorf("%w: %w", ErrLoadConfig, err)
	}

	config, err = env.ParseAs[T]()
	if err != nil {
		return ctx, config, fmt.Errorf("%w: %w", ErrLoadConfig, err)
	}

	ctx = Context(ctx, &config)

	return ctx, config, nil
}
