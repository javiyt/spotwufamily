package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin"
)

var _ admin.SyncReader = (*Database)(nil)

// LastSync returns the most recently started sync run, if any.
func (d *Database) LastSync(ctx context.Context) (admin.SyncInfo, bool, error) {
	var status, started string
	var finished sql.NullString
	err := d.db.QueryRowContext(ctx, `SELECT status, started_at, finished_at FROM sync_runs ORDER BY id DESC LIMIT 1`).
		Scan(&status, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return admin.SyncInfo{}, false, nil
	}
	if err != nil {
		return admin.SyncInfo{}, false, fmt.Errorf("read last sync run: %w", err)
	}
	info := admin.SyncInfo{Status: status}
	info.StartedAt, _ = time.Parse(time.RFC3339, started)
	if finished.Valid {
		info.FinishedAt, _ = time.Parse(time.RFC3339, finished.String)
	}
	return info, true, nil
}
