package maintenance

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// errUnknownAction is returned when no action has the requested name.
var errUnknownAction = &SafeError{Code: "not_found", Message: "The requested action was not found."}

// Service previews maintenance actions, issues preview tokens and runs
// confirmed actions as jobs.
type Service struct {
	jobs    *jobs.Manager
	actions []Action
	byName  map[string]Action
	secret  []byte
	now     func() time.Time
}

// NewService registers actions in order. It panics if two actions share a
// name, since that is a wiring bug rather than a runtime condition.
func NewService(m *jobs.Manager, actions []Action, secret []byte, now func() time.Time) *Service {
	s := &Service{
		jobs:    m,
		actions: append([]Action(nil), actions...),
		byName:  make(map[string]Action, len(actions)),
		secret:  append([]byte(nil), secret...),
		now:     now,
	}
	for _, a := range actions {
		if _, dup := s.byName[a.Name()]; dup {
			panic("maintenance: duplicate action name " + a.Name())
		}
		s.byName[a.Name()] = a
	}
	return s
}

// Actions lists registered actions in registration order.
func (s *Service) Actions() []ActionInfo {
	out := make([]ActionInfo, 0, len(s.actions))
	for _, a := range s.actions {
		ok, reason := a.Available()
		info := ActionInfo{Name: a.Name(), Title: a.Name(), Available: ok}
		if !ok {
			info.UnavailableReason = reason
		}
		if d, isD := a.(Describer); isD {
			desc := d.Describe()
			if desc.Title != "" {
				info.Title = desc.Title
			}
			info.UsesNetwork, info.Destructive = desc.UsesNetwork, desc.Destructive
		}
		out = append(out, info)
	}
	return out
}

// Preview validates p, computes the action's preview and returns a token
// that authorises running exactly this preview for TokenTTL.
func (s *Service) Preview(ctx context.Context, name string, p Params) (Preview, Token, error) {
	a, pv, err := s.preview(ctx, name, p)
	if err != nil {
		return Preview{}, "", err
	}
	tok := signToken(s.secret, a.Name(), p, pv.Fingerprint, s.now().Add(TokenTTL))
	return pv, tok, nil
}

// Run recomputes the preview, verifies tok against it and the typed
// confirmation, then submits the action's job. A *jobs.ConflictError from
// the job manager is returned unchanged; other failures are safe errors.
func (s *Service) Run(ctx context.Context, name string, p Params, tok Token, typed string) (jobs.Job, error) {
	a, pv, err := s.preview(ctx, name, p)
	if err != nil {
		return jobs.Job{}, err
	}
	if !verifyToken(s.secret, tok, a.Name(), p, pv.Fingerprint, s.now()) {
		return jobs.Job{}, ErrStalePreview
	}
	if pv.RequiresTyped != "" && typed != pv.RequiresTyped {
		return jobs.Job{}, ErrConfirmationRequired
	}
	spec, err := a.Spec(ctx, maps.Clone(p))
	if err != nil {
		return jobs.Job{}, safeErr(err)
	}
	j, err := s.jobs.Submit(ctx, spec)
	if err != nil {
		var ce *jobs.ConflictError
		if errors.As(err, &ce) {
			return jobs.Job{}, ce
		}
		return jobs.Job{}, safeErr(err)
	}
	return j, nil
}

// preview resolves the action, validates params, checks availability and
// computes a fresh preview.
func (s *Service) preview(ctx context.Context, name string, p Params) (Action, Preview, error) {
	a, ok := s.byName[name]
	if !ok {
		return nil, Preview{}, errUnknownAction
	}
	if err := a.Validate(p); err != nil {
		return nil, Preview{}, validationErr(err)
	}
	if ok, _ := a.Available(); !ok {
		return nil, Preview{}, ErrUnavailable
	}
	pv, err := a.Preview(ctx, maps.Clone(p))
	if err != nil {
		return nil, Preview{}, safeErr(err)
	}
	pv.Action = a.Name()
	return a, pv, nil
}

// validationErr keeps SafeErrors from Validate and maps anything else to a
// generic validation error so raw details never reach callers.
func validationErr(err error) error {
	var se *SafeError
	if errors.As(err, &se) {
		return se
	}
	if mapped := Safe(err).(*SafeError); mapped.Code != "internal" {
		return mapped
	}
	return &SafeError{Code: "validation", Message: "The request is not valid."}
}

// safeErr returns maintenance sentinels unchanged (so callers can match them
// with errors.Is; Safe maps them) and converts everything else with Safe.
func safeErr(err error) error {
	for _, sentinel := range []error{ErrStalePreview, ErrConfirmationRequired, ErrUnavailable, ErrCredentialsMissing} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return Safe(err)
}
