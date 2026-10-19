package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/soggycactus/paprika-3-mcp/internal/mcpserver"
	"gopkg.in/natefinch/lumberjack.v2"
)

var version = "dev" // set during build with -ldflags

func getLogFilePath() string {
	switch runtime.GOOS {
	case "darwin": // macOS
		return filepath.Join(os.Getenv("HOME"), "Library", "Logs", "paprika-3-mcp", "server.log")
	case "linux":
		return "/var/log/paprika-3-mcp/server.log"
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "paprika-3-mcp", "server.log")
	default:
		// fallback to /tmp for unknown OS
		return "/tmp/paprika-3-mcp/server.log"
	}
}

// getCachePath returns where the recipe cache is stored, one file per account
func getCachePath(username string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(username))
	return filepath.Join(dir, "paprika-3-mcp", fmt.Sprintf("recipes-%s.json", hex.EncodeToString(sum[:])[:12]))
}

// readKeychain is replaced in tests
var readKeychain = func(ctx context.Context, service string) (string, error) {
	// The password reaches us through stdout, so it never shows up in a process listing
	out, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("reading keychain item %q: %w", service, err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// resolvePassword picks the password from the macOS keychain, the PAPRIKA_PASSWORD environment variable
// or the --password flag, in that order. The flag is visible to every local process via ps; avoid it.
func resolvePassword(keychainService, flagPassword string) (string, error) {
	if keychainService != "" {
		if runtime.GOOS != "darwin" {
			return "", errors.New("--keychain-service is only supported on macOS")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return readKeychain(ctx, keychainService)
	}
	if env := os.Getenv("PAPRIKA_PASSWORD"); env != "" {
		return env, nil
	}
	return flagPassword, nil
}

func main() {
	username := flag.String("username", "", "Paprika 3 username (email)")
	password := flag.String("password", "", "Paprika 3 password (deprecated: visible in process listings, use --keychain-service or PAPRIKA_PASSWORD)")
	keychainService := flag.String("keychain-service", "", "macOS keychain service name holding the Paprika 3 password (generic password item)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("paprika-3-mcp version %s\n", version)
		os.Exit(0)
	}

	pw, err := resolvePassword(*keychainService, *password)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *username == "" || pw == "" {
		fmt.Fprintln(os.Stderr, "username and password are required")
		os.Exit(1)
	}

	logFile := getLogFilePath()
	writer := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    100,  // megabytes
		MaxBackups: 5,    // keep 5 old log files
		MaxAge:     10,   // days
		Compress:   true, // gzip old logs
	}

	logger := slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	s, err := mcpserver.NewServer(mcpserver.NewServerOptions{
		Version:  version,
		Username: *username,
		Password: pw,
		Logger:   logger,
		// Recipes only, never credentials
		CachePath: getCachePath(*username),
	})
	if err != nil {
		logger.Error("failed to start paprika-3-mcp server", "err", err)
		os.Exit(1)
	}

	logger.Info("starting mcp server", "version", version)

	s.Start()
}
