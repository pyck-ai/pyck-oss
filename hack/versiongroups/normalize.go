package main

import "regexp"

// Normalizer is applied to a captured raw string to derive the value used
// for comparison, and knows how to splice a --fix target back into the
// original raw string without disturbing any part of it the normalizer
// itself did not consider significant (e.g. a "-tctl-1.18.4-cli-1.5.0"
// suffix on a Temporal admin-tools tag, or a "-alpine" suffix on a golang
// Docker tag).
type Normalizer interface {
	// Normalize reduces raw to its comparable form. ok is false when raw
	// is not a value this normalizer understands (e.g. a floating tag
	// like ":latest" under leading-semver) — such occurrences are
	// skipped entirely: not a match, not a violation.
	Normalize(raw string) (value string, ok bool)
	// FixValue returns the replacement for raw's captured span, given
	// target is the (already-normalized) source-of-truth value.
	FixValue(raw, target string) string
}

const (
	normalizeLeadingSemver = "leading-semver"
	normalizeMajorMinor    = "major-minor"
)

// normalizers is the closed set of supported `normalize:` values. "" (no
// normalize: key) compares captured strings exactly as-is. This set is
// deliberately closed — see the package doc comment in main.go for why.
var normalizers = map[string]Normalizer{
	"":                     exactNormalizer{},
	normalizeLeadingSemver: leadingSemverNormalizer{},
	normalizeMajorMinor:    majorMinorNormalizer{},
}

// exactNormalizer is used when no `normalize:` is set: the captured group
// IS the value, compared byte-for-byte.
type exactNormalizer struct{}

func (exactNormalizer) Normalize(raw string) (string, bool) { return raw, true }
func (exactNormalizer) FixValue(_, target string) string    { return target }

// leadingSemverNormalizer extracts the leading X.Y.Z from a captured
// string, tolerating an optional "v" prefix and ignoring any trailing
// suffix (e.g. "v1.31.1-tctl-1.18.4-cli-1.5.0" -> "1.31.1"). Mirrors the
// old check-temporal-versions.sh leading_semver() helper.
type leadingSemverNormalizer struct{}

var leadingSemverPattern = regexp.MustCompile(`^(v?)([0-9]+\.[0-9]+\.[0-9]+)`)

func (leadingSemverNormalizer) Normalize(raw string) (string, bool) {
	m := leadingSemverPattern.FindStringSubmatch(raw)
	if m == nil {
		return "", false
	}
	return m[2], true
}

// FixValue rewrites only the leading X.Y.Z portion of raw, preserving any
// "v" prefix and any suffix after it untouched (e.g. raw
// "v1.30.0-tctl-1.18.4-cli-1.5.0" with target "1.31.1" becomes
// "v1.31.1-tctl-1.18.4-cli-1.5.0").
func (leadingSemverNormalizer) FixValue(raw, target string) string {
	loc := leadingSemverPattern.FindStringSubmatchIndex(raw)
	if loc == nil {
		// Normalize() would have returned ok=false in this case, so a
		// well-behaved caller never reaches here; return raw unchanged
		// defensively rather than corrupt it.
		return raw
	}
	vPrefix := raw[loc[2]:loc[3]]
	suffix := raw[loc[1]:]
	return vPrefix + target + suffix
}

// majorMinorNormalizer truncates a version to its X.Y form. Applied to
// BOTH sides of a comparison, so a source of "1.26.4" and a captured
// Docker tag of "1.26" compare equal.
type majorMinorNormalizer struct{}

var majorMinorPattern = regexp.MustCompile(`^([0-9]+\.[0-9]+)`)

func (majorMinorNormalizer) Normalize(raw string) (string, bool) {
	m := majorMinorPattern.FindStringSubmatch(raw)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func (majorMinorNormalizer) FixValue(_, target string) string { return target }
