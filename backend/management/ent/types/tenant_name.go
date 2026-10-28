package types

import "strings"

// TenantNameKey is the value of a tenant's name_key column: the name
// folded the way Zitadel folds an organization name for its uniqueness
// constraint (strings.ToLower in the eventstore's unique constraints), so
// two names Zitadel would refuse as the same organization collide on the
// tenant_name_key index before a registration run reaches Zitadel.
//
// The name is not trimmed: Zitadel trims an org name before folding it,
// but registerTenant refuses a name with surrounding whitespace, and the
// migration that backfilled the column folds stored names as they are.
//
// Zitadel and pyck may be built with Go versions that ship different
// Unicode tables. Case pairs are only ever added between Unicode versions,
// so a newer table folds a superset of what an older one does: the key
// can refuse a name an older Zitadel would accept, never accept one it
// refuses.
func TenantNameKey(name string) string {
	return strings.ToLower(name)
}
