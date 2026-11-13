//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmbeddedCrossRigReadyUnreadableTargetKeepsSourceExcludedWithDiagnostics
// pins GH#5784 AC-3: a source whose bare cross-prefix blocker's route points
// at an unreadable rig stays out of `bd ready`, and the ready query still
// succeeds — exit 0, the target and the reason named on stderr, and in --json
// mode a parseable JSON document on stdout. Target unavailability is a
// blocking result carried in diagnostics, never an operation failure.
func TestEmbeddedCrossRigReadyUnreadableTargetKeepsSourceExcludedWithDiagnostics(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig ready tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gt")

	// The target rig exists and is readable while the edge is added, then the
	// route is repointed at a rig that does not: the stored edge stays and the
	// target becomes unreadable at read time.
	targetDir := filepath.Join(dir, "hq-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "hq")

	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	writeRoutes := func(path string) {
		t.Helper()
		if err := os.WriteFile(routesPath, []byte(`{"prefix":"hq-","path":"`+path+`"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeRoutes("hq-repo")

	foreign := bdCreate(t, bd, targetDir, "foreign blocker")
	source := bdCreate(t, bd, dir, "source with cross-rig blocker")
	unblocked := bdCreate(t, bd, dir, "locally free source")

	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, foreign.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (cross-prefix): %v\n%s", err, out)
	}

	// The target rig is no longer reachable: the route names a missing rig.
	writeRoutes("gone-rig")

	// Human mode: exit 0, the source withheld, the locally free source
	// offered, and the unresolved target + reason named on stderr.
	stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "ready")
	if err != nil {
		t.Fatalf("bd ready with an unreadable cross-rig target must still exit 0: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, source.ID) {
		t.Errorf("bd ready offered %s while its cross-rig blocker %s is unreadable:\n%s", source.ID, foreign.ID, stdout)
	}
	if !strings.Contains(stdout, unblocked.ID) {
		t.Errorf("bd ready withheld locally free %s:\n%s", unblocked.ID, stdout)
	}
	if !strings.Contains(stderr, foreign.ID) {
		t.Errorf("bd ready stderr must name the unresolved target %s:\nstdout:\n%s\nstderr:\n%s", foreign.ID, stdout, stderr)
	}
	if !strings.Contains(stderr, "cannot be read") || !strings.Contains(stderr, "remains blocking") {
		t.Errorf("bd ready stderr must state the target cannot be read and remains blocking, with the reason:\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "routes.jsonl") || !strings.Contains(stderr, "bd dep remove") {
		t.Errorf("bd ready stderr must name a route/removal remedy for the unresolved target:\nstderr:\n%s", stderr)
	}

	// JSON mode: same contract on stdout — a parseable JSON document without
	// the withheld source, and the diagnosis still on stderr.
	jsonOut, jsonErr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "ready", "--json")
	if err != nil {
		t.Fatalf("bd ready --json with an unreadable cross-rig target must still exit 0: %v\nstdout:\n%s\nstderr:\n%s", err, jsonOut, jsonErr)
	}
	// `bd ready --json` lists issues as a JSON array (unless the envelope
	// nests it), each with its "id" field.
	var listed []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &listed); err != nil {
		t.Errorf("bd ready --json stdout must be parseable JSON, got %v:\n%s", err, jsonOut)
	} else {
		for _, issue := range listed {
			if issue.ID == source.ID {
				t.Errorf("bd ready --json offered %s while its cross-rig blocker %s is unreadable:\n%s", source.ID, foreign.ID, jsonOut)
			}
		}
		found := false
		for _, issue := range listed {
			if issue.ID == unblocked.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("bd ready --json withheld locally free %s:\n%s", unblocked.ID, jsonOut)
		}
	}
	if !strings.Contains(jsonErr, foreign.ID) {
		t.Errorf("bd ready --json stderr must name the unresolved target %s:\nstderr:\n%s", foreign.ID, jsonErr)
	}
}

// bdRunWithFlockRetryBuffers is bdRunWithFlockRetry with the two output
// streams kept separate: AC-3's contract distinguishes stdout (the ready
// result, parseable JSON in --json mode) from stderr (the diagnosis of the
// unreadable target), so a combined stream cannot evidence it.
func bdRunWithFlockRetryBuffers(t *testing.T, bd, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	for attempt := 0; attempt < 10; attempt++ {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		err = cmd.Run()
		stdout, stderr = outBuf.String(), errBuf.String()
		if err == nil || !isEmbeddedLockOutput(stdout+stderr) {
			return stdout, stderr, err
		}
		t.Logf("bd %s: flock contention (attempt %d/10), retrying...", args[0], attempt+1)
		time.Sleep(time.Duration(500*(1<<min(attempt, 4))) * time.Millisecond)
	}
	return stdout, stderr, err
}
