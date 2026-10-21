package maintenance

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// maxLogTextLen bounds a logged error cause.
const maxLogTextLen = 2000

// credentialPatterns matches GitHub tokens and key=value credentials.
var credentialPatterns = regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|(?i)(token|authorization|password|secret|client_secret)\s*[:=]?\s*\S+)`)

// LogText renders err's cause (see CauseOf) for a server-side log: the
// given secret values and anything that looks like a credential are
// redacted, control characters become spaces and the text is truncated.
// Local paths are kept: the log is the operator's, never a response.
func LogText(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	text := CauseOf(err).Error()
	for _, v := range secrets {
		if v != "" {
			text = strings.ReplaceAll(text, v, "[redacted]")
		}
	}
	text = credentialPatterns.ReplaceAllString(text, "[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > maxLogTextLen {
		text = string(r[:maxLogTextLen]) + "…"
	}
	return text
}

// IsCancellation reports whether err's cause is a context cancellation or
// deadline: an expected end, logged below error level.
func IsCancellation(err error) bool {
	cause := CauseOf(err)
	return errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)
}
