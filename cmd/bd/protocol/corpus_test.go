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
	"regexp"
	"slices"
	"strings"
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
	// literals are run-specific strings the runner knows (the workspace's
	// random id prefix and its directory) mapped to their placeholders.
	literals map[string]string
	// commitsAdded is how many Dolt commits a Write capture added.
	commitsAdded int
}

// corpusRun is one generation of the PLAN in one envelope mode.
type corpusRun struct {
	blobs   map[string][]byte // capture name -> canonical blob
	commits map[string]int    // Write capture name -> Dolt commits it added
}

// corpusCommitsFile is the commit ledger. It is test-only state, outside
// testdata/corpus, so the release job does not ship it to consumers.
const corpusCommitsFile = "corpus_commits.golden"

type captureRunner func(envelope bool, capture Capture) captureResult

// newLiveCaptureRunner adapts one fresh subprocess workspace to captureRunner.
// The corpus PLAN is stateful, so a runner owns exactly one workspace and a
// separate runner is required for every independent corpus generation.
func newLiveCaptureRunner(t *testing.T) captureRunner {
	t.Helper()
	w := newWorkspace(t)
	literals := map[string]string{w.prefix: "<PREFIX>", w.dir: "<WORKSPACE>"}
	if real, err := filepath.EvalSymlinks(w.dir); err == nil {
		literals[real] = "<WORKSPACE>"
	}
	return func(envelope bool, c Capture) captureResult {
		before := 0
		if c.Write {
			n, err := w.doltCommitCount()
			if err != nil {
				return captureResult{execErr: fmt.Errorf("commit count before %s: %w", c.Name, err)}
			}
			before = n
		}
		result := runLiveCapture(w, envelope, c)
		result.literals = literals
		if c.Write && result.execErr == nil {
			after, err := w.doltCommitCount()
			if err != nil {
				result.execErr = fmt.Errorf("commit count after %s: %w", c.Name, err)
				return result
			}
			result.commitsAdded = after - before
		}
		return result
	}
}

// doltCommitCount reads the workspace's Dolt commit count (dolt_log) through
// bd's own read-only report of it. The corpus runs in embedded mode, where
// `bd sql` is refused, so dolt_log is not otherwise reachable.
func (w *workspace) doltCommitCount() (int, error) {
	cmd := exec.Command(w.bd, "flatten", "--dry-run", "--json")
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bd flatten --dry-run --json: %w", err)
	}
	var report struct {
		CommitCount *int `json:"commit_count"`
	}
	if err := json.Unmarshal(out, &report); err != nil || report.CommitCount == nil {
		return 0, fmt.Errorf("bd flatten --dry-run --json printed no commit_count: %v\n%s", err, out)
	}
	return *report.CommitCount, nil
}

func runLiveCapture(w *workspace, envelope bool, c Capture) captureResult {
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

// generateCorpus runs the exact corpus PLAN through runner for one envelope
// mode and returns name -> canonicalized blob bytes.
func generateCorpus(envelope bool, runner captureRunner) (corpusRun, error) {
	run := corpusRun{blobs: make(map[string][]byte), commits: make(map[string]int)}
	minted := map[string]string{} // placeholder -> minted id
	for _, c := range CorpusPlan() {
		resolved, err := resolveMinted(c, minted)
		if err != nil {
			return corpusRun{}, err
		}
		result := runner(envelope, resolved)
		if err := checkCaptureExit(c.Name, result); err != nil {
			return corpusRun{}, fmt.Errorf("capture %q (envelope=%v): %w", c.Name, envelope, err)
		}
		if c.Kind == CaptureSetup {
			continue
		}
		if c.Mints {
			id, err := mintedID(result.stdout)
			if err != nil {
				return corpusRun{}, fmt.Errorf("capture %q (envelope=%v): %w\nraw stdout:\n%s", c.Name, envelope, err, result.stdout)
			}
			minted[MintedPlaceholder(c.Name)] = id
		}
		proj := Projection{Literals: map[string]string{}, Events: c.Kind == CaptureEvents}
		for lit, ph := range result.literals {
			proj.Literals[lit] = ph
		}
		for ph, id := range minted {
			proj.Literals[id] = ph
		}

		var canon []byte
		if c.Kind == CaptureText {
			canon, err = marshalCanonical(newProjector(proj).project(string(result.stdout)))
		} else {
			canon, err = CanonicalizeJSONWith(result.stdout, proj)
		}
		if err != nil {
			return corpusRun{}, fmt.Errorf("canonicalize %s (envelope=%v): %w\nraw stdout:\n%s", c.Name, envelope, err, result.stdout)
		}
		if c.Name != errorCaptureName && isErrorEnvelope(canon, envelope) {
			return corpusRun{}, fmt.Errorf("capture %q (envelope=%v) produced an error envelope, not real output:\n%s\n(check the bd command in CorpusPlan)", c.Name, envelope, canon)
		}
		run.blobs[c.Name] = canon
		if c.Write {
			run.commits[c.Name] = result.commitsAdded
		}
	}
	return run, nil
}

// resolveMinted returns c with every MintedPlaceholder in its Args replaced by
// the id that capture minted earlier in this run.
func resolveMinted(c Capture, minted map[string]string) (Capture, error) {
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		args[i] = a
		if !strings.Contains(a, "<MINTED:") {
			continue
		}
		for ph, id := range minted {
			args[i] = strings.ReplaceAll(args[i], ph, id)
		}
		if strings.Contains(args[i], "<MINTED:") {
			return Capture{}, fmt.Errorf("capture %q names %s before any capture minted it", c.Name, a)
		}
	}
	c.Args = args
	return c, nil
}

// mintedID reads the id from a Mints capture's output: a flat issue object, or
// one under the envelope's data.
func mintedID(stdout []byte) (string, error) {
	var top struct {
		ID   string `json:"id"`
		Data *struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &top); err != nil {
		return "", fmt.Errorf("read minted id: %w", err)
	}
	if top.ID != "" {
		return top.ID, nil
	}
	if top.Data != nil && top.Data.ID != "" {
		return top.Data.ID, nil
	}
	return "", fmt.Errorf("capture is marked Mints but its output names no id")
}

// commitLedgerDisagreement reports a write whose commit count differs between
// the flat and envelope runs: BD_JSON_ENVELOPE changes only what bd prints, so
// a difference is a bug in bd or in the probe, not a second expectation.
func commitLedgerDisagreement(flat, envelope map[string]int) error {
	flatLedger, envelopeLedger := renderCommitLedger(flat), renderCommitLedger(envelope)
	if bytes.Equal(flatLedger, envelopeLedger) {
		return nil
	}
	return fmt.Errorf("the flat and envelope runs disagree on Dolt commits per write\n--- flat ---\n%s--- envelope ---\n%s", flatLedger, envelopeLedger)
}

// renderCommitLedger renders the Write captures' commit counts in PLAN order.
func renderCommitLedger(commits map[string]int) []byte {
	var b bytes.Buffer
	b.WriteString("# Dolt commits each corpus write capture adds to its embedded workspace:\n")
	b.WriteString("# dolt_log's row count (bd flatten --dry-run --json) after the command minus\n")
	b.WriteString("# before it. Generated by TestCorpusGolden; the flat and envelope runs must\n")
	b.WriteString("# agree. Regenerate with `make corpus-regen` and review the diff.\n")
	for _, c := range CorpusPlan() {
		if c.Write {
			fmt.Fprintf(&b, "%s %d\n", c.Name, commits[c.Name])
		}
	}
	return b.Bytes()
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
	commits := make(map[string]map[string]int, len(modes))
	for _, m := range modes {
		runner := newLiveCaptureRunner(t)
		generated, err := generateCorpus(m.envelope, runner)
		if err != nil {
			t.Fatalf("generate %s corpus: %v", m.name, err)
		}
		blobs[m.name] = generated.blobs
		commits[m.name] = generated.commits
	}
	if err := commitLedgerDisagreement(commits["flat"], commits["envelope"]); err != nil {
		t.Fatal(err)
	}
	ledger := renderCommitLedger(commits["flat"])
	ledgerPath := filepath.Join("testdata", corpusCommitsFile)

	dir := filepath.Join("testdata", "corpus")
	schemaVersion := schemaVersionFromBlobs(t, blobs)
	manifest := NewManifest(schemaVersion, corpusGeneratedBy, CorpusPlan(), blobs)
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	if *updateCorpus {
		writeCorpus(t, dir, blobs, manifestBytes)
		if err := os.WriteFile(ledgerPath, ledger, 0o644); err != nil {
			t.Fatalf("write %s: %v", ledgerPath, err)
		}
		t.Logf("regenerated corpus under %s and %s", dir, ledgerPath)
		return
	}

	wantLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read committed %s: %v\nrun `make corpus-regen`", ledgerPath, err)
	}
	if !bytes.Equal(ledger, wantLedger) {
		t.Errorf("commit ledger drift in %s\n--- committed ---\n%s\n--- live bd ---\n%s\nrun `make corpus-regen` and review the diff before committing", ledgerPath, wantLedger, ledger)
	}

	for _, m := range modes {
		for name, got := range blobs[m.name] {
			path := filepath.Join(dir, m.name, name+".json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read committed %s: %v\nrun `make corpus-regen` to (re)generate the corpus", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("corpus drift in %s/%s.json\n--- committed ---\n%s\n--- live bd ---\n%s\nrun `make corpus-regen` and review the diff before committing", m.name, name, want, got)
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

// legacyCaptureNames freezes the captures that predate the native-fix section,
// in order. New captures are appended after them, so every legacy blob (and
// the gascity decoder tests that read them) keeps its bytes.
var legacyCaptureNames = []string{
	"create_root", "create_dep", "create_closed", "create_deleted", "dep_add",
	"show", "dep_list", "update", "close", "reopen", "dep_remove", "delete",
	"list", "ready", "count", "version", errorCaptureName,
}

var (
	captureNameRE       = regexp.MustCompile(`^[a-z0-9_]+$`)
	mintedPlaceholderRE = regexp.MustCompile(`<MINTED:([a-z0-9_]+)>`)
)

// checkCorpusPlan enforces the rules the generator and the shipped corpus rely on.
func checkCorpusPlan(plan []Capture) []error {
	var errs []error
	if len(plan) < len(legacyCaptureNames) {
		return []error{fmt.Errorf("plan has %d captures, fewer than the %d legacy ones", len(plan), len(legacyCaptureNames))}
	}
	for i, name := range legacyCaptureNames {
		c := plan[i]
		if c.Name != name || c.Kind != CaptureJSON || c.Write || c.Mints {
			errs = append(errs, fmt.Errorf("legacy capture %d is %q (kind %d, write %v, mints %v), want plain %q: new captures go after the legacy ones", i, c.Name, c.Kind, c.Write, c.Mints, name))
		}
	}
	seen := map[string]Capture{}
	pendingWrite := ""
	for _, c := range plan {
		if !captureNameRE.MatchString(c.Name) {
			errs = append(errs, fmt.Errorf("capture name %q is not [a-z0-9_]+", c.Name))
		}
		if _, dup := seen[c.Name]; dup {
			errs = append(errs, fmt.Errorf("capture name %q appears twice", c.Name))
		}
		for _, a := range c.Args {
			for _, m := range mintedPlaceholderRE.FindAllStringSubmatch(a, -1) {
				if src, ok := seen[m[1]]; !ok || !src.Mints {
					errs = append(errs, fmt.Errorf("capture %q names %s, but no earlier capture %q mints an id", c.Name, m[0], m[1]))
				}
			}
		}
		switch c.Kind {
		case CaptureSetup:
			if c.Write || c.Mints {
				errs = append(errs, fmt.Errorf("setup step %q cannot be Write or Mints: it produces no blob", c.Name))
			}
		case CaptureEvents:
			if len(c.Args) != 4 || c.Args[0] != "history" || c.Args[2] != "--events" || c.Args[3] != "--json" {
				errs = append(errs, fmt.Errorf("events capture %q args = %q, want [history <id> --events --json]", c.Name, c.Args))
			}
			pendingWrite = ""
		}
		if c.Mints && (c.Kind != CaptureJSON || !c.Write) {
			errs = append(errs, fmt.Errorf("capture %q mints an id, so it must be a JSON Write capture", c.Name))
		}
		if c.Kind != CaptureSetup && !slices.Contains(c.Args, "--json") {
			errs = append(errs, fmt.Errorf("capture %q does not pass --json", c.Name))
		}
		if c.Write {
			if pendingWrite != "" {
				errs = append(errs, fmt.Errorf("write %q has no events capture before the next write %q", pendingWrite, c.Name))
			}
			pendingWrite = c.Name
		}
		seen[c.Name] = c
	}
	if pendingWrite != "" {
		errs = append(errs, fmt.Errorf("write %q has no events capture after it", pendingWrite))
	}
	return errs
}

func TestCorpusPlanIsWellFormed(t *testing.T) {
	for _, err := range checkCorpusPlan(CorpusPlan()) {
		t.Error(err)
	}
}

// TestCorpusPlanRulesFire proves each checkCorpusPlan rule rejects a plan that
// breaks it, so TestCorpusPlanIsWellFormed passing means something.
func TestCorpusPlanRulesFire(t *testing.T) {
	mutate := func(f func([]Capture) []Capture) []Capture {
		plan := CorpusPlan()
		return f(plan)
	}
	idx := func(plan []Capture, name string) int {
		for i, c := range plan {
			if c.Name == name {
				return i
			}
		}
		t.Fatalf("no capture %q", name)
		return -1
	}
	tests := []struct {
		name string
		plan []Capture
		want string
	}{
		{"capture inserted before a legacy one", mutate(func(p []Capture) []Capture {
			return append([]Capture{{Name: "early", Args: []string{"list", "--json"}}}, p...)
		}), "new captures go after the legacy ones"},
		{"duplicate name", mutate(func(p []Capture) []Capture { return append(p, p[len(p)-1]) }), "appears twice"},
		{"write without events", mutate(func(p []Capture) []Capture {
			i := idx(p, "priority_events")
			return append(p[:i], p[i+1:]...)
		}), `write "priority" has no events capture`},
		{"minted id used before minting", mutate(func(p []Capture) []Capture {
			i := idx(p, "todo_add")
			p[i].Mints = false
			return p
		}), "no earlier capture"},
		{"setup marked write", mutate(func(p []Capture) []Capture {
			i := idx(p, "seed_anchor")
			p[i].Write = true
			return p
		}), "cannot be Write"},
		{"events capture with other args", mutate(func(p []Capture) []Capture {
			i := idx(p, "tag_events")
			p[i].Args = []string{"show", CorpusTagID, "--json"}
			return p
		}), "want [history <id> --events --json]"},
		{"capture without --json", mutate(func(p []Capture) []Capture {
			i := idx(p, "info")
			p[i].Args = []string{"info"}
			return p
		}), "does not pass --json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var msgs []string
			for _, err := range checkCorpusPlan(tc.plan) {
				msgs = append(msgs, err.Error())
			}
			if !strings.Contains(strings.Join(msgs, "\n"), tc.want) {
				t.Fatalf("checkCorpusPlan errors = %q, want one containing %q", msgs, tc.want)
			}
		})
	}
}

// TestCommitLedgerDisagreementFires proves the flat/envelope agreement check
// rejects a write whose commit count differs between the two runs.
func TestCommitLedgerDisagreementFires(t *testing.T) {
	same := map[string]int{"priority": 1, "dep_relate": 0}
	if err := commitLedgerDisagreement(same, map[string]int{"priority": 1, "dep_relate": 0}); err != nil {
		t.Fatalf("equal ledgers reported as disagreeing: %v", err)
	}
	err := commitLedgerDisagreement(same, map[string]int{"priority": 2, "dep_relate": 0})
	if err == nil || !strings.Contains(err.Error(), "priority 2") {
		t.Fatalf("commitLedgerDisagreement = %v, want an error naming the envelope count", err)
	}
}
