package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const DriverName = "sqlite3"

type Database struct {
	db *sql.DB
}

func Open(path string) (*Database, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open(DriverName, path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	database := &Database{db: db}
	if err := database.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return database, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) DB() *sql.DB {
	return d.db
}

func (d *Database) Migrate(ctx context.Context) error {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		return err
	}
	if err := d.withContext(ctx); err != nil {
		return err
	}

	return ApplyMigrations(d.db, migrations)
}

func (d *Database) Optimize(ctx context.Context) error {
	if err := d.withContext(ctx); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return fmt.Errorf("optimize SQLite database: %w", err)
	}

	return nil
}

func (d *Database) configure() error {
	pragmas := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = DELETE`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
	}
	for _, pragma := range pragmas {
		if _, err := d.db.Exec(pragma); err != nil {
			return fmt.Errorf("configure SQLite %q: %w", pragma, err)
		}
	}

	return nil
}

func (d *Database) withContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
