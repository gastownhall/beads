package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestHandleSchemaSkewJSON_Shape verifies the JSON written to stderr by
// handleSchemaSkewJSON has the expected shape: error (terse one-liner),
// hint (escape-hatch string), schema_skew subobject, and schema_version.
func TestHandleSchemaSkewJSON_Shape(t *testing.T) {
	skew := &schema.SchemaSkewError{DBVersion: 45, BinaryVersion: 42}

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w

	handleSchemaSkewJSON(skew)

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

	// "error" key must contain the terse Error() string.
	wantError := skew.Error()
	if got, ok := parsed["error"].(string); !ok || got != wantError {
		t.Errorf("error = %v, want %q", parsed["error"], wantError)
	}

	// "hint" key must contain EscapeHint().
	wantHint := skew.EscapeHint()
	if got, ok := parsed["hint"].(string); !ok || got != wantHint {
		t.Errorf("hint = %v, want %q", parsed["hint"], wantHint)
	}

	// "schema_version" key must be JSONSchemaVersion.
	if got, ok := parsed["schema_version"].(float64); !ok || int(got) != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", parsed["schema_version"], JSONSchemaVersion)
	}

	// "schema_skew" subobject must be present with current_version, required_version, delta.
	skewObj, ok := parsed["schema_skew"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema_skew key missing or wrong type: %T", parsed["schema_skew"])
	}
	if got, ok := skewObj["current_version"].(float64); !ok || int(got) != 45 {
		t.Errorf("schema_skew.current_version = %v, want 45", skewObj["current_version"])
	}
	if got, ok := skewObj["required_version"].(float64); !ok || int(got) != 42 {
		t.Errorf("schema_skew.required_version = %v, want 42", skewObj["required_version"])
	}
	if got, ok := skewObj["delta"].(float64); !ok || int(got) != 3 {
		t.Errorf("schema_skew.delta = %v, want 3", skewObj["delta"])
	}
}

// TestIgnoreSchemaSkewFlagRegistered verifies that --ignore-schema-skew is
// registered as a persistent flag on the root command.
func TestIgnoreSchemaSkewFlagRegistered(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("ignore-schema-skew")
	if f == nil {
		t.Fatal("--ignore-schema-skew persistent flag is not registered")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--ignore-schema-skew flag type = %q, want bool", f.Value.Type())
	}
}

// TestIgnoreSchemaSkewFlagPropagatesEnvVar verifies that PersistentPreRun
// sets BD_IGNORE_SCHEMA_SKEW=1 when --ignore-schema-skew is true, so all
// store-open paths see the escape hatch uniformly (not just checkSchemaSkew).
func TestIgnoreSchemaSkewFlagPropagatesEnvVar(t *testing.T) {
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)

	savePersistentPreRunState(t)
	old := ignoreSchemaSkew
	ignoreSchemaSkew = true
	t.Cleanup(func() { ignoreSchemaSkew = old })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}
	if err := rootCmd.PersistentPreRunE(versionCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}

	if got := os.Getenv("BD_IGNORE_SCHEMA_SKEW"); got != "1" {
		t.Errorf("BD_IGNORE_SCHEMA_SKEW = %q after PersistentPreRun with --ignore-schema-skew; want 1", got)
	}
}
