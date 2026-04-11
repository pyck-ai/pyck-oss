package api

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

// ParseGraphQL reads an apigen_gen.graphql file and returns all operations.
func ParseGraphQL(filePath string) (*types.Operations, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read GraphQL file: %w", err)
	}

	ops := &types.Operations{
		Queries:   make([]types.Operation, 0),
		Mutations: make([]types.Operation, 0),
	}

	for _, opText := range extractOperations(string(content)) {
		op, err := parseOperation(opText)
		if err != nil {
			continue
		}
		switch op.Type {
		case "query":
			ops.Queries = append(ops.Queries, op)
		case "mutation":
			ops.Mutations = append(ops.Mutations, op)
		}
	}

	if len(ops.Queries) == 0 && len(ops.Mutations) == 0 {
		return nil, types.ErrNoOperations
	}
	return ops, nil
}

// DetectServiceName infers the service name from the GraphQL file path.
// Expected pattern: backend/<service>/api/graph/apigen_gen.graphql.
func DetectServiceName(graphqlPath string) (string, error) {
	abs, err := filepath.Abs(graphqlPath)
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i, part := range parts {
		if part == "backend" && i+1 < len(parts) {
			return parts[i+1], nil
		}
	}
	return "", types.ErrInvalidServicePath
}

// DetectServiceNameFromDir infers the service name from a directory path.
// Expected pattern: .../backend/<service>/...
func DetectServiceNameFromDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i, part := range parts {
		if part == "backend" && i+1 < len(parts) {
			return parts[i+1], nil
		}
	}
	return "", types.ErrInvalidServicePath
}

// ParseQualifiedOperation splits a "service.operationName" string into its
// service and operation components. Returns ("", s) if s contains no dot,
// indicating an unqualified operation name.
func ParseQualifiedOperation(s string) (service, opName string) {
	if before, after, ok := strings.Cut(s, "."); ok {
		return before, after
	}
	return "", s
}

func extractOperations(content string) []string {
	var operations []string
	var current strings.Builder
	var depth int
	var inOperation bool

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inOperation {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.HasPrefix(trimmed, "query ") || strings.HasPrefix(trimmed, "mutation ") {
				inOperation = true
				current.Reset()
			}
		}
		if inOperation {
			current.WriteString(line)
			current.WriteString("\n")
			for _, ch := range line {
				switch ch {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						operations = append(operations, current.String())
						inOperation = false
						current.Reset()
					}
				}
			}
		}
	}
	return operations
}

var operationHeaderRe = regexp.MustCompile(`^(query|mutation)\s+(\w+)(?:\s*\((.*?)\))?\s*\{`)

func parseOperation(opText string) (types.Operation, error) {
	m := operationHeaderRe.FindStringSubmatch(strings.TrimSpace(opText))
	if len(m) < 3 {
		return types.Operation{}, fmt.Errorf("%w: cannot extract operation header", types.ErrInvalidGraphQLFile)
	}
	op := types.Operation{
		Type:       m[1],
		Name:       m[2],
		Content:    strings.TrimSpace(opText),
		Variables:  make([]types.Variable, 0),
		ReturnType: extractReturnType(opText),
	}
	if len(m) > 3 && m[3] != "" {
		op.Variables = parseVariables(m[3])
	}
	return op, nil
}

func parseVariables(variablesStr string) []types.Variable {
	parts := splitByComma(variablesStr)
	vars := make([]types.Variable, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, ":") {
			continue
		}
		splits := strings.SplitN(part, ":", 2)
		vars = append(vars, types.Variable{
			Name: strings.TrimSpace(strings.TrimPrefix(splits[0], "$")),
			Type: strings.TrimSpace(splits[1]),
		})
	}
	return vars
}

func splitByComma(s string) []string {
	var parts []string
	var current strings.Builder
	var depth int
	for _, ch := range s {
		if ch == ',' && depth == 0 {
			parts = append(parts, current.String())
			current.Reset()
		} else {
			current.WriteRune(ch)
			switch ch {
			case '[', '(':
				depth++
			case ']', ')':
				depth--
			}
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

var fieldNameRe = regexp.MustCompile(`^\s*(\w+)\s*(?:\([^)]*\))?\s*\{`)

func extractReturnType(opText string) string {
	var depth int
	var inside bool
	for _, line := range strings.Split(opText, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, ch := range trimmed {
			switch ch {
			case '{':
				depth++
				if depth == 1 {
					inside = true
				}
			case '}':
				depth--
			}
		}
		if inside && depth == 1 {
			if m := fieldNameRe.FindStringSubmatch(trimmed); len(m) > 1 {
				return m[1]
			}
		}
		if inside && depth < 1 {
			break
		}
	}
	return ""
}
