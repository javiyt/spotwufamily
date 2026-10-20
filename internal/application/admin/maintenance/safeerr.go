package maintenance

import (
	"errors"
	"fmt"
	"math"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Sentinel errors maintenance workflows return; Safe maps them to fixed messages.
var (
	ErrCredentialsMissing   = errors.New("credentials missing")
	ErrStalePreview         = errors.New("stale preview")
	ErrConfirmationRequired = errors.New("confirmation required")
	ErrUnavailable          = errors.New("unavailable")
)

// SafeError is an error whose message is safe to show to users.
type SafeError struct {
	Code    string
	Message string
}

func (e *SafeError) Error() string { return e.Message }

// Safe returns the user-safe message.
func (e *SafeError) Safe() string { return e.Message }

// Safe maps err to a *SafeError; unknown errors become code "internal".
// A nil error stays nil.
func Safe(err error) error {
	if err == nil {
		return nil
	}
	var se *SafeError
	if errors.As(err, &se) {
		return se
	}
	var (
		jc *jobs.ConflictError
		ac *admin.ConflictError
		av *admin.ValidationError
		ap *admin.ConfirmationError
		ar *admin.ReferencedError
		ps *catalogsync.PartialSyncError
	)
	if wait, ok := artists.RetryAfterOf(err); ok {
		secs := int(math.Ceil(wait.Seconds()))
		return &SafeError{"rate_limited", fmt.Sprintf("The source is rate limited; retry after %d seconds.", secs)}
	}
	switch {
	case errors.As(err, &jc), errors.Is(err, jobs.ErrConflict), errors.As(err, &ac), errors.As(err, &ar):
		return &SafeError{"conflict", "Another change or job is in progress for this resource."}
	case errors.As(err, &av), errors.Is(err, artists.ErrInvalidMapping):
		return &SafeError{"validation", "The request is not valid."}
	case errors.Is(err, ErrCredentialsMissing):
		return &SafeError{"credentials_missing", "Required credentials are not configured."}
	case errors.Is(err, ErrStalePreview):
		return &SafeError{"stale_preview", "The preview is out of date; generate it again."}
	case errors.Is(err, ErrConfirmationRequired), errors.As(err, &ap), errors.Is(err, admin.ErrOverrideRequired):
		return &SafeError{"confirmation_required", "Explicit confirmation is required."}
	case errors.As(err, &ps), errors.Is(err, artists.ErrSourceRateLimited):
		return &SafeError{"rate_limited", "The source is rate limited; try again later."}
	case errors.Is(err, ErrUnavailable), errors.Is(err, artists.ErrSourceUnavailable):
		return &SafeError{"unavailable", "The source is temporarily unavailable."}
	case errors.Is(err, admin.ErrNotFound), errors.Is(err, artists.ErrSourceNotFound):
		return &SafeError{"not_found", "The requested item was not found."}
	}
	return &SafeError{"internal", "An internal error occurred."}
}
