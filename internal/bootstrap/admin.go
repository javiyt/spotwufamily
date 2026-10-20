// Package bootstrap is the shared composition root: it wires concrete adapters
// into application use cases so inbound adapters (CLI, HTTP admin) do not each
// repeat the wiring.
package bootstrap

import (
	"context"
	"os"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin"
)

// NewAdminService wires the admin use cases (queries and catalog edits). A missing or unusable
// database is not an error: the service keeps serving the YAML catalog
// (offline mode) and never creates the database file. The returned function
// releases the database handle.
func NewAdminService(catalogPath, dbPath string) (admin.Service, func(), error) {
	store := yamlcatalog.NewStore()
	credentials := SpotifyCredentialsPresent

	var ops admin.OperationsReader
	closeFn := func() {}
	if info, err := os.Stat(dbPath); err == nil && !info.IsDir() {
		if database, err := sqliteadapter.Open(dbPath); err == nil {
			ops = admin.NewOperations(database, database)
			closeFn = func() { _ = database.Close() }
		}
	}

	return admin.NewService(store, ops, credentials, catalogPath), closeFn, nil
}

// SpotifyCredentialsPresent reports whether both Client Credentials variables
// are set. Values are never read into the result.
func SpotifyCredentialsPresent() bool {
	return os.Getenv("SPOTIFY_CLIENT_ID") != "" && os.Getenv("SPOTIFY_CLIENT_SECRET") != ""
}

// MigrateDatabase creates and migrates a database (used by smoke tests).
func MigrateDatabase(ctx context.Context, dbPath string) error {
	database, err := sqliteadapter.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	return database.Migrate(ctx)
}
