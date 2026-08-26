// Package serviceroles defines the fixed set of per-service roles that gate
// access to individual pyck services. These roles live in Zitadel as project
// roles (seeded at bootstrap); this package is the single source of truth for
// which keys are assignable through the management API.
package serviceroles

//go:generate go tool enumer -type=ServiceRole -linecomment -output=servicerole_gen.go

// ServiceRole is an assignable per-service gate role. Its String() value is the
// Zitadel project-role key (e.g. "inventory_service") and must stay in sync
// with the roles seeded into Zitadel by the bootstrap config
// (backend/bootstrap/pkg/bootstrap/bootstrap.yaml). Management and workflow are
// intentionally not gated and therefore have no service role.
type ServiceRole int

const (
	Inventory ServiceRole = iota // inventory_service
	Picking                      // picking_service
	Receiving                    // receiving_service
	File                         // file_service
	MainData                     // main_data_service
)

// All is the fixed, ordered set of assignable per-service roles.
var All = ServiceRoleValues()

// IsServiceRole reports whether key is an assignable per-service role.
func IsServiceRole(key string) bool {
	_, err := ServiceRoleString(key)
	return err == nil
}
