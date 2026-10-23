// Package httpadmin is the inbound HTTP adapter of the local admin UI. It is
// keeps all business rules in internal/application/admin. Editing routes exist
// only when the service also implements Editor; job pages only when it
// implements Maintenance; maintenance actions only when it implements Operator.
package httpadmin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Service is the set of use cases the adapter consumes.
type Service interface {
	Dashboard(ctx context.Context) (admin.Dashboard, error)
	ListArtists(ctx context.Context, q admin.ListQuery) (admin.Page[admin.ArtistRow], error)
	ListGroups(ctx context.Context, q admin.ListQuery) (admin.Page[admin.GroupRow], error)
	ReviewQueue(ctx context.Context, q admin.ListQuery) (admin.Page[admin.ReviewItem], error)
}

// Server is the admin HTTP server.
type Server struct {
	svc       Service
	opts      Options
	logger    *log.Logger
	sessions  *sessions
	limiter   *failureLimiter
	templates map[string]*template.Template
	static    http.Handler
	routes    map[string]route
	editor    Editor
	resolver  Resolver
	maint     Maintenance
	op        Operator
	pub       Publisher
	pend      PendingProvider
	pendCache pendingCache
	now       func() time.Time
	fresh     freshnessCache
	dynamic   *http.ServeMux
	http      *http.Server

	mu    sync.RWMutex
	hosts map[string]struct{}
}

type route struct {
	methods []string
	handler http.HandlerFunc
	public  bool
}

// New validates the options and builds the server.
func New(svc Service, opts Options) (*Server, error) {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address %q: %w", opts.Addr, err)
	}
	if err := ValidateListen(host, opts.AllowRemote, opts.Token); err != nil {
		return nil, err
	}
	if len(opts.Token) < MinTokenLength {
		return nil, fmt.Errorf("auth token must be at least %d characters", MinTokenLength)
	}
	hosts, err := allowedHosts(opts.Addr)
	if err != nil {
		return nil, err
	}
	out := opts.Log
	if out == nil {
		out = io.Discard
	}

	s := &Server{
		svc:      svc,
		opts:     opts,
		logger:   log.New(out, "admin: ", log.LstdFlags),
		sessions: newSessions(),
		limiter:  &failureLimiter{now: time.Now},
		hosts:    hosts,
		now:      opts.Now,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.templates, err = parseTemplates(); err != nil {
		return nil, err
	}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	s.static = http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	s.routes = map[string]route{
		"/":               {methods: []string{http.MethodGet}, handler: s.handleDashboard},
		"/artists":        {methods: []string{http.MethodGet}, handler: s.handleArtists},
		"/groups":         {methods: []string{http.MethodGet}, handler: s.handleGroups},
		"/review":         {methods: []string{http.MethodGet}, handler: s.handleReview},
		"/operations":     {methods: []string{http.MethodGet}, handler: s.handleOperations},
		"/logout":         {methods: []string{http.MethodPost}, handler: s.handleLogout},
		"/api/v1/status":  {methods: []string{http.MethodGet}, handler: s.apiDashboard},
		"/api/v1/artists": {methods: []string{http.MethodGet}, handler: s.apiArtists},
		"/api/v1/groups":  {methods: []string{http.MethodGet}, handler: s.apiGroups},
		"/api/v1/review":  {methods: []string{http.MethodGet}, handler: s.apiReview},
		"/auth":           {methods: []string{http.MethodGet}, handler: s.handleAuth, public: true},
		"/login":          {methods: []string{http.MethodGet, http.MethodPost}, handler: s.handleLogin, public: true},
	}
	if e, ok := svc.(Editor); ok {
		s.editor = e
		s.registerEditor()
	}
	if rs, ok := svc.(Resolver); ok && rs.Resolvable() {
		s.resolver = rs
		s.registerResolver()
	}
	if m, ok := svc.(Maintenance); ok {
		s.maint = m
		s.registerMaintenance()
	}
	if o, ok := svc.(Operator); ok {
		s.op = o
		s.registerOperator()
	}
	s.http = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          s.logger,
	}
	return s, nil
}

// HTTPServer exposes the configured net/http server (timeouts, limits).
func (s *Server) HTTPServer() *http.Server { return s.http }

// Serve serves on ln until ctx is cancelled, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if _, port, err := net.SplitHostPort(ln.Addr().String()); err == nil {
		s.mu.Lock()
		host, _, _ := net.SplitHostPort(s.opts.Addr)
		s.hosts[strings.ToLower(joinHostPort(host, port))] = struct{}{}
		if isLoopbackHost(host) {
			for _, h := range []string{"127.0.0.1", "localhost", "::1"} {
				s.hosts[strings.ToLower(joinHostPort(h, port))] = struct{}{}
			}
		}
		s.mu.Unlock()
	}

	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			_ = s.http.Close()
			return fmt.Errorf("shutdown admin server: %w", err)
		}
		<-errc
		return nil
	}
}

func (s *Server) hostAllowed(host string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.hosts[strings.ToLower(host)]
	return ok
}

func (s *Server) allowedSnapshot() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[string]struct{}, len(s.hosts))
	for k := range s.hosts {
		cp[k] = struct{}{}
	}
	return cp
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Handler returns the full middleware chain.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w := &statusWriter{ResponseWriter: rw, status: http.StatusOK}
		// Only the path is logged: launch tokens travel in the query string.
		defer func() { s.logger.Printf("%s %s %d", r.Method, r.URL.Path, w.status) }()

		setSecurityHeaders(w.Header())
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

		if !s.hostAllowed(r.Host) {
			s.fail(w, r, http.StatusForbidden, "forbidden_host", "request host is not allowed")
			return
		}
		if !isSafeMethod(r.Method) && !sameOrigin(r, s.allowedSnapshot()) {
			s.fail(w, r, http.StatusForbidden, "forbidden_origin", "cross-origin request rejected")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/static/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.fail(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return
			}
			s.static.ServeHTTP(w, r)
			return
		}

		rt, known := s.routes[r.URL.Path]
		if !known || !rt.public {
			if !s.sessions.valid(s.sessionID(r)) {
				s.fail(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			}
		}
		if !known && s.dynamic != nil {
			if _, pattern := s.dynamic.Handler(r); pattern != "" {
				s.dynamic.ServeHTTP(w, r)
				return
			}
		}
		if !known {
			s.fail(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		if !methodAllowed(rt.methods, r.Method) {
			w.Header().Set("Allow", strings.Join(rt.methods, ", "))
			s.fail(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		rt.handler(w, r)
	})
}

func methodAllowed(methods []string, m string) bool {
	if m == http.MethodHead {
		m = http.MethodGet
	}
	for _, allowed := range methods {
		if allowed == m {
			return true
		}
	}
	return false
}

func (s *Server) sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}
