//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerSetState(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "sst")
	issue := bdProxiedCreate(t, bd, p.dir, "Set-state target")

	setStateJSON := func(t *testing.T, spec string, extra ...string) map[string]interface{} {
		t.Helper()
		args := append([]string{"set-state", issue.ID, spec, "--json"}, extra...)
		out, err := bdProxiedRun(t, bd, p.dir, args...)
		if err != nil {
			t.Fatalf("set-state %s: %v\n%s", spec, err, out)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return got
	}

	t.Run("set_new_dimension", func(t *testing.T) {
		got := setStateJSON(t, "patrol=active", "--reason", "starting patrol")
		if got["changed"] != true {
			t.Errorf("expected changed=true, got %v", got["changed"])
		}
		if got["old_value"] != nil {
			t.Errorf("expected old_value nil for a new dimension, got %v", got["old_value"])
		}
		if got["new_value"] != "active" {
			t.Errorf("new_value = %v, want active", got["new_value"])
		}
		if _, ok := got["event_id"].(string); !ok || got["event_id"] == "" {
			t.Errorf("expected an event_id, got %v", got["event_id"])
		}
		// Label cache must reflect the new state.
		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, issue.ID)
		if !containsStr(labels, "patrol:active") {
			t.Errorf("expected label patrol:active, got %v", labels)
		}
	})

	t.Run("change_swaps_label", func(t *testing.T) {
		got := setStateJSON(t, "patrol=muted")
		if got["changed"] != true || got["old_value"] != "active" || got["new_value"] != "muted" {
			t.Errorf("unexpected change result: %v", got)
		}
		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, issue.ID)
		if containsStr(labels, "patrol:active") {
			t.Errorf("old label patrol:active should have been removed, got %v", labels)
		}
		if !containsStr(labels, "patrol:muted") {
			t.Errorf("expected label patrol:muted, got %v", labels)
		}
	})

	t.Run("no_change_is_noop", func(t *testing.T) {
		got := setStateJSON(t, "patrol=muted")
		if got["changed"] != false {
			t.Errorf("expected changed=false for identical value, got %v", got)
		}
	})

	t.Run("event_bead_created_as_child", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "state", "list", issue.ID, "--json")
		if err != nil {
			t.Fatalf("state list: %v\n%s", err, out)
		}
		var res struct {
			States map[string]string `json:"states"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if res.States["patrol"] != "muted" {
			t.Errorf("state list shows patrol=%q, want muted", res.States["patrol"])
		}
		// The set-state calls created event children under the issue.
		children := bdProxiedListJSON(t, bd, p, "--parent", issue.ID, "--status", "all")
		var events int
		for _, c := range children {
			if strings.HasPrefix(c.ID, issue.ID+".") {
				events++
			}
		}
		if events == 0 {
			t.Errorf("expected event child beads under %s, got none", issue.ID)
		}
	})

	t.Run("invalid_format", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "set-state", issue.ID, "patrolactive")
		if err == nil {
			t.Fatalf("expected error for missing '=', got success: %s", out)
		}
	})

	t.Run("multiple_dimensions", func(t *testing.T) {
		m := bdProxiedCreate(t, bd, p.dir, "Multi-dimension state")

		for _, spec := range []string{"phase=planning", "env=staging"} {
			out, err := bdProxiedRun(t, bd, p.dir, "set-state", m.ID, spec)
			if err != nil {
				t.Fatalf("set-state %s: %v\n%s", spec, err, out)
			}
		}

		out, err := bdProxiedRun(t, bd, p.dir, "state", "list", m.ID, "--json")
		if err != nil {
			t.Fatalf("state list: %v\n%s", err, out)
		}
		var res struct {
			States map[string]string `json:"states"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if res.States["phase"] != "planning" {
			t.Errorf("phase = %q, want planning (states=%v)", res.States["phase"], res.States)
		}
		if res.States["env"] != "staging" {
			t.Errorf("env = %q, want staging (states=%v)", res.States["env"], res.States)
		}

		// Each distinct dimension is an independent label; both must be present.
		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, m.ID)
		if !containsStr(labels, "phase:planning") || !containsStr(labels, "env:staging") {
			t.Errorf("expected both phase:planning and env:staging labels, got %v", labels)
		}
	})

	t.Run("set_state_on_wisp", func(t *testing.T) {
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp set-state", "--ephemeral")

		out, err := bdProxiedRun(t, bd, p.dir, "set-state", wisp.ID, "patrol=muted", "--json")
		if err != nil {
			t.Fatalf("set-state on wisp: %v\n%s", err, out)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got["changed"] != true || got["new_value"] != "muted" {
			t.Errorf("unexpected wisp set-state result: %v", got)
		}

		// Wisp state lives in wisp_labels; verify via the state reader.
		val, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "state", wisp.ID, "patrol")
		if err != nil {
			t.Fatalf("state on wisp: %v\nstderr:\n%s", err, stderr)
		}
		if strings.TrimSpace(val) != "muted" {
			t.Errorf("wisp state value = %q, want muted", strings.TrimSpace(val))
		}
	})
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
