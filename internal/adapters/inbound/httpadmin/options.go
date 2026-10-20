package httpadmin

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// MinTokenLength is the shortest accepted session token. Generated launch
// tokens are far longer; the floor protects operator-supplied remote tokens.
const MinTokenLength = 32

// Options configures the admin server.
type Options struct {
	// Addr is the listen address (host:port). Requests must carry a matching
	// Host header, which defeats DNS rebinding.
	Addr string
	// Token is the shared secret exchanged for a session cookie.
	Token string
	// AllowRemote permits a non-loopback Addr. It is an explicit opt-in.
	AllowRemote bool
	// Log receives access and error lines. Nil discards them.
	Log io.Writer
}

// ValidateListen rejects any bind that could expose the admin beyond the
// machine unless the operator explicitly opted in and supplied a strong token.
func ValidateListen(host string, allowRemote bool, token string) error {
	if host == "" {
		return errors.New("host must not be empty (an empty host binds every interface)")
	}
	if isLoopbackHost(host) {
		return nil
	}
	if !allowRemote {
		return fmt.Errorf("refusing to bind non-loopback host %q: pass --allow-remote and provide an auth token to opt in", host)
	}
	if len(token) < MinTokenLength {
		return fmt.Errorf("non-loopback host %q requires an auth token of at least %d characters", host, MinTokenLength)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback() && !ip.IsUnspecified()
}

// allowedHosts lists the Host header values accepted for addr.
func allowedHosts(addr string) (map[string]struct{}, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	hosts := map[string]struct{}{}
	add := func(h string) { hosts[strings.ToLower(joinHostPort(h, port))] = struct{}{} }
	if isLoopbackHost(host) {
		for _, h := range []string{"127.0.0.1", "localhost", "::1"} {
			add(h)
		}
	}
	add(host)
	return hosts, nil
}

func joinHostPort(host, port string) string {
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}
