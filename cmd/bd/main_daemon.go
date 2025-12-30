package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DaemonStatus captures daemon connection state for the current command
type DaemonStatus struct {
	Mode               string `json:"mode"` // "daemon" or "direct"
	Connected          bool   `json:"connected"`
	Degraded           bool   `json:"degraded"`
	SocketPath         string `json:"socket_path,omitempty"`
	AutoStartEnabled   bool   `json:"auto_start_enabled"`
	AutoStartAttempted bool   `json:"auto_start_attempted"`
	AutoStartSucceeded bool   `json:"auto_start_succeeded"`
	FallbackReason     string `json:"fallback_reason,omitempty"` // "none","flag_no_daemon","connect_failed","health_failed","auto_start_disabled","auto_start_failed"
	Detail             string `json:"detail,omitempty"`          // short diagnostic
	Health             string `json:"health,omitempty"`          // "healthy","degraded","unhealthy"
}

// Fallback reason constants
const (
	FallbackNone              = "none"
	FallbackFlagNoDaemon      = "flag_no_daemon"
	FallbackConnectFailed     = "connect_failed"
	FallbackHealthFailed      = "health_failed"
	FallbackWorktreeSafety    = "worktree_safety"
	cmdDaemon                 = "daemon"
	cmdImport                 = "import"
	statusHealthy             = "healthy"
	FallbackAutoStartDisabled = "auto_start_disabled"
	FallbackAutoStartFailed   = "auto_start_failed"
	FallbackDaemonUnsupported = "daemon_unsupported"
	FallbackWispOperation     = "wisp_operation"
)

// Command group IDs for help organization
const (
	GroupMaintenance  = "maintenance"
	GroupIntegrations = "integrations"
)

// signalOrchestratorActivity writes an activity signal for orchestrator daemon.
// This enables exponential backoff based on bd usage detection.
// Best-effort: silent on any failure, never affects bd operation.
func signalOrchestratorActivity() {
	// Determine town root from environment
	// Priority: GT_ROOT env > skip (no default path detection)
	townRoot := os.Getenv("GT_ROOT")
	if townRoot == "" {
		return // Not in orchestrator environment, skip
	}

	// Ensure daemon directory exists
	daemonDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		return
	}

	// Build command line from os.Args
	cmdLine := strings.Join(os.Args, " ")

	// Determine actor (use package-level var if set, else fall back to env)
	actorName := actor
	if actorName == "" {
		if bdActor := os.Getenv("BD_ACTOR"); bdActor != "" {
			actorName = bdActor
		} else if user := os.Getenv("USER"); user != "" {
			actorName = user
		} else {
			actorName = "unknown"
		}
	}

	// Build activity signal
	activity := struct {
		LastCommand string `json:"last_command"`
		Actor       string `json:"actor"`
		Timestamp   string `json:"timestamp"`
	}{
		LastCommand: cmdLine,
		Actor:       actorName,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(activity)
	if err != nil {
		return
	}

	// Write atomically (write to temp, rename)
	activityPath := filepath.Join(daemonDir, "activity.json")
	tmpPath := activityPath + ".tmp"
	// nolint:gosec // G306: 0644 is appropriate for a status file
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmpPath, activityPath)
}
