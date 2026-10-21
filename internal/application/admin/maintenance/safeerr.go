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

// SafeError is an error whose message is safe to show to users. The error
// it was mapped from is kept as its cause for server-side logs only.
type SafeError struct {
	Code    string
	Message string
	cause   error
}

func (e *SafeError) Error() string { return e.Message }

// Safe returns the user-safe message.
func (e *SafeError) Safe() string { return e.Message }

// Cause returns the error Safe mapped, or nil. It is deliberately not
// Unwrap: the cause never takes part in errors.Is/As matching, and its text
// (local paths, SQL) is for server logs only, never for responses.
func (e *SafeError) Cause() error { return e.cause }

// CauseOf returns the cause of a *SafeError in err's chain, or err itself
// when there is none.
func CauseOf(err error) error {
	var se *SafeError
	if errors.As(err, &se) && se.cause != nil {
		return se.cause
	}
	return err
}

// Safe maps err to a *SafeError; unknown errors become code "internal".
// The original error is kept as the cause. A nil error stays nil.
func Safe(err error) error {
	if err == nil {
		return nil
	}
	var se *SafeError
	if errors.As(err, &se) {
		return se
	}
	se = classify(err)
	se.cause = err
	return se
}

// classify picks the fixed user-safe message for err.
func classify(err error) *SafeError {
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
		return &SafeError{Code: "rate_limited", Message: fmt.Sprintf("The source is rate limited; retry after %d seconds.", secs)}
	}
	switch {
	case errors.Is(err, ErrPublishInterrupted):
		return &SafeError{Code: "interrupted", Message: "Publishing was interrupted; a partial release may exist on GitHub — check the Releases page before retrying."}
	case errors.Is(err, ErrGhNotAuthenticated):
		return &SafeError{Code: "credentials_missing", Message: "GitHub CLI is not authenticated. Run `gh auth login` in a terminal."}
	case errors.Is(err, ErrReleasePackageNotClean):
		return &SafeError{Code: "conflict", Message: "The release package directory is not empty; publish or remove the prepared package before preparing another."}
	case errors.Is(err, ErrNothingPrepared):
		return &SafeError{Code: "validation", Message: "Nothing is prepared for the current catalog content; run release-prepare first."}
	case errors.Is(err, ErrReleasePrerequisite):
		return &SafeError{Code: "unavailable", Message: "A release prerequisite is missing or the release script failed (python3, Go and the release script are required)."}
	case errors.As(err, &jc), errors.Is(err, jobs.ErrConflict), errors.As(err, &ac), errors.As(err, &ar), errors.Is(err, ErrLocked):
		return &SafeError{Code: "conflict", Message: "Another change or job is in progress for this resource."}
	case errors.As(err, &av), errors.Is(err, artists.ErrInvalidMapping):
		return &SafeError{Code: "validation", Message: "The request is not valid."}
	case errors.Is(err, ErrCredentialsMissing):
		return &SafeError{Code: "credentials_missing", Message: "Required credentials are not configured."}
	case errors.Is(err, ErrStalePreview):
		return &SafeError{Code: "stale_preview", Message: "The preview is out of date; generate it again."}
	case errors.Is(err, ErrConfirmationRequired), errors.As(err, &ap), errors.Is(err, admin.ErrOverrideRequired):
		return &SafeError{Code: "confirmation_required", Message: "Explicit confirmation is required."}
	case errors.As(err, &ps), errors.Is(err, artists.ErrSourceRateLimited):
		return &SafeError{Code: "rate_limited", Message: "The source is rate limited; try again later."}
	case errors.Is(err, ErrUnavailable), errors.Is(err, artists.ErrSourceUnavailable):
		return &SafeError{Code: "unavailable", Message: "The source is temporarily unavailable."}
	case errors.Is(err, admin.ErrNotFound), errors.Is(err, artists.ErrSourceNotFound):
		return &SafeError{Code: "not_found", Message: "The requested item was not found."}
	}
	return &SafeError{Code: "internal", Message: "An internal error occurred."}
}
