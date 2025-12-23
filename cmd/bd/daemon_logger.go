package main

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// daemonLogger wraps slog for daemon logging.
// Provides level-specific methods and backward-compatible log() for migration.
type daemonLogger struct {
	logger *slog.Logger
}

// log is the backward-compatible logging method (maps to Info level).
// Use Info(), Warn(), Error(), Debug() for explicit levels.
func (d *daemonLogger) log(format string, args ...interface{}) {
	d.logger.Info(format, toSlogArgs(args)...)
}

// Info logs at INFO level.
func (d *daemonLogger) Info(msg string, args ...interface{}) {
	d.logger.Info(msg, toSlogArgs(args)...)
}

// Warn logs at WARN level.
func (d *daemonLogger) Warn(msg string, args ...interface{}) {
	d.logger.Warn(msg, toSlogArgs(args)...)
}

// Error logs at ERROR level.
func (d *daemonLogger) Error(msg string, args ...interface{}) {
	d.logger.Error(msg, toSlogArgs(args)...)
}

// Debug logs at DEBUG level.
func (d *daemonLogger) Debug(msg string, args ...interface{}) {
	d.logger.Debug(msg, toSlogArgs(args)...)
}

// toSlogArgs converts variadic args to slog-compatible key-value pairs.
// If args are already in key-value format (string, value, string, value...),
// they're passed through. Otherwise, they're wrapped as "args" for sprintf-style logs.
func toSlogArgs(args []interface{}) []any {
	if len(args) == 0 {
		return nil
	}
	// Check if args look like slog key-value pairs (string key followed by value)
	// If first arg is a string and we have pairs, treat as slog format
	if len(args) >= 2 {
		if _, ok := args[0].(string); ok {
			// Likely slog-style: "key", value, "key2", value2
			result := make([]any, len(args))
			for i, a := range args {
				result[i] = a
			}
			return result
		}
	}
	// For sprintf-style args, wrap them (caller should use fmt.Sprintf)
	result := make([]any, len(args))
	for i, a := range args {
		result[i] = a
	}
	return result
}

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
func setupDaemonLogger(logPath string, jsonFormat bool, level slog.Level) (*lumberjack.Logger, daemonLogger) {
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

	logger := daemonLogger{
		logger: slog.New(handler),
	}

	return logF, logger
}

// setupDaemonLoggerLegacy is the old signature for backward compatibility during migration.
// TODO: Remove this once all callers are updated to use the new signature.
func setupDaemonLoggerLegacy(logPath string) (*lumberjack.Logger, daemonLogger) {
	return setupDaemonLogger(logPath, false, slog.LevelInfo)
}

// SetupStderrLogger creates a logger that writes to stderr only (no file).
// Useful for foreground mode or testing.
func SetupStderrLogger(jsonFormat bool, level slog.Level) daemonLogger {
	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if jsonFormat {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return daemonLogger{
		logger: slog.New(handler),
	}
}

// newTestLogger creates a no-op logger for testing.
// Logs are discarded - use this when you don't need to verify log output.
func newTestLogger() daemonLogger {
	return daemonLogger{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// newTestLoggerWithWriter creates a logger that writes to the given writer.
// Use this when you need to capture and verify log output in tests.
func newTestLoggerWithWriter(w io.Writer) daemonLogger {
	return daemonLogger{
		logger: slog.New(slog.NewTextHandler(w, nil)),
	}
}
