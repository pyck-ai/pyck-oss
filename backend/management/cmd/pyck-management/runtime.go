package main

import (
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"

	_ "github.com/pyck-ai/pyck/backend/management/ent/gen/runtime"
)
