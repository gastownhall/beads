//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// purgeScenarioRunner drives one storage mode for the purge retention
// scenario. run never fatals: several steps assert a refusal.
type purgeScenarioRunner struct {
	mode string
	run  func(t *testing.T, args ...string) (stdout, stderr string, err error)
}

func (r purgeScenarioRunner) must(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := r.run(t, args...)
	if err != nil {
		t.Fatalf("%s: bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			r.mode, strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func (r purgeScenarioRunner) create(t *testing.T, title string, args ...string) string {
	t.Helper()
	id := strings.TrimSpace(r.must(t, append([]string{"create", title, "--silent"}, args...)...))
	if id == "" {
		t.Fatalf("%s: bd create %q printed no id", r.mode, title)
	}
	return id
}

func (r purgeScenarioRunner) exists(t *testing.T, id string) bool {
	t.Helper()
	_, _, err := r.run(t, "show", id)
	return err == nil
}

// purgeJSON runs a purge that must succeed and decodes its --json object.
func (r purgeScenarioRunner) purgeJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out := r.must(t, append(append([]string{"purge"}, args...), "--json")...)
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("%s: bd purge %s --json printed no object:\n%s", r.mode, strings.Join(args, " "), out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &result); err != nil {
		t.Fatalf("%s: parse purge JSON: %v\n%s", r.mode, err, out)
	}
	return result
}

func jsonCount(result map[string]any, key string) int {
	n, _ := result[key].(float64)
	return int(n)
}

// runPurgeRetentionScenario is the retention sweep an orchestrator runs over
// the wisps plane — `bd purge --wisps-plane --older-than <age> --force` — and
// the three things it has to get right:
//
//   - the wisps PLANE is the selection, so a closed --no-history bead goes
//     with the closed ephemeral wisp while a closed durable bead stays;
//   - a closed wisp a live wisp depends on (parent-child, tracks) is kept and
//     counted, on the default purge as much as the plane one;
//   - --older-than has sub-day precision: rows closed seconds ago are swept
//     by "1s" and kept by "36h" (which used to be floored to 24h), and an
//     out-of-range value is refused rather than wrapped;
//   - --limit drains a backlog in bounded runs, reporting remaining/has_more.
func runPurgeRetentionScenario(t *testing.T, r purgeScenarioRunner) {
	t.Helper()

	wisp := r.create(t, "closed wisp", "--ephemeral")
	noHistory := r.create(t, "closed no-history bead", "--no-history")
	durable := r.create(t, "closed durable bead")

	parent := r.create(t, "closed molecule root", "--ephemeral")
	child := r.create(t, "open step", "--ephemeral")
	r.must(t, "dep", "add", child, parent, "--type", "parent-child")

	tracked := r.create(t, "closed tracked wisp", "--ephemeral")
	convoy := r.create(t, "live convoy", "--ephemeral")
	r.must(t, "dep", "add", convoy, tracked, "--type", "tracks")
	r.must(t, "update", convoy, "--status", "in_progress")

	r.must(t, "close", wisp, noHistory, durable, tracked)
	r.must(t, "close", parent, "--force")

	// Everything above closed within the last few seconds. closed_at is
	// stored at second precision, so wait comfortably past one second before
	// asking "--older-than 1s" for it.
	time.Sleep(2500 * time.Millisecond)

	// The plane selector reaches durable-tier rows, so it takes the durable
	// tier's require-a-filter gate.
	if stdout, stderr, err := r.run(t, "purge", "--wisps-plane", "--force"); err == nil ||
		!strings.Contains(stdout+stderr, "requires --older-than or --pattern") {
		t.Fatalf("%s: unfiltered purge --wisps-plane: err=%v\n%s%s", r.mode, err, stdout, stderr)
	}

	// Hour precision: nothing here is 36 hours old.
	if got := r.purgeJSON(t, "--wisps-plane", "--older-than", "36h", "--dry-run"); jsonCount(got, "purge_count") != 0 {
		t.Fatalf("%s: --older-than 36h selected rows closed seconds ago: %v", r.mode, got)
	}

	preview := r.purgeJSON(t, "--wisps-plane", "--older-than", "1s", "--dry-run")
	if jsonCount(preview, "purge_count") != 2 || jsonCount(preview, "live_dependent_skipped") != 2 {
		t.Fatalf("%s: dry run = %v; want purge_count 2 (wisp + no-history) and live_dependent_skipped 2 (root + tracked)",
			r.mode, preview)
	}

	// --limit drains in bounded runs and says whether more remain.
	first := r.purgeJSON(t, "--wisps-plane", "--older-than", "1s", "--limit", "1", "--force")
	if jsonCount(first, "purged_count") != 1 || jsonCount(first, "remaining") != 1 || first["has_more"] != true {
		t.Fatalf("%s: purge --limit 1 = %v; want purged_count 1, remaining 1, has_more true", r.mode, first)
	}
	result := r.purgeJSON(t, "--wisps-plane", "--older-than", "1s", "--limit", "1", "--force")
	if jsonCount(result, "purged_count") != 1 || jsonCount(result, "live_dependent_skipped") != 2 ||
		jsonCount(result, "remaining") != 0 || result["has_more"] != false {
		t.Fatalf("%s: second purge --limit 1 = %v; want purged_count 1, live_dependent_skipped 2, remaining 0, has_more false",
			r.mode, result)
	}
	if stdout, stderr, err := r.run(t, "purge", "--limit", "-1", "--force"); err == nil {
		t.Fatalf("%s: purge --limit -1 succeeded:\n%s%s", r.mode, stdout, stderr)
	}
	if stdout, stderr, err := r.run(t, "purge", "--older-than", "213504d", "--force"); err == nil ||
		!strings.Contains(stdout+stderr, "out of range") {
		t.Fatalf("%s: purge --older-than 213504d must refuse as out of range: err=%v\n%s%s", r.mode, err, stdout, stderr)
	}
	for id, want := range map[string]bool{
		wisp: false, noHistory: false,
		parent: true, child: true, tracked: true, convoy: true, durable: true,
	} {
		if got := r.exists(t, id); got != want {
			t.Errorf("%s: after purge --wisps-plane, %s exists = %v, want %v", r.mode, id, got, want)
		}
	}

	// The default (ephemeral-tier) purge protects live dependents too, and
	// still leaves no-history beads alone.
	root2 := r.create(t, "second closed root", "--ephemeral")
	step2 := r.create(t, "second open step", "--ephemeral")
	r.must(t, "dep", "add", step2, root2, "--type", "parent-child")
	r.must(t, "close", root2, "--force")
	noHistory2 := r.create(t, "second no-history bead", "--no-history")
	r.must(t, "close", noHistory2)
	plain := r.purgeJSON(t, "--force")
	if jsonCount(plain, "live_dependent_skipped") < 1 {
		t.Errorf("%s: default purge reported no live-dependent skip: %v", r.mode, plain)
	}
	if !r.exists(t, root2) {
		t.Errorf("%s: default purge deleted %s, whose step %s is still open", r.mode, root2, step2)
	}
	if !r.exists(t, noHistory2) {
		t.Errorf("%s: default purge deleted no-history bead %s; only --wisps-plane selects it", r.mode, noHistory2)
	}
}

func embeddedPurgeScenarioRunner(bd, dir string) purgeScenarioRunner {
	return purgeScenarioRunner{
		mode: "embedded",
		run: func(t *testing.T, args ...string) (string, string, error) {
			t.Helper()
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			return stdout.String(), stderr.String(), err
		},
	}
}

func proxiedPurgeScenarioRunner(bd string, p proxiedProject) purgeScenarioRunner {
	return purgeScenarioRunner{
		mode: "proxied",
		run: func(t *testing.T, args ...string) (string, string, error) {
			t.Helper()
			return bdProxiedRunBuffers(t, bd, p.dir, args...)
		},
	}
}

func TestEmbeddedPurgeWispsPlaneRetention(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pwp")
	runPurgeRetentionScenario(t, embeddedPurgeScenarioRunner(bd, dir))
}

func TestProxiedServerPurgeWispsPlaneRetention(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "pwx")
	runPurgeRetentionScenario(t, proxiedPurgeScenarioRunner(bd, p))
}
