package std

import (
	"regexp"
	"strings"
)

var (
	nonAlphaNumericRegex = regexp.MustCompile(`[^a-z0-9\s-]`)
	spaceAndHyphenRegex  = regexp.MustCompile(`[\s-]+`)
	SlugRegex            = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

func IsValidSlug(slug string) bool {
	return SlugRegex.MatchString(slug)
}

func ToSlug(str string) string {
	str = strings.TrimSpace(str)
	str = strings.ToLower(str)
	str = nonAlphaNumericRegex.ReplaceAllString(str, "-")
	str = spaceAndHyphenRegex.ReplaceAllString(str, "-")
	str = strings.Trim(str, "-")
	return str
}
