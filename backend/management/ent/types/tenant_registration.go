// Package types holds the Go types of typed JSON columns and the Go
// derivations of computed columns. They live outside ent/schema because
// the schema package imports the generated privacy package, which imports
// the generated code, which would import the schema package again for the
// column types.
package types

// TenantRegistration is the Zitadel admin account a pending tenant row
// still has to be provisioned with. PasswordHash is the bcrypt hash
// (modular crypt format, "$2a$...") the workflow imports into Zitadel as
// the user's password; the clear-text password is never stored.
type TenantRegistration struct {
	AdminUsername  string `json:"adminUsername"`
	AdminEmail     string `json:"adminEmail"`
	AdminFirstName string `json:"adminFirstName"`
	AdminLastName  string `json:"adminLastName"`
	PasswordHash   string `json:"passwordHash"`
}
