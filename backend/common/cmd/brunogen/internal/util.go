package gen

import (
	"log"
	"strings"
	"unicode"
)

// LogVerbosef logs a formatted message when verbose is true.
func LogVerbosef(verbose bool, format string, args ...any) {
	if verbose {
		log.Printf(format, args...)
	}
}

// addSpacesToCamelCase inserts a space before each uppercase letter.
// e.g. "CheckInUserDevice" → "Check In User Device".
func addSpacesToCamelCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Slugify converts a name to a lowercase hyphen-separated slug.
// e.g. "Create and verify file" → "create-and-verify-file".
func Slugify(name string) string {
	var b strings.Builder
	prevHyphen := true
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// StripServicePrefix removes the service name prefix from a resource name.
// e.g. StripServicePrefix("inventoryitem", "inventory") → "item".
func StripServicePrefix(resourceName, serviceName string) string {
	lower := strings.ToLower(resourceName)
	prefix := strings.ToLower(strings.ReplaceAll(serviceName, "-", ""))
	if stripped, ok := strings.CutPrefix(lower, prefix); ok && stripped != "" {
		return stripped
	}
	return lower
}

// Singularize converts simple plural English words to singular form.
func Singularize(word string) string {
	lower := strings.ToLower(word)
	if strings.HasSuffix(lower, "ies") && len(lower) > 3 {
		return lower[:len(lower)-3] + "y"
	}
	if strings.HasSuffix(lower, "s") && len(lower) > 1 {
		return lower[:len(lower)-1]
	}
	return lower
}

// ExtractResourceName derives a singular resource name from a GraphQL operation
// name by stripping common verb prefixes and singularizing.
// e.g. "createInventoryItems" → "inventoryitem".
func ExtractResourceName(operationName string) string {
	prefixes := []string{
		"create", "get", "update", "delete", "execute",
		"list", "add", "remove", "set", "clear",
	}
	lower := strings.ToLower(operationName)
	for _, prefix := range prefixes {
		if rest, ok := strings.CutPrefix(lower, prefix); ok {
			lower = rest
			break
		}
	}
	return Singularize(lower)
}
