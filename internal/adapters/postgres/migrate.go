package postgres

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/vit3ro/transaction-execution-api/migrations"
)

const migrateDriverScheme = "pgx5"

func Migrate(databaseURL string) (err error) {
	migrateURL, err := toMigrateURL(databaseURL)
	if err != nil {
		return err
	}
	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}
	migrator, err := migrate.NewWithSourceInstance("iofs", source, migrateURL)
	if err != nil {
		return fmt.Errorf("connect migrator: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := migrator.Close()
		err = errors.Join(err, sourceErr, databaseErr)
	}()

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func toMigrateURL(databaseURL string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", errors.New("parse database url for migrations: invalid url")
	}
	parsed.Scheme = migrateDriverScheme
	return parsed.String(), nil
}
