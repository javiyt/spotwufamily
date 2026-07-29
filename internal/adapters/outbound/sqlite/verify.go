package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

type VerifyReport struct {
	Migrations int
	Checks     []string
}

func (d *Database) Verify(ctx context.Context, migrations []Migration) (VerifyReport, error) {
	if err := d.withContext(ctx); err != nil {
		return VerifyReport{}, err
	}

	report := VerifyReport{Checks: []string{}}
	if err := verifyIntegrity(d.db); err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, "integrity_check")

	if err := verifyForeignKeys(d.db); err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, "foreign_key_check")

	if err := verifyMigrations(d.db, migrations); err != nil {
		return report, err
	}
	report.Migrations = len(migrations)
	report.Checks = append(report.Checks, "migrations")

	return report, nil
}

func verifyIntegrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("run integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check failed: %s", result)
	}

	return nil
}

func verifyForeignKeys(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("run foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if rows.Next() {
		return fmt.Errorf("foreign_key_check reported violations")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate foreign_key_check: %w", err)
	}

	return nil
}

func verifyMigrations(db *sql.DB, migrations []Migration) error {
	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		existing, ok := applied[migration.Version]
		if !ok {
			return fmt.Errorf("migration %03d is not applied", migration.Version)
		}
		if existing.Checksum != migration.Checksum {
			return fmt.Errorf("migration %03d checksum mismatch", migration.Version)
		}
	}

	return nil
}
