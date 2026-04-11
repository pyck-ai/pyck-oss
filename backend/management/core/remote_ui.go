package core

import (
	"fmt"
	"reflect"
	"strings"
)

// computedKeys are system-computed keys that must never be overwritten by
// external metadata (Zitadel). They are set exclusively by EnrichRemoteUIURLs.
var computedKeys = map[string]bool{
	"remoteWebUI":    true,
	"remoteMobileUI": true,
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

// DetectFlavour returns the flavour name from tenant data, or "" for normal tenants.
// Precedence: flavour string > isPyckGo bool.
// Expects boolean fields to already be native Go bools (converted at ingestion).
func DetectFlavour(data map[string]any) string {
	if data == nil {
		return ""
	}
	// flavour string takes precedence (future extensibility)
	if f, ok := data["flavour"].(string); ok && f != "" {
		return f
	}
	if v, ok := data["isPyckGo"].(bool); ok && v {
		return "pyck-go"
	}
	return ""
}

// EnrichRemoteUIURLs populates remoteWebUI and remoteMobileUI in data.
// Returns the (possibly newly created) data map, callers MUST assign back.
//
// Guarantees:
//   - Returns input data unchanged if frontendBaseURL is empty
//   - Initializes data map if nil and frontendBaseURL is configured
//   - Skips each URL field independently if already set to a non-empty string
//   - Both web and mobile URLs are always populated
//   - Flavour tenants use flavour-based paths, normal tenants use tenant-ID paths
func EnrichRemoteUIURLs(data map[string]any, tenantID, frontendBaseURL, env string) map[string]any {
	if frontendBaseURL == "" {
		return data
	}
	if data == nil {
		data = make(map[string]any)
	}

	base := strings.TrimRight(frontendBaseURL, "/")
	env = strings.ToLower(env)

	var webURL, mobileURL string
	if flavour := DetectFlavour(data); flavour != "" {
		webURL = fmt.Sprintf("%s/flavours/%s/%s/web/mf-manifest.json", base, flavour, env)
		mobileURL = fmt.Sprintf("%s/flavours/%s/%s/mobile/widgets.rfw", base, flavour, env)
	} else {
		webURL = fmt.Sprintf("%s/%s/web/mf-manifest.json", base, tenantID)
		mobileURL = fmt.Sprintf("%s/%s/mobile/widgets.rfw", base, tenantID)
	}

	if v, ok := data["remoteWebUI"].(string); !ok || v == "" {
		data["remoteWebUI"] = webURL
	}
	if v, ok := data["remoteMobileUI"].(string); !ok || v == "" {
		data["remoteMobileUI"] = mobileURL
	}

	return data
}

// MergeData merges existing DB data with incoming Zitadel metadata.
// Incoming values overwrite existing ones for shared keys, EXCEPT computed keys
// (remoteWebUI, remoteMobileUI) which are never taken from external sources.
// Returns nil only when both inputs are nil.
func MergeData(existing, incoming map[string]any) map[string]any {
	if existing == nil && incoming == nil {
		return nil
	}
	result := make(map[string]any)
	for k, v := range existing {
		result[k] = v
	}
	for k, v := range incoming {
		if computedKeys[k] {
			continue
		}
		result[k] = v
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

// ReconcileTenantData produces the final merged+enriched data for a single tenant.
// Flow: merge → enrich. Boolean fields must already be converted at ingestion.
func ReconcileTenantData(existingData, zitadelMetadata map[string]any, tenantID, frontendBaseURL, env string) map[string]any {
	merged := MergeData(existingData, zitadelMetadata)
	return EnrichRemoteUIURLs(merged, tenantID, frontendBaseURL, env)
}
