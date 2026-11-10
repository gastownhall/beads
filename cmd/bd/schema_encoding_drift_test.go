package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// driftRefusalForTest is the refusal a pre-0059 cursor over a drifted table
// returns, with the engine's stack still appended to its cause.
func driftRefusalForTest() *schema.SchemaEncodingDriftError {
	return &schema.SchemaEncodingDriftError{
		Migration:   "0059_recompute_null_gate_is_blocked.up.sql",
		Version:     59,
		CursorTable: "schema_migrations",
		Cause: errors.New("migration 0059_recompute_null_gate_is_blocked.up.sql: " +
			"Error 1105 (HY000): panic recovered: invalid hash length: 19\n" +
			"goroutine 81 [running]:\nruntime/debug.Stack()"),
	}
}

// captureDriftStdio is captureStdio serialized against the package's other
// stdio redirections.
func captureDriftStdio(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	return captureStdio(t, fn)
}

// assertDriftText checks the human rendering: the refusal block exactly once,
// with migration context, compact symptom and runbook, and no engine stack or
// generic error line.
func assertDriftText(t *testing.T, refusal *schema.SchemaEncodingDriftError, stdout, stderr string) {
	t.Helper()
	if stdout != "" {
		t.Errorf("text refusal wrote to stdout: %q", stdout)
	}
	if stderr != refusal.UserMessage() {
		t.Errorf("stderr is not exactly one refusal block:\n%s", stderr)
	}
	for _, want := range []string{
		refusal.Migration, "schema_migrations", "invalid hash length: 19", schema.SchemaEncodingDriftRecoveryURL,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("text refusal lacks %q:\n%s", want, stderr)
		}
	}
	for _, unwanted := range []string{"goroutine", "debug.Stack", "Error: ", "schema migration failed"} {
		if strings.Contains(stderr, unwanted) {
			t.Errorf("text refusal carries %q:\n%s", unwanted, stderr)
		}
	}
}

// assertDriftJSON decodes the one JSON document the refusal wrote and checks
// its schema_encoding_drift block.
func assertDriftJSON(t *testing.T, stdout, stderr string) {
	t.Helper()
	if stdout != "" {
		t.Errorf("JSON refusal wrote to stdout as well: %q", stdout)
	}
	dec := json.NewDecoder(strings.NewReader(stderr))
	var got map[string]interface{}
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, stderr)
	}
	if dec.More() {
		t.Errorf("more than one JSON document on stderr:\n%s", stderr)
	}
	msg, _ := got["error"].(string)
	if strings.Contains(msg, "goroutine") || strings.Contains(msg, "\n") {
		t.Errorf("JSON error carries the engine stack: %q", msg)
	}
	if hint, _ := got["hint"].(string); !strings.Contains(hint, schema.SchemaEncodingDriftRecoveryURL) {
		t.Errorf("JSON hint lacks the recovery URL: %q", hint)
	}
	drift, ok := got["schema_encoding_drift"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing schema_encoding_drift object:\n%s", stderr)
	}
	for key, want := range map[string]interface{}{
		"migration":     "0059_recompute_null_gate_is_blocked.up.sql",
		"version":       float64(59),
		"cursor_source": "schema_migrations",
		"symptom":       "invalid hash length: 19",
		"recovery_url":  schema.SchemaEncodingDriftRecoveryURL,
	} {
		if drift[key] != want {
			t.Errorf("schema_encoding_drift.%s = %#v, want %#v", key, drift[key], want)
		}
	}
}

func assertExit1(t *testing.T, err error) {
	t.Helper()
	var exit *exitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Errorf("err = %v (%T), want a silent exit 1", err, err)
	}
}

// TestRenderTypedOpenErrorSchemaEncodingDrift: the store-open paths (root
// pre-run, proxied provider, bd serve startup, the identity preview) share
// renderTypedOpenError, which must render a wrapped drift refusal in full.
func TestRenderTypedOpenErrorSchemaEncodingDrift(t *testing.T) {
	refusal := driftRefusalForTest()
	wrapped := fmt.Errorf("failed to open database: uow: init schema: %w", refusal)

	t.Run("text", func(t *testing.T) {
		pinJSONOutput(t, false)
		var handled bool
		stdout, stderr := captureDriftStdio(t, func() { handled = renderTypedOpenError(wrapped) })
		if !handled {
			t.Fatal("renderTypedOpenError did not handle a wrapped SchemaEncodingDriftError")
		}
		assertDriftText(t, refusal, stdout, stderr)
	})

	t.Run("json", func(t *testing.T) {
		pinJSONOutput(t, true)
		var handled bool
		stdout, stderr := captureDriftStdio(t, func() { handled = renderTypedOpenError(wrapped) })
		if !handled {
			t.Fatal("renderTypedOpenError did not handle a wrapped SchemaEncodingDriftError")
		}
		assertDriftJSON(t, stdout, stderr)
	})

	// refuseUnverifiablePreviewOpen is a real caller that turns the rendered
	// refusal into the command's exit status.
	t.Run("caller exits 1 after one refusal", func(t *testing.T) {
		pinJSONOutput(t, false)
		var err error
		stdout, stderr := captureDriftStdio(t, func() { err = refuseUnverifiablePreviewOpen(wrapped) })
		assertExit1(t, err)
		assertDriftText(t, refusal, stdout, stderr)
	})
}

// driftMigratorStore is a store whose explicit schema migration stops on a
// drift refusal, wrapped the way the storage layer wraps MigrateUp errors.
type driftMigratorStore struct {
	storage.DoltStorage
	err error
}

func (s *driftMigratorStore) ApplySchemaMigrations(context.Context) (int, error) { return 0, s.err }

// TestSchemaEncodingDriftMigrateRendering: `bd migrate schema` renders the
// same refusal instead of its generic "schema migration failed" error or
// JSON, and exits 1.
func TestSchemaEncodingDriftMigrateRendering(t *testing.T) {
	refusal := driftRefusalForTest()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	oldStore := store
	store = &driftMigratorStore{err: fmt.Errorf("apply schema migrations: %w", refusal)}
	t.Cleanup(func() { store = oldStore })

	for _, jsonOn := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOn), func(t *testing.T) {
			pinJSONOutput(t, jsonOn)
			var err error
			stdout, stderr := captureDriftStdio(t, func() { err = handleSchemaMigrate() })
			assertExit1(t, err)
			if jsonOn {
				assertDriftJSON(t, stdout, stderr)
			} else {
				assertDriftText(t, refusal, stdout, stderr)
			}
		})
	}
}
