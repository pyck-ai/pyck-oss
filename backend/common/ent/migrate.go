package ent

import (
	"context"
	"os"

	"ariga.io/atlas/sql/sqltool"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/lib/pq"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/log"
)

type MigrateOptions struct {
	TargetSchema          string
	DbMasterUrl           string
	MigrationShadowDbName string
	MigrationDbName       string
	Tables                []*schema.Table
}

func Migrate(ctx context.Context, opts MigrateOptions) {
	logger := log.ForContext(ctx).With().
		Str("component", "ent-migrate").
		Logger()

	// Create a local migration directory able to understand golang-migrate migration file format for replay.
	dir, err := sqltool.NewGolangMigrateDir("ent/migrate/migrations")
	if err != nil {
		logger.Fatal().Err(err).Msg("failed creating atlas migration directory")
		return
	}

	// Migrate diff options.
	entopts := []schema.MigrateOption{
		schema.WithDir(dir), // Migration directory to use
		schema.WithMigrationMode(schema.ModeReplay),
		schema.WithDialect(dialect.Postgres),
		schema.WithDropIndex(true), // Remove old indexes
		schema.WithDropColumn(true),
		schema.WithSchemaName(opts.TargetSchema),
	}
	if len(os.Args) != 2 {
		logger.Fatal().Msg("migration name is required. Use: 'go run -mod=readonly ent/migrate/main.go <name>'")
		return
	}

	if err := db.CreateShadowDatabase(ctx,
		opts.DbMasterUrl,
		opts.MigrationShadowDbName,
		opts.MigrationDbName,
		opts.TargetSchema,
	); err != nil {
		logger.Fatal().Err(err).Msg("failed creating shadow database")
		return
	}

	defer func() {
		// Drop the shadow database after generating the migration file
		if err := db.DropShadowDatabase(ctx, opts.DbMasterUrl, opts.MigrationShadowDbName); err != nil {
			logger.Fatal().Err(err).Msg("failed dropping shadow database")
		}
	}()

	// Generate migrations using the shadow database and the schema for "inventory
	if err := schema.Diff(
		ctx,
		db.GetDbMigrateDiffUrl(opts.DbMasterUrl, opts.MigrationShadowDbName, opts.TargetSchema),
		os.Args[1],
		opts.Tables,
		entopts...,
	); err != nil {
		logger.Fatal().Err(err).Msg("failed generating migration file")
		return
	}
}
