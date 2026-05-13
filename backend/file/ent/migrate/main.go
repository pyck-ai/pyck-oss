//go:build ignore
// +build ignore

package main

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/pyck-ai/pyck/backend/common/ent"

	"github.com/pyck-ai/pyck/backend/file/core"
	"github.com/pyck-ai/pyck/backend/file/ent/gen/migrate"
)

func main() {
	if err := core.LoadEnv(); err != nil {
		log.Fatal().Err(err).Msg("failed loading configuration")
		return
	}

	ent.Migrate(context.Background(), ent.MigrateOptions{
		TargetSchema:          "file",
		Tables:                migrate.Tables,
		DbMasterUrl:           core.Config.DbMasterUrl,
		MigrationShadowDbName: core.Config.MigrationShadowDbName,
		MigrationDbName:       core.Config.MigrationDbName,
	})
}
