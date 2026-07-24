//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerStatuses(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "sta")

	type statusesResult struct {
		BuiltInStatuses []statusInfo         `json:"built_in_statuses"`
		CustomStatuses  []types.CustomStatus `json:"custom_statuses"`
	}
	get := func(t *testing.T) statusesResult {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, "statuses", "--json")
		if err != nil {
			t.Fatalf("statuses --json: %v\n%s", err, out)
		}
		var got statusesResult
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return got
	}

	// NOTE: There is no statuses_embedded_test.go counterpart. These scenarios
	// mirror the shape of the types embedded coverage (default text output, JSON
	// field structure, custom-from-config fallback) applied to statuses.

	t.Run("built_ins", func(t *testing.T) {
		got := get(t)
		if len(got.BuiltInStatuses) != len(builtInStatuses) {
			t.Errorf("built-in statuses = %d, want %d", len(got.BuiltInStatuses), len(builtInStatuses))
		}
		if len(got.CustomStatuses) != 0 {
			t.Errorf("expected no custom statuses initially, got %v", got.CustomStatuses)
		}
	})

	t.Run("default_output_text", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "statuses")
		if err != nil {
			t.Fatalf("statuses: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "Built-in statuses") {
			t.Errorf("expected 'Built-in statuses' header: %s", s)
		}
		for _, name := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
			if !strings.Contains(s, name) {
				t.Errorf("expected status %q in output: %s", name, s)
			}
		}
		if !strings.Contains(s, "No custom statuses") {
			t.Errorf("expected 'No custom statuses' message: %s", s)
		}
	})

	t.Run("json_builtin_fields", func(t *testing.T) {
		got := get(t)
		if len(got.BuiltInStatuses) == 0 {
			t.Fatal("expected built-in statuses")
		}
		for _, s := range got.BuiltInStatuses {
			if s.Name == "" {
				t.Error("expected non-empty name in built-in status")
			}
			if s.Category == "" {
				t.Errorf("expected non-empty category for status %q", s.Name)
			}
			if s.Description == "" {
				t.Errorf("expected non-empty description for status %q", s.Name)
			}
		}
	})

	t.Run("custom_from_config_fallback", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "status.custom", "in_review:active")
		if err != nil {
			t.Fatalf("config set status.custom: %v\n%s", err, out)
		}
		got := get(t)
		var found bool
		for _, cs := range got.CustomStatuses {
			if cs.Name == "in_review" {
				found = true
				if cs.Category != types.CategoryActive {
					t.Errorf("in_review category = %q, want active", cs.Category)
				}
			}
		}
		if !found {
			t.Errorf("expected custom status in_review resolved from config, got %v", got.CustomStatuses)
		}
	})

	t.Run("custom_in_text_output", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "statuses")
		if err != nil {
			t.Fatalf("statuses: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "Custom statuses") {
			t.Errorf("expected 'Custom statuses' section after config set: %s", s)
		}
		if !strings.Contains(s, "in_review") {
			t.Errorf("expected custom status in_review in text output: %s", s)
		}
	})
}

func TestProxiedServerTypes(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "typ")

	type typesResult struct {
		CoreTypes   []typeInfo `json:"core_types"`
		CustomTypes []string   `json:"custom_types"`
	}
	get := func(t *testing.T) typesResult {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, "types", "--json")
		if err != nil {
			t.Fatalf("types --json: %v\n%s", err, out)
		}
		var got typesResult
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return got
	}

	t.Run("core_types", func(t *testing.T) {
		got := get(t)
		if len(got.CoreTypes) != len(coreWorkTypes) {
			t.Errorf("core types = %d, want %d", len(got.CoreTypes), len(coreWorkTypes))
		}
		if len(got.CoreTypes) < 6 {
			t.Errorf("expected at least 6 core types, got %d", len(got.CoreTypes))
		}
		if len(got.CustomTypes) != 0 {
			t.Errorf("expected no custom types initially, got %v", got.CustomTypes)
		}
	})

	t.Run("default_output_text", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "types")
		if err != nil {
			t.Fatalf("types: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "Core work types") {
			t.Errorf("expected 'Core work types' header: %s", s)
		}
		for _, typeName := range []string{"task", "bug", "feature", "chore", "epic", "decision"} {
			if !strings.Contains(s, typeName) {
				t.Errorf("expected %q in types output: %s", typeName, s)
			}
		}
	})

	t.Run("default_output_descriptions", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "types")
		if err != nil {
			t.Fatalf("types: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "General work item") {
			t.Errorf("expected task description in output: %s", s)
		}
		if !strings.Contains(s, "Bug report") {
			t.Errorf("expected bug description in output: %s", s)
		}
	})

	t.Run("json_core_type_fields", func(t *testing.T) {
		got := get(t)
		for _, ct := range got.CoreTypes {
			if ct.Name == "" {
				t.Error("expected non-empty 'name' in core type")
			}
			if ct.Description == "" {
				t.Errorf("expected non-empty 'description' for core type %q", ct.Name)
			}
		}
	})

	t.Run("json_has_all_core_types", func(t *testing.T) {
		got := get(t)
		names := make(map[string]bool)
		for _, ct := range got.CoreTypes {
			names[ct.Name] = true
		}
		for _, expected := range []string{"task", "bug", "feature", "chore", "epic", "decision"} {
			if !names[expected] {
				t.Errorf("expected core type %q in JSON output", expected)
			}
		}
	})

	t.Run("sections_static", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "types", "--sections", "--json")
		if err != nil {
			t.Fatalf("types --sections --json: %v\n%s", err, out)
		}
		var sections []typeSectionsInfo
		if err := json.Unmarshal(out, &sections); err != nil {
			t.Fatalf("unmarshal sections: %v\n%s", err, out)
		}
		if len(sections) != len(coreWorkTypes) {
			t.Errorf("sections entries = %d, want %d", len(sections), len(coreWorkTypes))
		}
	})

	t.Run("no_custom_types_message", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "types")
		if err != nil {
			t.Fatalf("types: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "No custom types") {
			t.Errorf("expected 'No custom types' message: %s", out)
		}
	})

	t.Run("custom_from_config_fallback", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "types.custom", "spike_x,research,ops")
		if err != nil {
			t.Fatalf("config set types.custom: %v\n%s", err, out)
		}
		got := get(t)
		names := make(map[string]bool)
		for _, ct := range got.CustomTypes {
			names[ct] = true
		}
		for _, expected := range []string{"spike_x", "research", "ops"} {
			if !names[expected] {
				t.Errorf("expected custom type %q resolved from config, got %v", expected, got.CustomTypes)
			}
		}
	})

	t.Run("custom_types_in_text_output", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "types")
		if err != nil {
			t.Fatalf("types: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "custom types") {
			t.Errorf("expected custom types section: %s", s)
		}
		if !strings.Contains(s, "research") {
			t.Errorf("expected configured custom type in text output: %s", s)
		}
	})
}
