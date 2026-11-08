// Package pushstate reads the local Dolt push bookkeeping file
// (.beads/push-state.json) shared by bd's push commands and bd doctor.
//
// The file is local-only on purpose: keeping it out of the Dolt metadata table
// avoids merge conflicts on multi-machine setups (GH#2466).
package pushstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// FileName is the push-state file's name inside the .beads directory.
const FileName = "push-state.json"

// State is the push bookkeeping for the default (origin) Dolt remote.
//
// LastPush and LastCommit describe the last SUCCESSFUL push only. A failed
// auto-push records LastAttempt and LastError instead, so a failure never
// reads as a success watermark (GH#6289). A successful push sets both
// timestamps and clears LastError.
type State struct {
	LastPush    string `json:"last_push"`              // RFC3339 time of the last successful push
	LastCommit  string `json:"last_commit"`            // Dolt commit hash the last successful push published
	LastAttempt string `json:"last_attempt,omitempty"` // RFC3339 time of the last push attempt, failed or not
	LastError   string `json:"last_error,omitempty"`   // why the last attempt failed; empty after a success
}

// Path returns the push-state file path for beadsDir.
func Path(beadsDir string) string {
	return filepath.Join(beadsDir, FileName)
}

// Load reads beadsDir's push-state file. A missing file returns (nil, nil);
// a file that cannot be read or parsed returns the error.
func Load(beadsDir string) (*State, error) {
	data, err := os.ReadFile(Path(beadsDir)) //nolint:gosec // path is constructed internally
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ps State
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, err
	}
	return &ps, nil
}
