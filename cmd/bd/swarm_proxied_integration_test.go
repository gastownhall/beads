//go:build cgo

package main

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// swarmModeRunner runs the same bd binary against one project in one mode
// (classic embedded or proxied-server), so the cross-mode parity checks below
// exercise identical commands against identical fixtures and diff the output.
type swarmModeRunner struct {
	name string
	dir  string
	env  func(dir string) []string
}

func (m swarmModeRunner) run(t *testing.T, bd string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = m.dir
	cmd.Env = m.env(m.dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return stdout.String(), stderr.String(), err
}

func (m swarmModeRunner) mustRun(t *testing.T, bd string, args ...string) string {
	t.Helper()
	stdout, stderr, err := m.run(t, bd, args...)
	if err != nil {
		t.Fatalf("[%s] bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			m.name, strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func (m swarmModeRunner) mustFail(t *testing.T, bd string, args ...string) string {
	t.Helper()
	stdout, stderr, err := m.run(t, bd, args...)
	if err == nil {
		t.Fatalf("[%s] expected bd %s to fail, but it succeeded:\nstdout:\n%s\nstderr:\n%s",
			m.name, strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

// buildSwarmParityFixture creates the same epic DAG in either mode, using
// explicit IDs so the two modes' outputs are byte-comparable:
//
//	psw-epic1
//	├─ psw-c1  closed
//	├─ psw-c2  in_progress, assignee worker1
//	├─ psw-c3  open, no blockers          (ready)
//	└─ psw-c4  open, blocks-dep on psw-c3 (blocked)
func buildSwarmParityFixture(t *testing.T, bd string, m swarmModeRunner) {
	t.Helper()
	// --id and --parent cannot be combined at create, so children get their
	// parent-child edge via dep add.
	m.mustRun(t, bd, "create", "Swarm port epic", "--type", "epic", "--id", "psw-epic1")
	m.mustRun(t, bd, "create", "Child done", "--type", "task", "--id", "psw-c1")
	m.mustRun(t, bd, "create", "Child active", "--type", "task", "--id", "psw-c2")
	m.mustRun(t, bd, "create", "Child ready", "--type", "task", "--id", "psw-c3")
	m.mustRun(t, bd, "create", "Child waiting", "--type", "task", "--id", "psw-c4")
	for _, child := range []string{"psw-c1", "psw-c2", "psw-c3", "psw-c4"} {
		m.mustRun(t, bd, "dep", "add", child, "psw-epic1", "--type", "parent-child")
	}
	m.mustRun(t, bd, "dep", "add", "psw-c4", "psw-c3")
	m.mustRun(t, bd, "close", "psw-c1", "-r", "fixture: pre-closed child")
	m.mustRun(t, bd, "update", "psw-c2", "--status", "in_progress", "--assignee", "worker1")
}

// buildSwarmCycleFixture creates a second epic whose two children form a
// blocking cycle. `bd dep add` refuses to create the closing edge (the cycle
// gate is upstream of both modes), so the reverse edge is planted with raw
// SQL via `bd sql`. Proxied-only: classic embedded mode has no `bd sql`
// ("not yet supported in embedded mode") and no other CLI door past the
// cycle gate, so there is no classic run to diff against — the detector
// itself (detectStructuralIssues) is the same shared code on both routes.
func buildSwarmCycleFixture(t *testing.T, bd string, m swarmModeRunner) {
	t.Helper()
	m.mustRun(t, bd, "create", "Cycle epic", "--type", "epic", "--id", "psw-epic2")
	m.mustRun(t, bd, "create", "Cycle A", "--type", "task", "--id", "psw-c5")
	m.mustRun(t, bd, "create", "Cycle B", "--type", "task", "--id", "psw-c6")
	for _, child := range []string{"psw-c5", "psw-c6"} {
		m.mustRun(t, bd, "dep", "add", child, "psw-epic2", "--type", "parent-child")
	}
	m.mustRun(t, bd, "dep", "add", "psw-c5", "psw-c6")
	m.mustRun(t, bd, "sql",
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) "+
			"VALUES (UUID(), 'psw-c6', 'psw-c5', 'blocks', NOW(), 'tester', '{}')")
}

// parseSwarmJSON extracts the first JSON object from stdout.
func parseSwarmJSON(t *testing.T, mode, out string) map[string]interface{} {
	t.Helper()
	s := strings.TrimSpace(out)
	start := strings.IndexAny(s, "{")
	if start < 0 {
		t.Fatalf("[%s] no JSON object in swarm output: %s", mode, s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("[%s] parse swarm JSON: %v\n%s", mode, err, s)
	}
	return m
}

// stripSwarmStatusTimestamps blanks the closed_at field on every completed
// entry in a parsed `swarm status --json` document. It is the only
// wall-clock-dependent field, so the rest must match across modes exactly.
func stripSwarmStatusTimestamps(t *testing.T, doc map[string]interface{}) {
	t.Helper()
	completed, ok := doc["completed"].([]interface{})
	if !ok {
		t.Fatalf("swarm status JSON has no completed array: %v", doc)
	}
	for _, entry := range completed {
		if e, ok := entry.(map[string]interface{}); ok {
			delete(e, "closed_at")
		}
	}
}

func TestProxiedServerSwarm(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// One journey: identical fixture in a proxied-server project and a classic
	// embedded project (same binary), then every read compared across modes.
	p := newSharedProxiedProject(t, bd, "psw")
	classicDir, _, _ := bdInit(t, bd, "--prefix", "psw")

	proxied := swarmModeRunner{name: "proxied", dir: p.dir, env: bdProxiedEnv}
	classic := swarmModeRunner{name: "classic", dir: classicDir, env: bdEnv}
	modes := []swarmModeRunner{proxied, classic}

	for _, m := range modes {
		buildSwarmParityFixture(t, bd, m)
	}
	buildSwarmCycleFixture(t, bd, proxied)

	// Read-only invariant: neither swarm status nor swarm validate may write a
	// Dolt commit. Capture HEAD before the reads and re-check at the end.
	db := openProxiedDB(t, p)
	headBefore := proxiedDoltHead(t, db)

	t.Run("status_fields", func(t *testing.T) {
		out := proxied.mustRun(t, bd, "swarm", "status", "psw-epic1")
		for _, want := range []string{
			"psw-c1",
			"⟳ psw-c2 [worker1]",
			"○ psw-c3",
			"◌ psw-c4 (needs psw-c3)",
			"Progress: 1/4 complete, 1/4 active (25%)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("swarm status missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("status_matches_classic", func(t *testing.T) {
		proxiedOut := proxied.mustRun(t, bd, "swarm", "status", "psw-epic1")
		classicOut := classic.mustRun(t, bd, "swarm", "status", "psw-epic1")
		if proxiedOut != classicOut {
			t.Errorf("swarm status output diverged from classic:\nproxied:\n%s\nclassic:\n%s", proxiedOut, classicOut)
		}
	})

	t.Run("status_json_matches_classic", func(t *testing.T) {
		proxiedDoc := parseSwarmJSON(t, "proxied", proxied.mustRun(t, bd, "swarm", "status", "psw-epic1", "--json"))
		classicDoc := parseSwarmJSON(t, "classic", classic.mustRun(t, bd, "swarm", "status", "psw-epic1", "--json"))
		// closed_at is the run's wall clock; everything else must be identical.
		stripSwarmStatusTimestamps(t, proxiedDoc)
		stripSwarmStatusTimestamps(t, classicDoc)
		if !reflect.DeepEqual(proxiedDoc, classicDoc) {
			t.Errorf("swarm status --json diverged from classic:\nproxied: %v\nclassic: %v", proxiedDoc, classicDoc)
		}
		if got := proxiedDoc["total_issues"]; got != float64(4) {
			t.Errorf("total_issues = %v, want 4", got)
		}
		if got := proxiedDoc["blocked_count"]; got != float64(1) {
			t.Errorf("blocked_count = %v, want 1", got)
		}
	})

	t.Run("validate_clean_dag", func(t *testing.T) {
		out := proxied.mustRun(t, bd, "swarm", "validate", "psw-epic1")
		for _, want := range []string{
			"psw-epic1",
			"Total issues: 4 (1 closed)",
			"Swarmable: YES",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("swarm validate missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("validate_matches_classic", func(t *testing.T) {
		proxiedOut := proxied.mustRun(t, bd, "swarm", "validate", "psw-epic1")
		classicOut := classic.mustRun(t, bd, "swarm", "validate", "psw-epic1")
		if proxiedOut != classicOut {
			t.Errorf("swarm validate output diverged from classic:\nproxied:\n%s\nclassic:\n%s", proxiedOut, classicOut)
		}
	})

	t.Run("validate_json_matches_classic", func(t *testing.T) {
		proxiedDoc := parseSwarmJSON(t, "proxied", proxied.mustRun(t, bd, "swarm", "validate", "psw-epic1", "--json"))
		classicDoc := parseSwarmJSON(t, "classic", classic.mustRun(t, bd, "swarm", "validate", "psw-epic1", "--json"))
		if !reflect.DeepEqual(proxiedDoc, classicDoc) {
			t.Errorf("swarm validate --json diverged from classic:\nproxied: %v\nclassic: %v", proxiedDoc, classicDoc)
		}
		if got := proxiedDoc["swarmable"]; got != true {
			t.Errorf("swarmable = %v, want true", got)
		}
		if got := proxiedDoc["max_parallelism"]; got != float64(2) {
			t.Errorf("max_parallelism = %v, want 2", got)
		}
	})

	t.Run("validate_cycle_fails", func(t *testing.T) {
		// The cycle path's member ordering is map-iteration-dependent, so
		// this asserts the verdict, not the bytes: nonzero exit (classic's
		// SilentExit on !Swarmable), the cycle error, and the NO verdict —
		// all produced by the same shared code the classic route runs.
		out := proxied.mustFail(t, bd, "swarm", "validate", "psw-epic2")
		if !strings.Contains(out, "Dependency cycle detected") {
			t.Errorf("expected cycle error, got:\n%s", out)
		}
		if !strings.Contains(out, "Swarmable: NO") {
			t.Errorf("expected 'Swarmable: NO', got:\n%s", out)
		}
	})

	t.Run("create_and_list_still_refused", func(t *testing.T) {
		out := proxied.mustFail(t, bd, "swarm", "create", "psw-epic1")
		if !strings.Contains(out, "swarm create is not supported in proxied-server mode") {
			t.Errorf("expected swarm create refusal, got:\n%s", out)
		}
		out = proxied.mustFail(t, bd, "swarm", "list")
		if !strings.Contains(out, "swarm list is not supported in proxied-server mode") {
			t.Errorf("expected swarm list refusal, got:\n%s", out)
		}
	})

	t.Run("reads_write_no_dolt_commit", func(t *testing.T) {
		if headAfter := proxiedDoltHead(t, db); headAfter != headBefore {
			t.Errorf("swarm status/validate advanced Dolt HEAD: before=%s after=%s", headBefore, headAfter)
		}
	})
}
