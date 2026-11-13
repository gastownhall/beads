//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCrossRigDepAddFeedbackNamesExternalStorage pins GH#5784 AC-5:
// every add form that stores a cross-prefix edge reports it as externally
// stored — `external: true` in JSON, a blocking-semantics note in human
// output — while a local edge's output stays exactly as it was. The JSON
// marker is classification's answer (the target lives in another rig's
// database), so it is set for informational types too; the human note is
// reserved for the types that actually gate readiness, and --quiet keeps it
// silent.
func TestEmbeddedCrossRigDepAddFeedbackNamesExternalStorage(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig dep add tests")
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

	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(`{"prefix":"hq-","path":"hq-repo"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	foreign := bdCreate(t, bd, targetDir, "foreign target")
	jsonOut := func(t *testing.T, args ...string) map[string]any {
		t.Helper()
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, append(args, "--json")...)
		if err != nil {
			t.Fatalf("bd %s --json: %v\nstdout:\n%s\nstderr:\n%s", args[0], err, stdout, stderr)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("bd %s --json stdout must be a JSON object, got %v:\n%s", args[0], err, stdout)
		}
		return out
	}

	t.Run("link cross-rig blocks is external", func(t *testing.T) {
		src := bdCreate(t, bd, dir, "link source")
		out := jsonOut(t, "link", src.ID, foreign.ID, "--type", "blocks")
		if out["external"] != true {
			t.Errorf("bd link --json cross-prefix edge must carry external: true, got %v:\n%v", out["external"], out)
		}
	})
	t.Run("dep add positional cross-rig blocks is external", func(t *testing.T) {
		src := bdCreate(t, bd, dir, "dep add source")
		out := jsonOut(t, "dep", "add", src.ID, foreign.ID, "--type", "blocks")
		if out["external"] != true {
			t.Errorf("bd dep add --json cross-prefix edge must carry external: true, got %v:\n%v", out["external"], out)
		}
	})
	t.Run("dep --blocks form cross-rig is external", func(t *testing.T) {
		src := bdCreate(t, bd, dir, "dep blocks source")
		out := jsonOut(t, "dep", foreign.ID, "--blocks", src.ID)
		if out["external"] != true {
			t.Errorf("bd dep --blocks --json cross-prefix edge must carry external: true, got %v:\n%v", out["external"], out)
		}
	})
	t.Run("dep add --file cross-rig blocks is external", func(t *testing.T) {
		src := bdCreate(t, bd, dir, "bulk source")
		file := filepath.Join(dir, "deps.jsonl")
		line := `{"from":"` + src.ID + `","to":"` + foreign.ID + `","type":"blocks"}` + "\n"
		if err := os.WriteFile(file, []byte(line), 0644); err != nil {
			t.Fatal(err)
		}
		out := jsonOut(t, "dep", "add", "--file", file)
		deps, ok := out["dependencies"].([]any)
		if !ok || len(deps) != 1 {
			t.Fatalf("bd dep add --file --json must list the stored edge, got %v", out)
		}
		if deps[0].(map[string]any)["external"] != true {
			t.Errorf("bulk cross-prefix edge must carry external: true, got %v", deps[0])
		}
	})
	t.Run("informational types are external storage without blocking promises", func(t *testing.T) {
		src := bdCreate(t, bd, dir, "tracks source")
		out := jsonOut(t, "link", src.ID, foreign.ID, "--type", "tracks")
		if out["external"] != true {
			t.Errorf("a tracks edge to another rig is still externally stored: external must be true, got %v", out["external"])
		}

		// Human output for the informational type must not promise gating.
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "link", src.ID, foreign.ID, "--type", "tracks")
		if err != nil {
			t.Fatalf("bd link (human): %v\n%s", err, stdout+stderr)
		}
		if strings.Contains(stderr, "bd ready") {
			t.Errorf("human feedback for an informational cross-rig edge must not promise readiness gating:\nstderr:\n%s", stderr)
		}
	})
	t.Run("local edges stay compatible", func(t *testing.T) {
		src, peer := bdCreate(t, bd, dir, "local source"), bdCreate(t, bd, dir, "local peer")
		other := bdCreate(t, bd, dir, "local other")
		out := jsonOut(t, "link", src.ID, peer.ID, "--type", "blocks")
		if _, present := out["external"]; present {
			t.Errorf("a local edge must not carry the external marker, got %v", out)
		}
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "link", src.ID, other.ID, "--type", "related")
		if err != nil {
			t.Fatalf("bd link (human, local): %v\n%s", err, stdout+stderr)
		}
		if !strings.Contains(stdout, "Linked") {
			t.Errorf("local human output must keep its success line:\n%s", stdout)
		}
		if strings.Contains(stderr, "bd ready") {
			t.Errorf("a local edge must not print cross-rig semantics:\nstderr:\n%s", stderr)
		}
	})
	t.Run("human blocking semantics and quiet", func(t *testing.T) {
		src := bdCreate(t, bd, dir, "human note source")
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "add", src.ID, foreign.ID, "--type", "blocks")
		if err != nil {
			t.Fatalf("bd dep add (human): %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, "Added dependency") {
			t.Errorf("cross-rig human output must keep its success line:\n%s", stdout)
		}
		if !strings.Contains(stderr, "bd ready") || !strings.Contains(stderr, src.ID) {
			t.Errorf("cross-rig blocking add must explain on stderr that bd ready holds %s while its target is open or unreadable:\nstderr:\n%s", src.ID, stderr)
		}

		quiet := bdCreate(t, bd, dir, "quiet source")
		stdout, stderr, err = bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "add", quiet.ID, foreign.ID, "--type", "blocks", "--quiet")
		if err != nil {
			t.Fatalf("bd dep add --quiet: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if strings.Contains(stderr, "bd ready") {
			t.Errorf("--quiet must keep the blocking-semantics note silent:\nstderr:\n%s", stderr)
		}
	})
}
