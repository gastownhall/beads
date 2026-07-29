package audit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
)

const (
	// FileName is the audit log file name stored under .beads/.
	FileName = "interactions.jsonl"
	idPrefix = "int-"
)

var ensureFileBeforeCreateHook func(string)

// Entry is a generic append-only audit event. It is intentionally flexible:
// use Kind + typed fields for common cases, and Extra for everything else.
type Entry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`

	// Common metadata
	Actor   string `json:"actor,omitempty"`
	IssueID string `json:"issue_id,omitempty"`

	// LLM call
	Model    string `json:"model,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`

	// Tool call
	ToolName string `json:"tool_name,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`

	// Labeling (append-only)
	ParentID string `json:"parent_id,omitempty"`
	Label    string `json:"label,omitempty"`  // "good" | "bad" | etc
	Reason   string `json:"reason,omitempty"` // human / pipeline explanation

	Extra map[string]any `json:"extra,omitempty"`
}

func Path() (string, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return "", fmt.Errorf("no .beads directory found")
	}
	return filepath.Join(beadsDir, FileName), nil
}

// Enabled reports whether the optional JSONL interaction sidecar is enabled.
// First-class issue history is always recorded in the database events tables;
// this gate only controls .beads/interactions.jsonl.
func Enabled() bool {
	if raw, ok := os.LookupEnv("BD_AUDIT_ENABLED"); ok {
		return truthy(raw)
	}
	return config.GetBool("audit.enabled")
}

func truthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

// EnsureFile creates .beads/interactions.jsonl if it does not exist.
func EnsureFile() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return "", fmt.Errorf("failed to create .beads directory: %w", err)
	}
	if ensureFileBeforeCreateHook != nil {
		ensureFileBeforeCreateHook(p)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644) // nolint:gosec // JSONL is intended to be shared via git across clones/tools.
	if err == nil {
		if closeErr := f.Close(); closeErr != nil {
			return "", fmt.Errorf("failed to close interactions log: %w", closeErr)
		}
		return p, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("failed to create interactions log: %w", err)
	}
	return p, nil
}

// Append appends an event to .beads/interactions.jsonl as a single JSON line.
// This is intentionally append-only: callers must not mutate existing lines.
func Append(e *Entry) (string, error) {
	if e == nil {
		return "", fmt.Errorf("nil entry")
	}
	if e.Kind == "" {
		return "", fmt.Errorf("kind is required")
	}

	p, err := EnsureFile()
	if err != nil {
		return "", err
	}

	if e.ID == "" {
		e.ID, err = newID()
		if err != nil {
			return "", err
		}
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	} else {
		e.CreatedAt = e.CreatedAt.UTC()
	}

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644) // nolint:gosec // intended permissions
	if err != nil {
		return "", fmt.Errorf("failed to open interactions log: %w", err)
	}
	defer func() { _ = f.Close() }() // Best effort: file close in defer after flush

	// Marshal to a single byte slice and write atomically.
	// Using bufio.NewWriter could split into multiple write() syscalls,
	// which interleave under concurrent O_APPEND and corrupt lines.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return "", fmt.Errorf("failed to marshal interactions log entry: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return "", fmt.Errorf("failed to write interactions log entry: %w", err)
	}

	return e.ID, nil
}

// AppendIfEnabled appends only when the optional JSONL sidecar is enabled.
func AppendIfEnabled(e *Entry) (string, error) {
	if !Enabled() {
		return "", fmt.Errorf("audit JSONL sidecar is disabled; set audit.enabled=true or BD_AUDIT_ENABLED=1 to write %s", FileName)
	}
	return Append(e)
}

// LogFieldChange logs a field change (status, assignee, priority, etc.) to the
// optional JSONL sidecar when it is enabled. First-class issue history is
// recorded separately in the database events tables. Best-effort: errors are
// silently ignored so sidecar logging never blocks operations.
// Optional reason is included when non-empty (e.g., close reason, cleanup rule).
func LogFieldChange(issueID, field, oldValue, newValue, actor, reason string) {
	if oldValue == newValue {
		return
	}
	extra := map[string]any{
		"field":     field,
		"old_value": oldValue,
		"new_value": newValue,
	}
	if reason != "" {
		extra["reason"] = reason
	}
	_, _ = AppendIfEnabled(&Entry{
		Kind:    "field_change",
		IssueID: issueID,
		Actor:   actor,
		Extra:   extra,
	})
}

func newID() (string, error) {
	// 16 bytes (128-bit) of entropy — birthday probability for 8000 IDs is ~9e-32.
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate id: %w", err)
	}
	return idPrefix + hex.EncodeToString(b[:]), nil
}
