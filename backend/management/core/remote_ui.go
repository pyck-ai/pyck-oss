package core

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

// ErrInvalidUITemplate is returned when a UI bundle URL template fails
// validation at write time.
var ErrInvalidUITemplate = errors.New("invalid UI bundle URL template")

// ValidateRemoteUITemplate checks that a UI bundle URL template is a parseable
// text/template, a well-formed absolute http(s) URL whose host holds no
// invisible character or "%", and carries the {{.Version}} placeholder. Only
// {{.Version}} is required; {{.Slug}} is optional — per-tenant bundles bake the
// tenant into a literal path prefix and omit it, and workers stamp version-only
// for those (#1132/#1317). A malformed override is far cheaper to reject here
// than to discover when the frontend fails to load a manifest.
func ValidateRemoteUITemplate(tmpl string) error {
	if !strings.Contains(tmpl, "{{.Version}}") {
		return fmt.Errorf("%w: missing {{.Version}} placeholder", ErrInvalidUITemplate)
	}
	if _, err := template.New("uiBundleURL").Parse(tmpl); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidUITemplate, err)
	}
	u, err := url.Parse(tmpl)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidUITemplate, tmpl)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: must be an absolute http(s) URL", ErrInvalidUITemplate)
	}
	// url.Parse admits, and percent-decodes into, any rune in the host.
	if !utf8.ValidString(u.Host) || strings.ContainsFunc(u.Host, isInvisibleRune) {
		return fmt.Errorf("%w: host %+q contains an invisible or control character", ErrInvalidUITemplate, u.Host)
	}
	// A decoded "%" comes from "%25": a literal percent or an IPv6 zone,
	// which may carry an escaped space.
	if strings.Contains(u.Host, "%") {
		return fmt.Errorf("%w: host %+q must not contain \"%%\" or an IPv6 zone", ErrInvalidUITemplate, u.Host)
	}
	// Render values are single segments, so a ".." is literal text that would
	// fail every render; refuse it where the caller sees the error.
	if slices.Contains(strings.Split(u.Path, "/"), "..") {
		return fmt.Errorf("%w: path must not contain \"..\"", ErrInvalidUITemplate)
	}
	return nil
}

// ValidateRemoteUITemplates applies ValidateRemoteUITemplate to the UI template
// keys present in raw tenant data, as written by registerTenant.
func ValidateRemoteUITemplates(data map[string]any) error {
	for _, key := range RemoteUITemplateKeys {
		v, ok := data[key]
		if !ok {
			continue
		}
		tmpl, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: %w: must be a string", key, ErrInvalidUITemplate)
		}
		if err := ValidateRemoteUITemplate(tmpl); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// isInvisibleRune reports whether r is non-printable (control, format, space
// separator, private use, unassigned) or default-ignorable (variation
// selectors, Hangul fillers). IDN letters pass.
func isInvisibleRune(r rune) bool {
	return !unicode.IsPrint(r) ||
		unicode.In(r, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point)
}

// Tenant-data keys holding the per-workflow UI bundle URL *templates*. Each is a
// URL with {{.Slug}}/{{.Version}} placeholders that the workflow-service resolver
// substitutes at query time from the execution's pinned deployment-version
// metadata (#1317). The hostable origin + path layout stay tenant-controlled
// (needed for flavours and trusted-origins validation); only the bundle
// slug/version are filled in downstream.
//
// Aliased from common/workflow, the canonical home for this wire contract, so
// management and the workflow service cannot drift.
const (
	RemoteWebUITemplateKey    = commonworkflow.RemoteWebUITemplateKey
	RemoteMobileUITemplateKey = commonworkflow.RemoteMobileUITemplateKey
)

// RemoteUITemplateKeys lists both UI template keys.
var RemoteUITemplateKeys = []string{RemoteWebUITemplateKey, RemoteMobileUITemplateKey}

// DetectFlavour returns the flavour name from tenant data, or "" for a normal
// tenant. Aliased from common/workflow (the canonical home) so management and
// the workflow service share one definition.
func DetectFlavour(data map[string]any) string {
	return commonworkflow.DetectFlavour(data)
}

// MapsEqual compares two data maps for equality at the top level.
// Treats nil and empty map as equal to avoid unnecessary DB writes.
func MapsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
