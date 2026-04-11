//go:build ignore

package main

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/ent"
	"github.com/pyck-ai/pyck/backend/inventory/core"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/migrate"
	"github.com/rs/zerolog/log"
)

func main() {
	if err := core.LoadEnv(); err != nil {
		log.Fatal().Err(err).Msg("failed loading configuration")
		return
	}

	ent.Migrate(context.Background(), ent.MigrateOptions{
		TargetSchema:          "inventory",
		Tables:                migrate.Tables,
		DbMasterUrl:           core.Config.DbMasterUrl,
		MigrationShadowDbName: core.Config.MigrationShadowDbName,
		MigrationDbName:       core.Config.MigrationDbName,
	})
}
