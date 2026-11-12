//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCrossRigShowAndDepListRenderForeignDeps pins GH#5784 AC-6:
// a source whose stored edge names an issue another rig owns renders that
// foreign dependency where the worker looks — `bd show`'s DEPENDS ON
// section and single-anchor `bd dep list` — with its real issue fields, the
// edge type, and an explicit external marker, in text and JSON alike. An
// unreadable target keeps the existing truthful diagnostics and no issue row
// is fabricated for it.
func TestEmbeddedCrossRigShowAndDepListRenderForeignDeps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig display tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gt")

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

	foreign := bdCreate(t, bd, targetDir, "foreign dep target")
	source := bdCreate(t, bd, dir, "source with foreign dep")
	unreadable := bdCreate(t, bd, dir, "source with unreadable foreign dep")
	localPeer := bdCreate(t, bd, dir, "local peer")

	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, foreign.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (foreign): %v\n%s", err, out)
	}
	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", unreadable.ID, foreign.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (unreadable source): %v\n%s", err, out)
	}
	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, localPeer.ID, "--type", "tracks"); err != nil {
		t.Fatalf("bd link (local): %v\n%s", err, out)
	}

	t.Run("show human renders the foreign dep", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "show", source.ID)
		if err != nil {
			t.Fatalf("bd show: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, foreign.ID) {
			t.Errorf("bd show must render the foreign dependency %s:\n%s", foreign.ID, stdout)
		}
		if !strings.Contains(stdout, "(external)") {
			t.Errorf("bd show must mark the foreign dependency (external):\n%s", stdout)
		}
		if !strings.Contains(stdout, localPeer.ID) {
			t.Errorf("bd show must render the local dependency %s without a foreign-row marker:\n%s", localPeer.ID, stdout)
		}
	})
	t.Run("show json carries the foreign dep with external", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "show", source.ID, "--json")
		if err != nil {
			t.Fatalf("bd show --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var details []struct {
			Dependencies []struct {
				ID             string `json:"id"`
				External       bool   `json:"external"`
				DependencyType string `json:"dependency_type"`
			} `json:"dependencies"`
		}
		if err := json.Unmarshal([]byte(stdout), &details); err != nil {
			t.Fatalf("bd show --json stdout must be parseable JSON, got %v:\n%s", err, stdout)
		}
		var found bool
		for _, d := range details {
			for _, dep := range d.Dependencies {
				if dep.ID == foreign.ID {
					found = true
					if !dep.External {
						t.Errorf("foreign dependency %s must carry external: true:\n%s", foreign.ID, stdout)
					}
					if dep.DependencyType != "blocks" {
						t.Errorf("foreign dependency %s must carry its edge type, got %q:\n%s", foreign.ID, dep.DependencyType, stdout)
					}
				}
				if dep.ID == localPeer.ID && dep.External {
					t.Errorf("local dependency %s must not carry the external marker:\n%s", localPeer.ID, stdout)
				}
			}
		}
		if !found {
			t.Errorf("bd show --json must list the foreign dependency %s:\n%s", foreign.ID, stdout)
		}
	})
	t.Run("dep list single anchor renders the foreign dep", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "list", source.ID)
		if err != nil {
			t.Fatalf("bd dep list: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, foreign.ID) {
			t.Errorf("bd dep list must render the foreign dependency %s:\n%s", foreign.ID, stdout)
		}
		if !strings.Contains(stdout, "(external)") {
			t.Errorf("bd dep list must mark the foreign dependency (external):\n%s", stdout)
		}
	})
	t.Run("dep list json carries the foreign dep with external", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "list", source.ID, "--json")
		if err != nil {
			t.Fatalf("bd dep list --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var rows []struct {
			ID             string `json:"id"`
			External       bool   `json:"external"`
			DependencyType string `json:"dependency_type"`
		}
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("bd dep list --json stdout must be parseable JSON, got %v:\n%s", err, stdout)
		}
		var found bool
		for _, row := range rows {
			if row.ID == foreign.ID {
				found = true
				if !row.External || row.DependencyType != "blocks" {
					t.Errorf("foreign dependency %s must carry external: true and its edge type:\n%s", foreign.ID, stdout)
				}
			}
		}
		if !found {
			t.Errorf("bd dep list --json must list the foreign dependency %s:\n%s", foreign.ID, stdout)
		}
	})
	t.Run("unreadable target keeps truthful diagnostics", func(t *testing.T) {
		// The route named by every edge in this town now points at a rig
		// that does not exist: the stored edges stay, their target is
		// unreadable at read time.
		writeRoutes("gone-rig")

		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "list", unreadable.ID)
		if err != nil {
			t.Fatalf("bd dep list (unreadable): %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if strings.Contains(stdout, foreign.ID) {
			t.Errorf("bd dep list must not fabricate a row for an unreadable target:\n%s", stdout)
		}
		if !strings.Contains(stderr, foreign.ID) {
			t.Errorf("bd dep list must keep its dropped-edge warning naming the unreadable target:\nstderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "not shown above") {
			t.Errorf("bd dep list must keep its dropped-edge warning:\nstderr:\n%s", stderr)
		}

		jsonOut, jsonErr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "list", unreadable.ID, "--json")
		if err != nil {
			t.Fatalf("bd dep list --json (unreadable): %v\nstdout:\n%s\nstderr:\n%s", err, jsonOut, jsonErr)
		}
		if strings.Contains(jsonOut, foreign.ID) {
			t.Errorf("bd dep list --json must not fabricate a row for an unreadable target:\n%s", jsonOut)
		}
		if !strings.Contains(jsonErr, foreign.ID) {
			t.Errorf("bd dep list --json must keep its dropped-edge warning on stderr:\nstderr:\n%s", jsonErr)
		}
	})
}
