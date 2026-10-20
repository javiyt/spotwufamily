package sqlite

import "time"

// SetBackupClock replaces the clock used to name backups and returns a func
// restoring the previous one.
func SetBackupClock(now func() time.Time) func() {
	previous := backupNow
	backupNow = now
	return func() { backupNow = previous }
}
