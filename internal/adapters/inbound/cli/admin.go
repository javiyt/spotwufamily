package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/bootstrap"
)

const (
	adminTokenEnv     = "SPOTWUFAMILY_ADMIN_TOKEN"
	adminUsage        = "usage: spotwufamily admin serve [--host 127.0.0.1] [--port 8787] [--catalog data/artists.yaml] [--db data/catalog.db] [--allow-remote]"
	defaultAdminHost  = "127.0.0.1"
	defaultAdminPort  = 8787
	generatedTokenLen = 32
)

type adminServeOptions struct {
	host        string
	port        int
	catalogPath string
	dbPath      string
	allowRemote bool
}

func parseAdminServeOptions(args []string) (adminServeOptions, error) {
	options := adminServeOptions{host: defaultAdminHost, port: defaultAdminPort, catalogPath: defaultCatalogPath, dbPath: defaultDatabasePath}
	for i := 0; i < len(args); i++ {
		name := args[i]
		if name == "--allow-remote" {
			options.allowRemote = true
			continue
		}
		target := map[string]*string{"--host": &options.host, "--catalog": &options.catalogPath, "--db": &options.dbPath}[name]
		switch {
		case target != nil:
			i++
			if i >= len(args) {
				return options, fmt.Errorf("%s requires a value", name)
			}
			*target = args[i]
		case name == "--port":
			i++
			if i >= len(args) {
				return options, fmt.Errorf("--port requires a value")
			}
			port, err := strconv.Atoi(args[i])
			if err != nil || port < 0 || port > 65535 {
				return options, fmt.Errorf("--port must be between 0 and 65535")
			}
			options.port = port
		default:
			return options, fmt.Errorf("unknown option %q", name)
		}
	}
	return options, nil
}

func executeAdmin(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, adminUsage)
		return 0
	}
	if args[0] != "serve" {
		_, _ = fmt.Fprintf(stderr, "unknown admin command %q\n\n%s\n", args[0], adminUsage)
		return 2
	}
	return executeAdminServe(ctx, args[1:], stdout, stderr)
}

func executeAdminServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	options, err := parseAdminServeOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "admin serve: %v\n%s\n", err, adminUsage)
		return 2
	}

	token := os.Getenv(adminTokenEnv)
	if err := httpadmin.ValidateListen(options.host, options.allowRemote, token); err != nil {
		_, _ = fmt.Fprintf(stderr, "admin serve: %v (set %s for remote access)\n", err, adminTokenEnv)
		return 2
	}
	generated := token == ""
	if generated {
		if token, err = generateToken(); err != nil {
			_, _ = fmt.Fprintf(stderr, "admin serve: %v\n", err)
			return 1
		}
	}

	addr := net.JoinHostPort(options.host, strconv.Itoa(options.port))
	svc, closeDB, err := bootstrap.NewAdminService(options.catalogPath, options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "admin serve: %v\n", err)
		return 1
	}
	defer closeDB()

	server, err := httpadmin.New(svc, httpadmin.Options{Addr: addr, Token: token, AllowRemote: options.allowRemote, Log: stderr})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "admin serve: %v\n", err)
		return 2
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "admin serve: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	base := "http://" + ln.Addr().String()
	_, _ = fmt.Fprintf(stdout, "Spot Wu Family admin listening on %s\n", base)
	if generated {
		_, _ = fmt.Fprintf(stdout, "Open: %s/auth?token=%s\n", base, token)
	} else {
		_, _ = fmt.Fprintf(stdout, "Open: %s/login (token from %s)\n", base, adminTokenEnv)
	}
	_, _ = fmt.Fprintln(stdout, "Press Ctrl+C to stop.")

	if err := server.Serve(ctx, ln); err != nil {
		_, _ = fmt.Fprintf(stderr, "admin serve: %v\n", err)
		return 1
	}
	return 0
}

func generateToken() (string, error) {
	b := make([]byte, generatedTokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
