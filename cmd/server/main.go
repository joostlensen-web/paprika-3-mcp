// Command server runs paprika-3-mcp as a remote MCP server over streamable HTTP, e.g. on Vercel.
//
// Configuration comes from environment variables:
//   - PAPRIKA_USERNAME and PAPRIKA_PASSWORD: the Paprika 3 account
//   - MCP_SECRET: a random string of at least 32 characters; the server only answers on /mcp/<MCP_SECRET>
//   - PORT: the port to listen on (default 3000)
package main

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/soggycactus/paprika-3-mcp/internal/mcpserver"
)

var version = "dev" // set during build with -ldflags

const minSecretLength = 32

// secretPath only passes requests for /mcp/<secret> to next. Anything else gets a 404, so the endpoint can't be discovered.
func secretPath(secret string, next http.Handler) http.Handler {
	want := []byte("/mcp/" + secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if subtle.ConstantTimeCompare([]byte(path), want) != 1 {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	username := os.Getenv("PAPRIKA_USERNAME")
	password := os.Getenv("PAPRIKA_PASSWORD")
	secret := os.Getenv("MCP_SECRET")
	if username == "" || password == "" {
		logger.Error("PAPRIKA_USERNAME and PAPRIKA_PASSWORD are required")
		os.Exit(1)
	}
	if len(secret) < minSecretLength {
		logger.Error("MCP_SECRET must be at least 32 characters", "length", len(secret))
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	s, err := mcpserver.NewServer(mcpserver.NewServerOptions{
		Version:  version,
		Username: username,
		Password: password,
		Logger:   logger,
		// Recipes only, never credentials. Serverless instances only have a writable temp dir.
		CachePath: filepath.Join(os.TempDir(), "paprika-3-mcp", "recipes.json"),
	})
	if err != nil {
		logger.Error("failed to start paprika-3-mcp server", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           secretPath(secret, s.HTTPHandler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("starting remote mcp server", "version", version, "port", port)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
