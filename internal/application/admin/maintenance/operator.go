package maintenance

import (
	"context"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// maxRecentOperations bounds how many log entries one listing reads.
const maxRecentOperations = 100

// errOperationLogUnreadable is the safe error for a failing log read.
var errOperationLogUnreadable = &SafeError{Code: "unavailable", Message: "The operations log could not be read."}

// PreviewAction is Preview under the name the admin UI's port uses.
func (s *Service) PreviewAction(ctx context.Context, name string, p Params) (Preview, Token, error) {
	return s.Preview(ctx, name, p)
}

// RunAction is Run under the name the admin UI's port uses.
func (s *Service) RunAction(ctx context.Context, name string, p Params, tok Token, typed string) (jobs.Job, error) {
	return s.Run(ctx, name, p, tok, typed)
}

// RecentOperations returns up to n operation log entries, newest first (n is
// capped at 100). Without a log it returns nothing; a failing log yields a
// safe error and the real one goes to the log error handler.
func (s *Service) RecentOperations(ctx context.Context, n int) ([]OperationEntry, error) {
	if s.oplog == nil || n <= 0 {
		return nil, nil
	}
	n = min(n, maxRecentOperations)
	entries, err := s.oplog.Recent(ctx, n)
	if err != nil {
		s.reportLogErr(err)
		return nil, errOperationLogUnreadable
	}
	return entries, nil
}
