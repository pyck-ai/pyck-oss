package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	_ "embed"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

//go:embed templates/example.yml.tmpl
var ExampleTemplate string

//go:embed templates/test.yml.tmpl
var TestTemplate string

//go:embed templates/folder.yml.tmpl
var folderTemplate string

// templateData holds all fields passed to a Bruno YAML template.
type templateData struct {
	Timestamp     string
	OperationName string
	ServiceName   string
	// EnvServiceName is the env-var suffix for this service, used to build
	// the Bruno URL variable name: env_baseurl_<EnvServiceName>_query.
	// Derived from ServiceName via the embedded brunogen.config.yaml overrides.
	EnvServiceName   string
	GraphQLContent   string
	ScenarioName     string
	Description      string
	Seq              int
	Vars             map[string]any
	Tests            []RenderedTest
	Extracts         []RenderedExtract
	UseBody          bool
	UseVars          bool
	ExtractsNeedBody bool
	ExtractsNeedVars bool
	HasPlaceholders  bool
	// SourceFile is the repo-relative path of the fixture that drove generation.
	// Empty for operations generated without a fixture.
	SourceFile string
	// SkipChecks drives the optional before-request skip script.
	// Each RenderedExpect becomes: try { expect(...); bru.runner.skipRequest(); } catch(e) {}
	// The request is skipped when at least one assertion passes.
	SkipChecks []RenderedTest
}

// hasPlaceholders recursively checks whether any value in the map (or nested
// maps/slices) is a $placeholder directive.
func hasPlaceholders(v any) bool {
	switch val := v.(type) {
	case map[string]any:
		if _, ok := val["$placeholder"]; ok {
			return true
		}
		for _, child := range val {
			if hasPlaceholders(child) {
				return true
			}
		}
	case []any:
		for _, item := range val {
			if hasPlaceholders(item) {
				return true
			}
		}
	}
	return false
}

// RenderFile renders a Bruno YAML template and returns the result as a string.
// varNS namespaces cross-step collection variable names.
// description is written to the /docs property when non-empty.
// skipChecks drives the optional before-request skip script.
func RenderFile(
	tmplStr string,
	cfg types.Config,
	op types.Operation,
	scenarioName string,
	description string,
	vars map[string]any,
	tests []RenderedTest,
	extracts []RenderedExtract,
	useBody, useReqVars bool,
	seq int,
	varNS string,
	skipChecks []RenderedTest,
	sourceFile string,
) (string, error) {
	varHasPlaceholders := false
	for _, v := range vars {
		if hasPlaceholders(v) {
			varHasPlaceholders = true
		}
	}

	extractsNeedBody, extractsNeedVars := false, false
	for _, e := range extracts {
		if e.NeedsBody {
			extractsNeedBody = true
		}
		if e.NeedsVars {
			extractsNeedVars = true
		}
	}

	data := templateData{
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		OperationName:    addSpacesToCamelCase(op.Name),
		ServiceName:      cfg.ServiceName,
		EnvServiceName:   envServiceName(cfg.ServiceName),
		GraphQLContent:   strings.TrimSpace(op.Content),
		ScenarioName:     scenarioName,
		Description:      description,
		Seq:              seq,
		Vars:             vars,
		Tests:            tests,
		Extracts:         extracts,
		UseBody:          useBody,
		UseVars:          useReqVars,
		ExtractsNeedBody: extractsNeedBody,
		ExtractsNeedVars: extractsNeedVars,
		HasPlaceholders:  varHasPlaceholders,
		SourceFile:       sourceFile,
		SkipChecks:       skipChecks,
	}

	tmpl, err := template.New("bruno").Funcs(template.FuncMap{
		"marshalVars": func(v map[string]any) (string, error) {
			return MarshalVarsFor(v, varNS)
		},
		"indent": func(spaces int, s string) string {
			pad := strings.Repeat(" ", spaces)
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if strings.TrimSpace(line) != "" {
					lines[i] = pad + line
				}
			}
			return strings.Join(lines, "\n")
		},
	}).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}
	return buf.String(), nil
}

// WriteFolderYML writes a folder.yml file into dir.
// description is written to the /docs property when non-empty.
// Skips silently if it already exists; does nothing in dry-run mode.
func WriteFolderYML(dir, name, description string, cfg types.Config) error {
	if cfg.DryRun {
		return nil
	}
	path := filepath.Join(dir, "folder.yml")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	tmpl, err := template.New("folder").Parse(folderTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse folder template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, struct {
		Name        string
		Description string
		Seq         int
	}{Name: name, Description: description, Seq: 1}); err != nil {
		return fmt.Errorf("failed to execute folder template: %w", err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("failed to write folder.yml: %w", err)
	}
	LogVerbosef(cfg.Verbose, "Generated: %s", path)
	return nil
}

// RemoveGeneratedFiles deletes all *_gen.yml files in dir.
func RemoveGeneratedFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_gen.yml") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteFile writes content to filename, or logs a dry-run notice.
func WriteFile(filename, content string, cfg types.Config) error {
	if cfg.DryRun {
		LogVerbosef(cfg.Verbose, "[DRY-RUN] Would write: %s", filename)
		if cfg.Verbose {
			fmt.Println(content)
		}
		return nil
	}
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		return fmt.Errorf("failed to write Bruno file: %w", err)
	}
	LogVerbosef(cfg.Verbose, "Generated: %s", filename)
	return nil
}
