package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

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

func main() {
	username := flag.String("username", "", "Paprika 3 username (email)")
	password := flag.String("password", "", "Paprika 3 password")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("paprika-3-mcp version %s\n", version)
		os.Exit(0)
	}

	if *username == "" || *password == "" {
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
		Password: *password,
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
