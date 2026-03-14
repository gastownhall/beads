package audit

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gastownhall/beads/internal/beads"
)

const (
	// FileName is the audit log file name stored under .beads/.
	FileName = "interactions.jsonl"
	idPrefix = "int-"
)

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

// EnsureFile creates .beads/interactions.jsonl if it does not exist.
func EnsureFile() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
		return "", fmt.Errorf("failed to create .beads directory: %w", err)
	}
	_, statErr := os.Stat(p)
	if statErr == nil {
		return p, nil
	}
	if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("failed to stat interactions log: %w", statErr)
	}
	// nolint:gosec // JSONL is intended to be shared via git across clones/tools.
	if err := os.WriteFile(p, []byte{}, 0644); err != nil {
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

	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return "", fmt.Errorf("failed to write interactions log entry: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return "", fmt.Errorf("failed to flush interactions log: %w", err)
	}

	return e.ID, nil
}

func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate id: %w", err)
	}
	return idPrefix + hex.EncodeToString(b[:]), nil
}
