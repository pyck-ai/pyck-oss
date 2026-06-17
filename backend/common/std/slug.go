package std

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/gosimple/slug"
)

var (
	SlugRegex = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	// separatorRunRegex collapses any run of underscores and hyphens into a
	// single hyphen. gosimple/slug preserves underscores in its output, but
	// pyck's SlugRegex disallows them — so we post-process. Underscores have
	// always been valid input separators in pyck (the Shopify integration
	// passes names like "shopify_item_v1" expecting "shopify-item-v1").
	separatorRunRegex = regexp.MustCompile(`[_-]+`)
)

func IsValidSlug(s string) bool {
	return SlugRegex.MatchString(s)
}

// ToSlug converts an arbitrary string to a URL-safe, ASCII-only slug matching
// SlugRegex. Non-ASCII characters are transliterated (e.g. "ράφι" → "raphi",
// "café" → "cafe"). Underscores in the input are treated as separators and
// converted to hyphens. Inputs that yield no transliterable runes (pure
// emoji or symbols) fall back to the first 8 hex chars of the SHA-256 digest
// so distinct inputs always produce distinct slugs — preserving the
// (tenant_id, slug) unique-index contract.
func ToSlug(s string) string {
	if s == "" {
		return ""
	}
	out := slug.Make(s)
	out = separatorRunRegex.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-")
	if out == "" {
		h := sha256.Sum256([]byte(s))
		out = hex.EncodeToString(h[:4])
	}
	return out
}
