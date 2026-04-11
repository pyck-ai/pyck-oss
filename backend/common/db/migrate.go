package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/pyck-ai/pyck/backend/common/log"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func RunMigrations(ctx context.Context, db *sql.DB, serviceName string, appPath string) error {
	// create schema
	_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS \"%s\"", serviceName))
	if err != nil {
		return err
	}

	// run migrations
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return err
	}

	// allow running outside of a container by setting env-var SERVICES_PATH (e.q. path to backend/)
	migrationsPath := "file://migrations"
	if appPath != "" {
		migrationsPath = fmt.Sprintf("file://%s/%s/ent/migrate/migrations", appPath, serviceName)
	}

	migration, err := migrate.NewWithDatabaseInstance(migrationsPath, "postgres", driver)
	if err != nil {
		return err
	}

	err = migration.Up()
	if err != nil && err != migrate.ErrNoChange {
		if strings.Contains(err.Error(), "no such file or directory") {
			return errors.New("no migrations found. Please add migrations to ent/migrate/migrations folder")
		}

		return err
	}

	return nil
}

func GetDbMigrateDiffUrl(dbUrl string, shadowDbName string, targetSchema string) string {
	logger := log.DefaultLogger().With().
		Str("component", "db-migrate").
		Logger()

	parsedUrl, err := url.Parse(dbUrl)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed parsing database URL")
	}

	parsedUrl.Path = fmt.Sprintf("/%s", shadowDbName)

	return fmt.Sprintf("%s&search_path=%s", parsedUrl.String(), targetSchema)
}

func CreateShadowDatabase(ctx context.Context, dbUrl, shadowDbName, currentDbName, targetSchema string) error {
	logger := log.ForContext(ctx).With().
		Str("component", "db-migrate").
		Logger()

	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		return fmt.Errorf("failed connecting to the master DB: %v", err)
	}
	defer db.Close()

	shadowDbName = strings.ReplaceAll(shadowDbName, "-", "_")

	if err := DropShadowDatabase(ctx, dbUrl, shadowDbName); err != nil {
		logger.Warn().Err(err).Msg("failed to drop existing shadow database")
	}

	_, err = db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", shadowDbName, currentDbName))
	if err != nil {
		return fmt.Errorf("failed creating shadow database: %v", err)
	}

	logger.Info().Str("shadow_db", shadowDbName).Msg("shadow database created successfully")

	shadowDbUrl := GetDbMigrateDiffUrl(dbUrl, shadowDbName, currentDbName)
	shadowDb, err := sql.Open("postgres", shadowDbUrl)
	if err != nil {
		return fmt.Errorf("failed connecting to the shadow database: %v", err)
	}
	defer shadowDb.Close()

	_, err = shadowDb.ExecContext(ctx, fmt.Sprintf(`
	DO $$
	DECLARE
		r RECORD;
	BEGIN
		FOR r IN (SELECT nspname FROM pg_catalog.pg_namespace WHERE nspname NOT IN ('%s', 'pg_toast', 'pg_catalog', 'information_schema'))
		LOOP
			EXECUTE 'DROP SCHEMA IF EXISTS ' || quote_ident(r.nspname) || ' CASCADE';
		END LOOP;
	END $$;
	`, targetSchema))
	if err != nil {
		return fmt.Errorf("failed dropping schemas in shadow database: %v", err)
	}

	logger.Info().Str("target_schema", targetSchema).Msg("all schemas except target dropped successfully in shadow database")

	_, err = shadowDb.ExecContext(ctx, fmt.Sprintf(`
	DO $$
	DECLARE 
		r RECORD; 
	BEGIN 
		FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = '%s') 
		LOOP 
			EXECUTE 'DROP TABLE IF EXISTS "%s"."%s".' || quote_ident(r.tablename) || ' CASCADE'; 
		END LOOP; 
	END $$;
	`, targetSchema, shadowDbName, targetSchema))
	if err != nil {
		return fmt.Errorf("failed dropping tables in '%s' schema: %v", targetSchema, err)
	}

	logger.Info().
		Str("target_schema", targetSchema).
		Str("shadow_db", shadowDbName).
		Msg("all tables dropped successfully in schema of shadow database")

	return nil
}

func DropShadowDatabase(ctx context.Context, dbUrl string, shadowDbName string) error {
	logger := log.ForContext(ctx).With().
		Str("component", "db-migrate").
		Logger()

	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		return fmt.Errorf("failed connecting to the master DB: %v", err)
	}
	defer db.Close()

	if err := disconnectAllUsers(ctx, db, shadowDbName); err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", shadowDbName))
	if err != nil {
		return fmt.Errorf("failed dropping shadow database: %v", err)
	}

	logger.Info().Str("shadow_db", shadowDbName).Msg("shadow database dropped successfully")
	return nil
}

func disconnectAllUsers(ctx context.Context, db *sql.DB, dbName string) error {
	logger := log.ForContext(ctx).With().
		Str("component", "db-migrate").
		Logger()

	query := fmt.Sprintf(`
        SELECT pg_terminate_backend(pid)
        FROM pg_stat_activity
        WHERE datname = '%s' AND pid <> pg_backend_pid();`, dbName)

	_, err := db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to disconnect all users from the database %s: %v", dbName, err)
	}

	logger.Info().Str("db_name", dbName).Msg("disconnected all users from database")
	return nil
}
