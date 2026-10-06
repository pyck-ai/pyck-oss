package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestResolveServices(t *testing.T) {
	t.Parallel()

	// Map iteration order of cfg.Services is random, so declared is unsorted.
	declared := []string{"worker", "history", "frontend", "matching"}
	withInternal := append([]string{"internal-frontend"}, declared...)

	tests := []struct {
		name       string
		service    []string
		legacy     string
		declared   []string
		want       []string
		wantLegacy bool
		wantErr    error
	}{
		{
			name:     "nothing set uses every config service, sorted",
			declared: withInternal,
			want:     []string{"frontend", "history", "internal-frontend", "matching", "worker"},
		},
		{
			name:     "single --service",
			service:  []string{"history"},
			declared: declared,
			want:     []string{"history"},
		},
		{
			name:     "several --service values are sorted",
			service:  []string{"worker", "frontend"},
			declared: declared,
			want:     []string{"frontend", "worker"},
		},
		{
			// urfave/cli splits TEMPORAL_SERVICES on "," into the same slice
			// a repeated --service would produce.
			name:     "TEMPORAL_SERVICES env value",
			service:  []string{"matching", " history "},
			declared: declared,
			want:     []string{"history", "matching"},
		},
		{
			name:       "deprecated --services",
			legacy:     "frontend,history",
			declared:   declared,
			want:       []string{"frontend", "history"},
			wantLegacy: true,
		},
		{
			name:       "deprecated --services is trimmed",
			legacy:     " worker , matching ,",
			declared:   declared,
			want:       []string{"matching", "worker"},
			wantLegacy: true,
		},
		{
			name:     "--service wins over deprecated --services",
			service:  []string{"history"},
			legacy:   "frontend",
			declared: declared,
			want:     []string{"history"},
		},
		{
			name:     "blank --service falls back to every config service",
			service:  []string{""},
			declared: declared,
			want:     []string{"frontend", "history", "matching", "worker"},
		},
		{
			name:     "duplicates are removed",
			service:  []string{"history", "frontend", "history"},
			declared: declared,
			want:     []string{"frontend", "history"},
		},
		{
			name:     "unknown name",
			service:  []string{"history", "bogus"},
			declared: declared,
			wantErr:  ErrUnknownService,
		},
		{
			name:       "unknown name in deprecated --services",
			legacy:     "bogus",
			declared:   declared,
			wantErr:    ErrUnknownService,
			wantLegacy: true,
		},
		{
			name:     "valid name missing from the config",
			service:  []string{"internal-frontend"},
			declared: declared,
			wantErr:  ErrServiceNotInConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, usedLegacy, err := resolveServices(tt.service, tt.legacy, tt.declared)
			require.Equal(t, tt.wantLegacy, usedLegacy)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// The flags must feed resolveServices only when they were set: the flag
// default must not mask the "every config service" fallback, and
// TEMPORAL_SERVICES must be read like --service.
func TestServiceFlagSources(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	tests := []struct {
		name        string
		args        []string
		env         string
		wantSet     bool
		wantService []string
		wantLegacy  string
	}{
		{name: "unset", args: []string{"start"}},
		{name: "flag", args: []string{"start", "--service=history"}, wantSet: true, wantService: []string{"history"}},
		{name: "alias", args: []string{"start", "--svc", "worker"}, wantSet: true, wantService: []string{"worker"}},
		{name: "env", args: []string{"start"}, env: "frontend,matching", wantSet: true, wantService: []string{"frontend", "matching"}},
		{name: "deprecated", args: []string{"start", "--services", "frontend,history"}, wantLegacy: "frontend,history"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("TEMPORAL_SERVICES", tt.env)
			}

			app := buildCLI()
			start := app.Commands[0]

			var (
				gotSet     bool
				gotService []string
				gotLegacy  string
			)

			start.Action = func(_ context.Context, cmd *cli.Command) error {
				gotSet = cmd.IsSet("service")
				gotService, gotLegacy = serviceFlagValues(cmd)

				return nil
			}

			require.NoError(t, app.Run(context.Background(), append([]string{"temporal"}, tt.args...)))
			require.Equal(t, tt.wantSet, gotSet)
			require.Equal(t, tt.wantService, gotService)
			require.Equal(t, tt.wantLegacy, gotLegacy)
		})
	}
}
