package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

func EmbeddedMigrations() ([]Migration, error) {
	root, err := findRepositoryRoot()
	if err != nil {
		return nil, err
	}

	return LoadMigrations(os.DirFS(root), "migrations")
}

func LoadMigrations(source fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(source, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, err := migrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		path := filepath.ToSlash(filepath.Join(dir, entry.Name()))
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(data)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     entry.Name(),
			SQL:      string(data),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.SliceStable(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

func ApplyMigrations(db *sql.DB, migrations []Migration) error {
	if err := ensureMigrationTable(db); err != nil {
		return err
	}

	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		existing, ok := applied[migration.Version]
		if ok {
			if existing.Checksum != migration.Checksum {
				return fmt.Errorf("migration %03d checksum mismatch", migration.Version)
			}
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", migration.Name, err)
		}
		if _, err := tx.Exec(migration.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", migration.Name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			migration.Version,
			migration.Name,
			migration.Checksum,
			"1970-01-01T00:00:00Z",
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", migration.Name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", migration.Name, err)
		}
	}

	return nil
}

func ensureMigrationTable(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	return nil
}

func appliedMigrations(db *sql.DB) (map[int]Migration, error) {
	rows, err := db.Query(`SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := map[int]Migration{}
	for rows.Next() {
		var migration Migration
		if err := rows.Scan(&migration.Version, &migration.Name, &migration.Checksum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[migration.Version] = migration
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}

	return applied, nil
}

func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %s must start with numeric version", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("migration %s has invalid version", name)
	}

	return version, nil
}

func findRepositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "migrations")); err == nil {
			return dir, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat migrations directory: %w", err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate migrations directory")
		}
		dir = parent
	}
}
