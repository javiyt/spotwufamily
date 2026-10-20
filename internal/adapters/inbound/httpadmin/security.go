package httpadmin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie  = "swf_admin_session"
	sessionTTL     = 8 * time.Hour
	maxBodyBytes   = 64 << 10
	loginFailLimit = 10
	loginWindow    = time.Minute
)

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: https://*.scdn.co https://*.spotifycdn.com; " +
	"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// sessions are held in memory only: nothing is written to disk or the repo.
type sessions struct {
	mu      sync.Mutex
	items   map[string]time.Time
	flashes map[string]flash
	csrfKey []byte
	now     func() time.Time
}

func newSessions() *sessions {
	return &sessions{items: map[string]time.Time{}, flashes: map[string]flash{}, csrfKey: randomBytes(32), now: time.Now}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("httpadmin: system randomness unavailable: " + err.Error())
	}
	return b
}

func (s *sessions) create() string {
	id := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, exp := range s.items {
		if now.After(exp) {
			delete(s.items, k)
		}
	}
	s.items[id] = now.Add(sessionTTL)
	return id
}

func (s *sessions) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.items[id]
	if !ok {
		return false
	}
	if s.now().After(exp) {
		delete(s.items, id)
		return false
	}
	return true
}

// flash is a one-shot confirmation. Refresh appends the fixed hint that
// derived data may need refreshing after a catalog change; it carries no
// computed state.
type flash struct {
	Message string
	Refresh bool
}

// setFlash stores a one-shot confirmation for the next page of this session.
func (s *sessions) setFlash(id string, f flash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; ok {
		s.flashes[id] = f
	}
}

// takeFlash returns and clears the pending confirmation.
func (s *sessions) takeFlash(id string) flash {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flashes[id]
	delete(s.flashes, id)
	return f
}

func (s *sessions) destroy(id string) {
	s.mu.Lock()
	delete(s.items, id)
	delete(s.flashes, id)
	s.mu.Unlock()
}

func (s *sessions) csrf(id string) string {
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *sessions) checkCSRF(id, got string) bool {
	return got != "" && hmac.Equal([]byte(s.csrf(id)), []byte(got))
}

// failureLimiter throttles failed token attempts to blunt guessing.
type failureLimiter struct {
	mu    sync.Mutex
	fails []time.Time
	now   func() time.Time
}

func (l *failureLimiter) blocked() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	return len(l.fails) >= loginFailLimit
}

func (l *failureLimiter) record() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	l.fails = append(l.fails, l.now())
}

func (l *failureLimiter) prune() {
	cutoff := l.now().Add(-loginWindow)
	kept := l.fails[:0]
	for _, t := range l.fails {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.fails = kept
}

func tokenEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cache-Control", "no-store")
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin vets browser-supplied origin signals for state-changing requests.
func sameOrigin(r *http.Request, hosts map[string]struct{}) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		// With Referrer-Policy: no-referrer some browsers send "Origin: null" on
		// same-origin form posts. That alone proves nothing, so trust it only
		// when the browser also says the request is same-origin (a header page
		// script cannot forge).
		if origin == "null" {
			return r.Header.Get("Sec-Fetch-Site") == "same-origin"
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return false
		}
		_, ok := hosts[strings.ToLower(u.Host)]
		return ok
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	return true
}
