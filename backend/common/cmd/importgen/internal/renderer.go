package internal

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"text/template"

	_ "embed"

	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/types"
)

const outputFile = "import_gen.go"

//go:embed templates/import_gen.go.tmpl
var registryTemplate string

// WriteRegistryFile renders the template and writes formatted output.
func WriteRegistryFile(data types.TemplateData, outputDir string) error {
	tmpl, err := template.New("registry").Parse(registryTemplate)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		outPath := filepath.Join(outputDir, outputFile)
		if werr := os.WriteFile(outPath+".broken", buf.Bytes(), 0o600); werr != nil {
			return fmt.Errorf("format generated code: %w", err)
		}
		return fmt.Errorf("format generated code: %w (unformatted written to %s.broken)", err, outPath)
	}

	outPath := filepath.Join(outputDir, outputFile)
	if err := os.WriteFile(outPath, formatted, 0o600); err != nil {
		return err
	}

	return nil
}
