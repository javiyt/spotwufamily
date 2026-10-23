package httpadmin

import "time"

// SetPendingTimeout shortens the pending computation deadline for a test.
func SetPendingTimeout(d time.Duration) func() {
	old := pendingTimeout
	pendingTimeout = d
	return func() { pendingTimeout = old }
}
