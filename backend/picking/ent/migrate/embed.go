package migrate

import "embed"

// Migrations holds the embedded SQL migration files for the picking service.
//
//go:embed migrations/*.sql
var Migrations embed.FS
