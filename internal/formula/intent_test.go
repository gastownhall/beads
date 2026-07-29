package formula

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFormulaIntentRoundTrip verifies that the intent field survives a
// TOML→Formula→JSON round-trip (presence case) and is correctly absent
// when not declared (omitempty case).
func TestFormulaIntentRoundTrip(t *testing.T) {
	t.Run("intent_present", func(t *testing.T) {
		tomlData := []byte(`
formula     = "mol-intent-test"
description = "round-trip test"
intent      = "mail_only"

[[steps]]
id    = "s1"
title = "Step one"
type  = "task"
`)
		p := NewParser()
		f, err := p.ParseTOML(tomlData)
		if err != nil {
			t.Fatalf("ParseTOML failed: %v", err)
		}
		if f.Intent != "mail_only" {
			t.Errorf("Intent after ParseTOML = %q, want %q", f.Intent, "mail_only")
		}

		jsonBytes, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		jsonStr := string(jsonBytes)
		if !strings.Contains(jsonStr, `"intent":"mail_only"`) {
			t.Errorf("JSON does not contain intent field; got:\n%s", jsonStr)
		}
	})

	t.Run("intent_absent", func(t *testing.T) {
		tomlData := []byte(`
formula     = "mol-no-intent"
description = "no intent declared"

[[steps]]
id    = "s1"
title = "Step one"
type  = "task"
`)
		p := NewParser()
		f, err := p.ParseTOML(tomlData)
		if err != nil {
			t.Fatalf("ParseTOML failed: %v", err)
		}
		if f.Intent != "" {
			t.Errorf("Intent = %q, want empty string when not declared", f.Intent)
		}

		jsonBytes, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		jsonStr := string(jsonBytes)
		if strings.Contains(jsonStr, `"intent"`) {
			t.Errorf("JSON should NOT contain intent field when absent; got:\n%s", jsonStr)
		}
	})
}
