package internal

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/types"
)

// Capitalize returns s with the first letter uppercased.
// Used to convert GraphQL field names to Go method names
// (e.g., "createInventoryRepository" → "CreateInventoryRepository").
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// DeriveInputType extracts the Input parameter type from a mutation method.
func DeriveInputType(methods map[string]types.ClientMethod, methodName string) string {
	m, ok := methods[methodName]
	if !ok {
		return ""
	}
	for _, p := range m.Params {
		if strings.EqualFold(p.Name, "input") {
			return strings.TrimPrefix(p.Type, "*")
		}
	}
	return ""
}
