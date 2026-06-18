package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestHandleRemoteMigrateGateJSON_Shape verifies the JSON written to stderr has
// the expected shape: error one-liner, hint (escape-hatch string), and a
// remote_migrate_gate subobject with current/latest/pending.
func TestHandleRemoteMigrateGateJSON_Shape(t *testing.T) {
	gate := &schema.RemoteMigrateGateError{CurrentVersion: 48, LatestVersion: 50, Pending: 2}

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w

	handleRemoteMigrateGateJSON(gate)

	_ = w.Close()
	os.Stderr = origStderr

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, buf.String())
	}

	if got, ok := parsed["error"].(string); !ok || got != gate.Error() {
		t.Errorf("error = %v, want %q", parsed["error"], gate.Error())
	}
	if got, ok := parsed["hint"].(string); !ok || got != gate.EscapeHint() {
		t.Errorf("hint = %v, want %q", parsed["hint"], gate.EscapeHint())
	}

	obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
	if !ok {
		t.Fatalf("remote_migrate_gate key missing or wrong type: %T", parsed["remote_migrate_gate"])
	}
	if got, ok := obj["current_version"].(float64); !ok || int(got) != 48 {
		t.Errorf("current_version = %v, want 48", obj["current_version"])
	}
	if got, ok := obj["latest_version"].(float64); !ok || int(got) != 50 {
		t.Errorf("latest_version = %v, want 50", obj["latest_version"])
	}
	if got, ok := obj["pending"].(float64); !ok || int(got) != 2 {
		t.Errorf("pending = %v, want 2", obj["pending"])
	}
}
