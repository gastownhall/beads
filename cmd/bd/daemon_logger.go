package main

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// parseLogLevel converts a log level string to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// setupDaemonLogger creates a structured logger for the daemon.
// Returns the lumberjack logger (for cleanup) and the daemon logger.
//
// Parameters:
//   - logPath: path to log file (uses lumberjack for rotation)
//   - jsonFormat: if true, output JSON; otherwise text format
//   - level: log level (debug, info, warn, error)
func setupDaemonLogger(logPath string, jsonFormat bool, level slog.Level) (*lumberjack.Logger, *slog.Logger) {
	maxSizeMB := getEnvInt("BEADS_DAEMON_LOG_MAX_SIZE", 50)
	maxBackups := getEnvInt("BEADS_DAEMON_LOG_MAX_BACKUPS", 7)
	maxAgeDays := getEnvInt("BEADS_DAEMON_LOG_MAX_AGE", 30)
	compress := getEnvBool("BEADS_DAEMON_LOG_COMPRESS", true)

	logF := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		MaxAge:     maxAgeDays,
		Compress:   compress,
	}

	// Create multi-writer to log to both file and stderr (for foreground mode visibility)
	var w io.Writer = logF

	// Configure slog handler
	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if jsonFormat {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return logF, slog.New(handler)
}

// SetupStderrLogger creates a logger that writes to stderr only (no file).
// Useful for foreground mode or testing.
func SetupStderrLogger(jsonFormat bool, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if jsonFormat {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}

// newTestLogger creates a no-op logger for testing.
// Logs are discarded - use this when you don't need to verify log output.
func newTestLogger() *slog.Logger {
	return newSilentLogger()
}

// newSilentLogger creates a logger that discards all output.
// Use this for operations that need a logger but shouldn't produce output.
func newSilentLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// newTestLoggerWithWriter creates a logger that writes to the given writer.
// Use this when you need to capture and verify log output in tests.
func newTestLoggerWithWriter(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}
