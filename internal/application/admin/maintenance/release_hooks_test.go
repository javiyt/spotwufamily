package maintenance

// waitRefresh waits for a running background check. It is a test hook and
// lives here so production code carries no test-only helpers.
func (a *releaseAction) waitRefresh() {
	a.mu.Lock()
	done := a.refreshed
	a.mu.Unlock()
	if done != nil {
		<-done
	}
}
