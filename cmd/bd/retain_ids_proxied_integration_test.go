//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProxiedServerRetainIDs is TestEmbeddedRetainIDs's create half on the
// proxied route: `bd prune --retain-ids` retires the id it deletes, and
// `bd create --id` of it then fails with the same typed idempotency_expired
// envelope (--json) and the same never-runs-again sentence (text) the direct
// and http routes render, exit non-zero either way.
func TestProxiedServerRetainIDs(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "pr")
	keyed := bdProxiedCreate(t, bd, p.dir, "retire me", "--id", "pr-keyed1").ID
	if out, err := bdProxiedRun(t, bd, p.dir, "close", keyed); err != nil {
		t.Fatalf("bd close %s: %v\n%s", keyed, err, out)
	}
	out, err := bdProxiedRun(t, bd, p.dir, "prune", "--pattern", keyed, "--retain-ids", "--force", "--json")
	if err != nil {
		t.Fatalf("bd prune --retain-ids: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"retain_ids": true`) {
		t.Fatalf("bd prune --retain-ids --json = %s, want retain_ids true", out)
	}

	t.Run("json envelope", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "create", "again", "--id", keyed, "--json")
		if err == nil {
			t.Fatalf("bd create --id %s --json succeeded; a retired id must be refused:\n%s", keyed, stdout)
		}
		start := strings.Index(stdout, "{")
		if start < 0 {
			t.Fatalf("refusal printed no JSON envelope on stdout:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout[start:]), &envelope); err != nil {
			t.Fatalf("refusal is not a JSON envelope: %v\n%s", err, stdout)
		}
		if envelope["code"] != "idempotency_expired" || envelope["id"] != keyed {
			t.Errorf("envelope = %v, want code idempotency_expired and id %s", envelope, keyed)
		}
		if closedAt, _ := envelope["closed_at"].(string); closedAt == "" {
			t.Errorf("envelope carries no closed_at: %v", envelope)
		}
	})

	t.Run("text", func(t *testing.T) {
		_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "create", "again", "--id", keyed)
		if err == nil || !strings.Contains(stderr, "retired by a retention sweep") || !strings.Contains(stderr, "never runs again") {
			t.Errorf("bd create --id %s: err=%v stderr=%q, want the never-runs-again refusal", keyed, err, stderr)
		}
	})
}
