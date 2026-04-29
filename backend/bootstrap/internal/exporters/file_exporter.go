package exporters

import (
	"context"
	"fmt"
	"os"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// FileExporter exports credentials to a file
type FileExporter struct {
	keyPath string
}

// NewFileExporter creates a new FileExporter
func NewFileExporter(keyPath string) *FileExporter {
	return &FileExporter{
		keyPath: keyPath,
	}
}

// Exists checks if the credential file already exists and is non-empty.
func (e *FileExporter) Exists(_ context.Context, export Export) (bool, error) {
	keyFile, err := safePath(e.keyPath, export.File)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Size() > 0, nil
}

// Export writes credentials to a file
func (e *FileExporter) Export(ctx context.Context, credentials string, export Export) error {
	logger := log.ForContext(ctx)
	keyFile, err := safePath(e.keyPath, export.File)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, []byte(credentials), 0o644); err != nil {
		return fmt.Errorf("failed to write credentials: %w", err)
	}
	logger.Debug().Str("file", export.File).Msg("Saved credentials to file")
	return nil
}
