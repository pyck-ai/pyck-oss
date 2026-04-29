package exporters

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafePath(t *testing.T) {
	t.Parallel()

	base := "/data/keys"

	tests := []struct {
		name    string
		file    string
		want    string
		wantErr bool
	}{
		{
			name: "simple filename",
			file: "service-key.json",
			want: "/data/keys/service-key.json",
		},
		{
			name: "subdirectory",
			file: "subdir/key.json",
			want: "/data/keys/subdir/key.json",
		},
		{
			name:    "parent traversal",
			file:    "../my-env",
			wantErr: true,
		},
		{
			name:    "deep traversal",
			file:    "../../etc/shadow",
			wantErr: true,
		},
		{
			name: "absolute path stays inside",
			file: "/etc/passwd",
			want: "/data/keys/etc/passwd", // filepath.Join strips leading slash
		},
		{
			name:    "dot-dot in middle",
			file:    "subdir/../../escape",
			wantErr: true,
		},
		{
			name: "dot-dot that stays inside",
			file: "subdir/../key.json",
			want: "/data/keys/key.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := safePath(base, tt.file)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "escapes base directory")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, filepath.Clean(tt.want), got)
		})
	}
}
