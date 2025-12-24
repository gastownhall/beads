package main

import (
	"fmt"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// daemonLogger wraps a logging function for the daemon
type daemonLogger struct {
	logFunc func(string, ...interface{})
}

func (d *daemonLogger) log(format string, args ...interface{}) {
	d.logFunc(format, args...)
}

// setupDaemonLogger creates a rotating log file logger for the daemon
func setupDaemonLogger(logPath string) (*lumberjack.Logger, daemonLogger) {
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

	logger := daemonLogger{
		logFunc: func(format string, args ...interface{}) {
			msg := fmt.Sprintf(format, args...)
			timestamp := time.Now().Format("2006-01-02 15:04:05")
			_, _ = fmt.Fprintf(logF, "[%s] %s\n", timestamp, msg)
		},
	}

	return logF, logger
}
