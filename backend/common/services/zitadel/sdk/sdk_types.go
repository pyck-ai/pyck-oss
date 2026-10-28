package sdk

// UserProfile is one user of an organization, the shape zitadel-sync
// projects into the management.users table.
type UserProfile struct {
	ID        string
	Username  string
	Email     string
	FirstName string
	LastName  string
}

// Org identifies a Zitadel organization.
type Org struct {
	ID string
}

// Grant is a role assignment as Zitadel reports it, at either level: an
// organization's grant on a project (Roles is the menu of keys a user grant
// attached to it may carry) or a user's authorization within that org
// (Roles is the subset the user actually holds).
type Grant struct {
	ID    string
	Roles []string
}
