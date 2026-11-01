//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedRetainIDs drives the retaining sweep end to end through the CLI
// on the direct route: `bd prune --retain-ids` and `bd purge --retain-ids`
// retire what they delete, `bd create --id` of a retired id fails with the
// typed idempotency_expired envelope (--json) or the never-runs-again sentence
// (text), exit 1 either way, and `bd import` skips and reports a retired row
// instead of resurrecting it — and its --dry-run preview says so too.
func TestEmbeddedRetainIDs(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rt")

	run := func(args ...string) (stdout, stderr string, exitCode int) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, errOut, err := runCommandBuffers(t, cmd)
		var exit *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exit):
			exitCode = exit.ExitCode()
		default:
			t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
		}
		return out.String(), errOut.String(), exitCode
	}

	durable := bdCreate(t, bd, dir, "retire me", "--id", "rt-keyed1").ID
	bdClose(t, bd, dir, durable)
	out, stderr, code := run("prune", "--pattern", durable, "--retain-ids", "--force", "--json")
	if code != 0 {
		t.Fatalf("bd prune --retain-ids exited %d:\n%s\n%s", code, out, stderr)
	}
	var pruned map[string]any
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &pruned); err != nil {
		t.Fatalf("bd prune --json output is not JSON: %v\n%s", err, out)
	}
	if pruned["pruned_count"] != float64(1) || pruned["retain_ids"] != true {
		t.Fatalf("bd prune --retain-ids --json = %v, want pruned_count 1 and retain_ids true", pruned)
	}

	t.Run("create of the retired id is refused with the typed envelope", func(t *testing.T) {
		out, _, code := run("create", "again", "--id", durable, "--json")
		if code != 1 {
			t.Fatalf("bd create --id %s --json exited %d, want 1:\n%s", durable, code, out)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &envelope); err != nil {
			t.Fatalf("refusal is not a JSON envelope: %v\n%s", err, out)
		}
		if envelope["code"] != "idempotency_expired" || envelope["id"] != durable {
			t.Errorf("envelope = %v, want code idempotency_expired and id %s", envelope, durable)
		}
		if closedAt, _ := envelope["closed_at"].(string); closedAt == "" {
			t.Errorf("envelope carries no closed_at: %v", envelope)
		}
	})

	t.Run("text mode says the create never runs again", func(t *testing.T) {
		_, stderr, code := run("create", "again", "--id", durable)
		if code != 1 || !strings.Contains(stderr, "retired by a retention sweep") || !strings.Contains(stderr, "never runs again") {
			t.Errorf("bd create --id %s exited %d with stderr %q", durable, code, stderr)
		}
	})

	t.Run("purge retires a wisp", func(t *testing.T) {
		wisp := bdCreate(t, bd, dir, "retire my wisp", "--id", "rt-wisp-keyed1", "--ephemeral").ID
		bdClose(t, bd, dir, wisp)
		out, stderr, code := run("purge", "--pattern", wisp, "--retain-ids", "--force")
		if code != 0 || !strings.Contains(out, "Retired 1 id(s)") {
			t.Fatalf("bd purge --retain-ids exited %d:\n%s\n%s", code, out, stderr)
		}
		if _, _, code := run("create", "again", "--id", wisp, "--ephemeral", "--json"); code != 1 {
			t.Errorf("bd create of the purged, retired wisp id exited %d, want 1", code)
		}
	})

	t.Run("import skips and reports the retired row", func(t *testing.T) {
		jsonl := filepath.Join(t.TempDir(), "retired.jsonl")
		rows := `{"id":"` + durable + `","title":"resurrect me","status":"open","priority":2,"issue_type":"task"}` + "\n" +
			`{"id":"rt-fresh1","title":"fresh","status":"open","priority":2,"issue_type":"task"}` + "\n"
		if err := os.WriteFile(jsonl, []byte(rows), 0o600); err != nil {
			t.Fatal(err)
		}
		// The preview agrees with the run it previews: the retired row is
		// reported, not counted as a create.
		out, stderr, code := run("import", jsonl, "--dry-run", "--json")
		if code != 0 {
			t.Fatalf("bd import --dry-run exited %d:\n%s\n%s", code, out, stderr)
		}
		var preview map[string]any
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &preview); err != nil {
			t.Fatalf("bd import --dry-run --json output is not JSON: %v\n%s", err, out)
		}
		previewRetired, _ := preview["retired_ids"].([]any)
		previewIDs, _ := preview["ids"].([]any)
		if len(previewRetired) != 1 || previewRetired[0] != durable || preview["created"] != float64(1) ||
			len(previewIDs) != 1 || previewIDs[0] != "rt-fresh1" {
			t.Errorf("bd import --dry-run --json = %v, want retired_ids [%s], created 1 and ids [rt-fresh1]", preview, durable)
		}
		_, stderr, code = run("import", jsonl, "--dry-run")
		if code != 0 || !strings.Contains(stderr, "1 new") || !strings.Contains(stderr, "1 retired by a retention sweep") {
			t.Errorf("bd import --dry-run exited %d with stderr %q, want 1 new and 1 retired", code, stderr)
		}

		out, stderr, code = run("import", jsonl, "--json")
		if code != 0 {
			t.Fatalf("bd import exited %d:\n%s\n%s", code, out, stderr)
		}
		var report map[string]any
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &report); err != nil {
			t.Fatalf("bd import --json output is not JSON: %v\n%s", err, out)
		}
		retired, _ := report["retired_ids"].([]any)
		if len(retired) != 1 || retired[0] != durable || report["created"] != float64(1) {
			t.Errorf("bd import --json = %v, want retired_ids [%s] and created 1", report, durable)
		}
		if _, _, code := run("show", durable, "--json"); code == 0 {
			t.Errorf("bd show %s succeeded after the import; the retired row was resurrected", durable)
		}
	})
}
