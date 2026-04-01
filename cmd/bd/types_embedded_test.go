//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdTypes runs "bd types" with the given args and returns raw stdout.
func bdTypes(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"types"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd types %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdTypesJSON runs "bd types --json" and parses the result.
func bdTypesJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"types", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd types --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in types output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse types JSON: %v\n%s", err, s)
	}
	return m
}

func TestEmbeddedTypes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ty")

	// ===== Default output lists core types =====

	t.Run("default_output_core_types", func(t *testing.T) {
		out := bdTypes(t, bd, dir)
		if !strings.Contains(out, "Core work types") {
			t.Errorf("expected 'Core work types' header: %s", out)
		}
		for _, typeName := range []string{"task", "bug", "feature", "chore", "epic", "decision"} {
			if !strings.Contains(out, typeName) {
				t.Errorf("expected '%s' in types output: %s", typeName, out)
			}
		}
	})

	t.Run("default_output_descriptions", func(t *testing.T) {
		out := bdTypes(t, bd, dir)
		// Should show descriptions alongside types
		if !strings.Contains(out, "General work item") {
			t.Errorf("expected task description in output: %s", out)
		}
		if !strings.Contains(out, "Bug report") {
			t.Errorf("expected bug description in output: %s", out)
		}
	})

	// ===== --json output =====

	t.Run("json_output_structure", func(t *testing.T) {
		m := bdTypesJSON(t, bd, dir)
		coreTypes, ok := m["core_types"].([]interface{})
		if !ok {
			t.Fatal("expected 'core_types' array in JSON output")
		}
		if len(coreTypes) < 6 {
			t.Errorf("expected at least 6 core types, got %d", len(coreTypes))
		}
	})

	t.Run("json_core_type_fields", func(t *testing.T) {
		m := bdTypesJSON(t, bd, dir)
		coreTypes := m["core_types"].([]interface{})
		for _, ct := range coreTypes {
			ctm := ct.(map[string]interface{})
			if _, ok := ctm["name"]; !ok {
				t.Error("expected 'name' key in core type")
			}
			if _, ok := ctm["description"]; !ok {
				t.Error("expected 'description' key in core type")
			}
		}
	})

	t.Run("json_has_all_core_types", func(t *testing.T) {
		m := bdTypesJSON(t, bd, dir)
		coreTypes := m["core_types"].([]interface{})
		names := make(map[string]bool)
		for _, ct := range coreTypes {
			ctm := ct.(map[string]interface{})
			names[ctm["name"].(string)] = true
		}
		for _, expected := range []string{"task", "bug", "feature", "chore", "epic", "decision"} {
			if !names[expected] {
				t.Errorf("expected core type '%s' in JSON output", expected)
			}
		}
	})

	// ===== Custom types =====

	t.Run("no_custom_types_message", func(t *testing.T) {
		out := bdTypes(t, bd, dir)
		if !strings.Contains(out, "No custom types") {
			t.Errorf("expected 'No custom types' message: %s", out)
		}
	})

	t.Run("custom_types_from_config", func(t *testing.T) {
		// Set custom types via bd config
		cmd := exec.Command(bd, "config", "set", "types.custom", "spike,research,ops")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd config set failed: %v\n%s", err, out)
		}

		m := bdTypesJSON(t, bd, dir)
		customTypes, ok := m["custom_types"].([]interface{})
		if !ok || len(customTypes) == 0 {
			t.Error("expected custom_types in JSON after config set")
			return
		}
		names := make(map[string]bool)
		for _, ct := range customTypes {
			names[ct.(string)] = true
		}
		for _, expected := range []string{"spike", "research", "ops"} {
			if !names[expected] {
				t.Errorf("expected custom type '%s' in output", expected)
			}
		}
	})

	t.Run("custom_types_in_text_output", func(t *testing.T) {
		out := bdTypes(t, bd, dir)
		if !strings.Contains(out, "custom types") {
			t.Errorf("expected custom types section: %s", out)
		}
	})
}

// TestEmbeddedTypesConcurrent exercises types operations concurrently.
func TestEmbeddedTypesConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tyc")

	const numWorkers = 8

	type workerResult struct {
		worker int
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			// Alternate between JSON and plain text
			var args []string
			if worker%2 == 0 {
				args = []string{"types", "--json"}
			} else {
				args = []string{"types"}
			}

			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d types: %v\n%s", worker, err, out)
				results[worker] = r
				return
			}

			// For JSON workers, verify parse
			if worker%2 == 0 {
				s := strings.TrimSpace(string(out))
				start := strings.Index(s, "{")
				if start < 0 {
					r.err = fmt.Errorf("worker %d: no JSON: %s", worker, s)
					results[worker] = r
					return
				}
				var m map[string]interface{}
				if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
					r.err = fmt.Errorf("worker %d: JSON parse: %v", worker, err)
					results[worker] = r
					return
				}
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
