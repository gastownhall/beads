//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerState(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("state_query", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "st")
		issue := bdProxiedCreate(t, bd, p.dir, "State target")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "patrol:active")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "state", issue.ID, "patrol")
		if err != nil {
			t.Fatalf("state failed: %v\nstderr:\n%s", err, stderr)
		}
		if strings.TrimSpace(out) != "active" {
			t.Errorf("state value = %q, want active", strings.TrimSpace(out))
		}
	})

	t.Run("state_query_unset", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "su")
		issue := bdProxiedCreate(t, bd, p.dir, "No state")
		out, _, err := bdProxiedRunBuffers(t, bd, p.dir, "state", issue.ID, "mode")
		if err != nil {
			t.Fatalf("state failed: %v", err)
		}
		if !strings.Contains(out, "no mode state set") {
			t.Errorf("expected unset message, got:\n%s", out)
		}
	})

	t.Run("state_query_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sj")
		issue := bdProxiedCreate(t, bd, p.dir, "State json")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "mode:degraded")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "state", issue.ID, "mode", "--json")
		if err != nil {
			t.Fatalf("state --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "{")
		if start < 0 {
			t.Fatalf("no JSON object in state output:\n%s", stdout)
		}
		var res map[string]interface{}
		if err := json.Unmarshal([]byte(stdout[start:]), &res); err != nil {
			t.Fatalf("parse state JSON: %v\nraw: %s", err, stdout[start:])
		}
		if res["value"] != "degraded" {
			t.Errorf("json value = %v, want degraded", res["value"])
		}
	})

	t.Run("state_list", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sl")
		issue := bdProxiedCreate(t, bd, p.dir, "State list target")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "patrol:active,mode:normal")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "state", "list", issue.ID)
		if err != nil {
			t.Fatalf("state list failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "patrol: active") || !strings.Contains(out, "mode: normal") {
			t.Errorf("expected both dimensions in output, got:\n%s", out)
		}
	})

	t.Run("state_list_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "se")
		issue := bdProxiedCreate(t, bd, p.dir, "No labels")
		out, _, err := bdProxiedRunBuffers(t, bd, p.dir, "state", "list", issue.ID)
		if err != nil {
			t.Fatalf("state list failed: %v", err)
		}
		if !strings.Contains(out, "no state labels") {
			t.Errorf("expected empty-state message, got:\n%s", out)
		}
	})

	t.Run("state_reads_wisp_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sw")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp state", "--ephemeral")
		bdProxiedLabel(t, bd, p.dir, "add", wisp.ID, "patrol:muted")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "state", wisp.ID, "patrol")
		if err != nil {
			t.Fatalf("state on wisp failed: %v\nstderr:\n%s", err, stderr)
		}
		if strings.TrimSpace(out) != "muted" {
			t.Errorf("wisp state value = %q, want muted (must read from wisp_labels)", strings.TrimSpace(out))
		}
	})
}
