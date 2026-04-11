package resolvers

import "regexp"

var (
	passwordUppercaseRegex   = regexp.MustCompile(`[A-Z]`)
	passwordLowercaseRegex   = regexp.MustCompile(`[a-z]`)
	passwordNumberRegex      = regexp.MustCompile(`\d`)
	passwordSpecialCharRegex = regexp.MustCompile(`[\W_]`)
)

func isValidPassword(password string) bool {
	minMaxLen := len(password) >= 8 && len(password) <= 70
	return passwordUppercaseRegex.MatchString(password) &&
		passwordLowercaseRegex.MatchString(password) &&
		passwordNumberRegex.MatchString(password) &&
		passwordSpecialCharRegex.MatchString(password) &&
		minMaxLen
}
