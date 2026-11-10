package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// updateCorpus regenerates the committed golden corpus instead of checking it.
// `make corpus-regen` sets this; ordinary CI runs leave it false so a wire
// change shows up as a hard diff failure that must be reviewed.
var updateCorpus = flag.Bool("corpus.update", false, "regenerate the committed golden corpus under testdata/corpus/")

const corpusGeneratedBy = "cmd/bd/protocol TestCorpusGolden"

type captureResult struct {
	stdout   []byte
	exitCode int
	execErr  error
}

type captureRunner func(envelope bool, capture Capture) captureResult

// newLiveCaptureRunner adapts one fresh subprocess workspace to captureRunner.
// The corpus PLAN is stateful, so a runner owns exactly one workspace and a
// separate runner is required for every independent corpus generation.
func newLiveCaptureRunner(t *testing.T) captureRunner {
	t.Helper()
	w := newWorkspace(t)
	return func(envelope bool, c Capture) captureResult {
		env := w.env()
		if envelope {
			env = append(env, "BD_JSON_ENVELOPE=1")
		}
		cmd := exec.Command(w.bd, c.Args...)
		cmd.Dir = w.dir
		cmd.Env = env
		var stdout bytes.Buffer
		cmd.Stdout = &stdout // stdout only: bd writes human error banners to stderr
		runErr := cmd.Run()
		result := captureResult{stdout: stdout.Bytes()}
		if runErr == nil {
			return result
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.exitCode = exitErr.ExitCode()
			return result
		}
		result.execErr = runErr
		return result
	}
}

// generateCorpus runs the exact corpus PLAN through runner for one envelope
// mode and returns name -> canonicalized blob bytes.
func generateCorpus(envelope bool, runner captureRunner) (map[string][]byte, error) {
	out := make(map[string][]byte)
	for _, c := range CorpusPlan() {
		result := runner(envelope, c)
		if err := checkCaptureExit(c.Name, result); err != nil {
			return nil, fmt.Errorf("capture %q (envelope=%v): %w", c.Name, envelope, err)
		}

		canon, err := CanonicalizeJSON(result.stdout)
		if err != nil {
			return nil, fmt.Errorf("canonicalize %s (envelope=%v): %w\nraw stdout:\n%s", c.Name, envelope, err, result.stdout)
		}
		if c.Name != errorCaptureName && isErrorEnvelope(canon, envelope) {
			return nil, fmt.Errorf("capture %q (envelope=%v) produced an error envelope, not real output:\n%s\n(check the bd command in CorpusPlan)", c.Name, envelope, canon)
		}
		out[c.Name] = canon
	}
	return out, nil
}

// isErrorEnvelope reports whether a canonicalized blob is bd's {error, ...}
// shape (flat) or {data:{error,...}} / {error,...} under the v2 envelope.
func isErrorEnvelope(blob []byte, envelope bool) bool {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(blob, &top); err != nil {
		return false // arrays (list/ready) are never error envelopes
	}
	if _, ok := top["error"]; ok {
		return true
	}
	if envelope {
		if data, ok := top["data"]; ok {
			var inner map[string]json.RawMessage
			if json.Unmarshal(data, &inner) == nil {
				_, ok := inner["error"]
				return ok
			}
		}
	}
	return false
}

// errorCaptureExitCode is the exit status bd returns for the error capture
// (`bd show <missing> --json`). bd's RunE error handlers return
// exitError{Code: 1} (cmd/bd/errors.go: HandleErrorRespectJSON / SilentExit),
// which main() maps to os.Exit(1) — the not-found path exits 1 while still
// printing the {error, schema_version} envelope to stdout. Pinning the exact
// code makes the corpus catch a regression in bd's not-found exit status, which
// a downstream consumer's error classifier keys off of.
const errorCaptureExitCode = 1

// checkCaptureExit validates a capture result's exit status against the
// corpus contract, so the exit-codes-and-errors coverage CATALOG.md claims is
// actually enforced instead of silently discarded. Every capture except
// errorCaptureName must exit 0; the error capture must exit with
// errorCaptureExitCode. It returns a descriptive error instead of failing the
// test directly, so the rule is unit-testable without a live bd and every PLAN
// capture passes through the same validation.
func checkCaptureExit(name string, result captureResult) error {
	if result.execErr != nil {
		return fmt.Errorf("capture execution failed: %w", result.execErr)
	}
	if name == errorCaptureName {
		if got := result.exitCode; got != errorCaptureExitCode {
			return fmt.Errorf("error capture exit code = %d, want %d (bd not-found contract)", got, errorCaptureExitCode)
		}
		return nil
	}
	if result.exitCode != 0 {
		return fmt.Errorf("capture exit code = %d, want 0 (success)", result.exitCode)
	}
	return nil
}

func TestCheckCaptureExit(t *testing.T) {
	tests := []struct {
		desc    string
		capture string
		result  captureResult
		wantErr bool
	}{
		{"success capture, exit 0", "show", captureResult{}, false},
		{"success capture, non-zero exit", "show", captureResult{exitCode: 1}, true},
		{"error capture, expected exit code", errorCaptureName, captureResult{exitCode: errorCaptureExitCode}, false},
		{"error capture, exit 0", errorCaptureName, captureResult{}, true},
		{"error capture, wrong non-zero code", errorCaptureName, captureResult{exitCode: errorCaptureExitCode + 1}, true},
		{"execution failed", "show", captureResult{execErr: errors.New("exec: not started")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := checkCaptureExit(tc.capture, tc.result)
			if tc.wantErr && err == nil {
				t.Fatalf("checkCaptureExit(%q, %#v) = nil, want error", tc.capture, tc.result)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkCaptureExit(%q, %#v) = %v, want nil", tc.capture, tc.result, err)
			}
		})
	}
}

func TestCorpusGolden(t *testing.T) {
	requireDoltStore(t, "corpus golden test")

	modes := []struct {
		name     string
		envelope bool
	}{
		{"flat", false},
		{"envelope", true},
	}

	blobs := make(map[string]map[string][]byte, len(modes))
	for _, m := range modes {
		runner := newLiveCaptureRunner(t)
		generated, err := generateCorpus(m.envelope, runner)
		if err != nil {
			t.Fatalf("generate %s corpus: %v", m.name, err)
		}
		blobs[m.name] = generated
	}

	dir := filepath.Join("testdata", "corpus")
	schemaVersion := schemaVersionFromBlobs(t, blobs)
	manifest := NewManifest(schemaVersion, corpusGeneratedBy, CorpusPlan(), blobs)
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	if *updateCorpus {
		writeCorpus(t, dir, blobs, manifestBytes)
		t.Logf("regenerated corpus under %s", dir)
		return
	}

	for _, m := range modes {
		for name, got := range blobs[m.name] {
			path := filepath.Join(dir, m.name, name+".json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read committed %s: %v\nrun `make corpus-regen` to (re)generate the corpus", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("corpus drift in %s/%s.json\n--- committed ---\n%s\n--- live bd ---\n%s\nrun `make corpus-regen` and review the diff before committing", m.name, name, want, got)
			}
		}
	}

	wantManifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read committed manifest: %v\nrun `make corpus-regen`", err)
	}
	if !bytes.Equal(manifestBytes, wantManifest) {
		t.Fatalf("manifest drift\n--- committed ---\n%s\n--- live ---\n%s\nrun `make corpus-regen`", wantManifest, manifestBytes)
	}

	// The per-blob loop above only proves every generated blob is committed and
	// matches. It cannot catch a *stale* committed blob left behind when a
	// capture is removed or renamed from CorpusPlan — that file simply stops
	// being generated but stays on disk, and the release job ships the whole
	// testdata/corpus directory. Assert the committed file set is exactly the
	// generated blobs plus manifest.json so a deleted capture is a hard failure.
	assertNoStaleCorpusFiles(t, dir, blobs)
}

// assertNoStaleCorpusFiles fails if the committed corpus tree holds any .json
// file that the current PLAN did not generate. Without it, removing a capture
// leaves its stale blob committed (and shipped in the release archive) while
// still passing the per-blob golden comparison, which only checks generated
// files.
func assertNoStaleCorpusFiles(t *testing.T, dir string, blobs map[string]map[string][]byte) {
	t.Helper()
	want := map[string]bool{"manifest.json": true}
	for mode, byName := range blobs {
		for name := range byName {
			want[filepath.ToSlash(filepath.Join(mode, name+".json"))] = true
		}
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !want[rel] {
			t.Errorf("stale corpus file committed but not generated by CorpusPlan: %s\nremove it (or run `make corpus-regen`) after deleting a capture", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus dir %s: %v", dir, err)
	}
}

func writeCorpus(t *testing.T, dir string, blobs map[string]map[string][]byte, manifest []byte) {
	t.Helper()
	for mode, byName := range blobs {
		modeDir := filepath.Join(dir, mode)
		// Recreate the mode directory from scratch so a capture removed or
		// renamed in CorpusPlan does not leave a stale blob behind on regen.
		if err := os.RemoveAll(modeDir); err != nil {
			t.Fatalf("clean %s: %v", modeDir, err)
		}
		if err := os.MkdirAll(modeDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", modeDir, err)
		}
		for name, data := range byName {
			if err := os.WriteFile(filepath.Join(modeDir, name+".json"), data, 0o644); err != nil {
				t.Fatalf("write %s/%s: %v", mode, name, err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// schemaVersionFromBlobs reads the schema_version every bd --json object carries
// (flat mode) so the manifest records the same canary the wire uses. A bump in
// bd's JSONSchemaVersion changes every blob, which the diff guard then catches.
func schemaVersionFromBlobs(t *testing.T, blobs map[string]map[string][]byte) int {
	t.Helper()
	raw, ok := blobs["flat"]["version"]
	if !ok {
		t.Fatal("corpus missing flat/version blob")
	}
	var v struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse schema_version from version blob: %v", err)
	}
	if v.SchemaVersion == 0 {
		t.Fatalf("version blob has no schema_version:\n%s", raw)
	}
	return v.SchemaVersion
}
