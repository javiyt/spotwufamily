package maintenance

import (
	"context"
	"slices"
	"sort"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Params are the user-supplied arguments of a maintenance action. Each action
// offers typed accessors and rejects unknown keys in Validate.
type Params map[string]string

// ScopeLine is one labelled line describing what an action will touch.
type ScopeLine struct {
	Label string
	Value string
}

// Preview describes what running an action would do. Fingerprint identifies
// the state the preview was computed from; RequiresTyped is the exact phrase
// the user must type to confirm, empty for non-destructive actions.
type Preview struct {
	Action        string
	Title         string
	Scope         []ScopeLine
	Warnings      []string
	Destructive   bool
	UsesNetwork   bool
	Fingerprint   string
	RequiresTyped string
}

// Action is a previewable, confirmable maintenance operation run as a job.
type Action interface {
	Name() string
	Validate(Params) error
	Preview(ctx context.Context, p Params) (Preview, error)
	Spec(ctx context.Context, p Params) (jobs.Spec, error)
	Available() (bool, string)
}

// QuickAvailability is optionally implemented by actions whose Available is
// costly. AvailableNow must return promptly (a cached or "checking" answer);
// Service.Actions uses it, while Service.Action and the preview/run path
// keep calling Available.
type QuickAvailability interface {
	AvailableNow() (bool, string)
}

// Description is static metadata an action may expose for listings.
type Description struct {
	Title       string
	UsesNetwork bool
	Destructive bool
}

// Describer is optionally implemented by actions to describe themselves in
// Service.Actions; without it the title defaults to the action name.
type Describer interface {
	Describe() Description
}

// ActionInfo summarises a registered action for listings.
type ActionInfo struct {
	Name              string
	Title             string
	Available         bool
	UnavailableReason string
	UsesNetwork       bool
	Destructive       bool
}

// RejectUnknown returns a validation SafeError if p has keys not in allowed.
func RejectUnknown(p Params, allowed ...string) error {
	var unknown []string
	for k := range p {
		if !slices.Contains(allowed, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return &SafeError{Code: "validation", Message: "Unknown parameter: " + quoteParam(unknown[0]) + "."}
}

// quoteParam bounds and sanitises a user-supplied key for display.
func quoteParam(k string) string {
	const maxLen = 40
	out := make([]rune, 0, len(k))
	for _, r := range k {
		if len(out) >= maxLen {
			out = append(out, '…')
			break
		}
		if r < 0x20 || r == 0x7f {
			r = '?'
		}
		out = append(out, r)
	}
	return "\"" + string(out) + "\""
}
