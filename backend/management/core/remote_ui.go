package core

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"text/template"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

// ErrInvalidUITemplate is returned when a UI bundle URL template fails
// validation at write time.
var ErrInvalidUITemplate = errors.New("invalid UI bundle URL template")

// ValidateRemoteUITemplate checks that a UI bundle URL template is a parseable
// text/template, a well-formed absolute http(s) URL, and carries both the
// {{.Slug}} and {{.Version}} placeholders. The setTenantUITemplate mutation is
// system-only, but a malformed override is far cheaper to reject here than to
// discover when the frontend fails to load a manifest.
func ValidateRemoteUITemplate(tmpl string) error {
	for _, placeholder := range []string{"{{.Slug}}", "{{.Version}}"} {
		if !strings.Contains(tmpl, placeholder) {
			return fmt.Errorf("%w: missing %s placeholder", ErrInvalidUITemplate, placeholder)
		}
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
	return nil
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

// zitadelSyncedKeys are the only tenant-data keys taken from Zitadel metadata
// during sync. Everything else in tenant.Data is written directly (tenant
// registration, the system-role UI-template mutation) and is the source of
// truth. Keep this set in sync with tenant-settings.json.
var zitadelSyncedKeys = map[string]bool{
	"isPyckGo":       true,
	"isSetupPending": true,
	"flavour":        true,
}

// booleanFields lists tenant data keys whose canonical type is bool
// (per tenant-settings.json schema). Zitadel metadata stores everything as
// strings, so callers should convert at the ingestion boundary using
// IsBooleanField + strconv.ParseBool.
var booleanFields = map[string]bool{
	"isPyckGo":       true,
	"isSetupPending": true,
}

// IsBooleanField reports whether key is a tenant data field with bool as its
// canonical type.
func IsBooleanField(key string) bool {
	return booleanFields[key]
}

// DetectFlavour returns the flavour name from tenant data, or "" for a normal
// tenant. Aliased from common/workflow (the canonical home) so management and
// the workflow service share one definition.
func DetectFlavour(data map[string]any) string {
	return commonworkflow.DetectFlavour(data)
}

// MergeData overlays the Zitadel-synced flag keys on top of stored data.
// Stored data (existing) is the source of truth; from incoming (Zitadel) only
// zitadelSyncedKeys are taken. Returns nil only when both inputs are nil.
func MergeData(existing, incoming map[string]any) map[string]any {
	if existing == nil && incoming == nil {
		return nil
	}
	result := make(map[string]any)
	for k, v := range existing {
		result[k] = v
	}
	for k, v := range incoming {
		if zitadelSyncedKeys[k] {
			result[k] = v
		}
	}
	return result
}

// MapsEqual compares two data maps for equality at the top level.
// Treats nil and empty map as equal to avoid unnecessary DB writes.
func MapsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

