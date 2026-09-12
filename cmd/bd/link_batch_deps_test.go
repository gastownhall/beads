package main

// Regression tests for GH#5585: `bd link` and `bd batch`'s `dep.add` op built
// a types.DependencyType directly from the raw --type/args value and only
// called IsValid(), skipping the canonicalDependencyType/validateDependencyType
// pair GH#5069/#5116 introduced for `bd dep add --type` and `bd create --deps`.
// An alias like "blocked-by" was stored verbatim instead of normalizing to the
// canonical "blocks" type, so the edge never gated `bd ready`/`bd blocked`; a
// typo'd/made-up type was accepted outright.
//
// These tests run the real bd binary, built with the gms_pure_go embedded-Dolt
// engine (see buildBDForInitTests), so they need no cgo and no external Dolt
// server. They reuse the createDepsTestEnv/runCreateDepsBD family of helpers
// from create_deps_atomic_test.go (same package, no build tag).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkBatchDepType returns the stored type of the dependency from `from` to
// `to`, as reported by `bd dep list <from> --json`. That command's --json
// shape is the target issue decorated with a "dependency_type" field (see
// printDepListEdges), not the raw types.Dependency row.
func linkBatchDepType(t *testing.T, bd, dir, from, to string) string {
	t.Helper()
	out := runCreateDepsBD(t, bd, dir, "dep", "list", from, "--json")
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("dep list %s --json: no JSON array in output:\n%s", from, out)
	}
	var deps []struct {
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &deps); err != nil {
		t.Fatalf("parse dep list --json: %v\n%s", err, out)
	}
	for _, d := range deps {
		if d.ID == to {
			return d.DependencyType
		}
	}
	t.Fatalf("dep list %s --json: no edge to %s found in %+v", from, to, deps)
	return ""
}

// TestCLI_Link_TypeBlockedByAliasGates is the `bd link` counterpart to
// TestCLI_DepAdd_TypeBlockedByAliasGates (GH#5069/#5116): `bd link` is
// documented as `dep add` shorthand and must normalize the same aliases.
func TestCLI_Link_TypeBlockedByAliasGates(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	runCreateDepsBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "test",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	blocker := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link Blocker", "-p", "1", "--json"))
	blocked := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link Blocked", "-p", "1", "--json"))

	// blocked depends on (is blocked by) blocker, via the "blocked-by" alias
	// rather than the default "blocks" type.
	out := runCreateDepsBD(t, bd, dir, "link", blocked, blocker, "--type", "blocked-by")
	if !strings.Contains(out, "Linked") {
		t.Fatalf("expected 'Linked', got: %s", out)
	}

	gotType := linkBatchDepType(t, bd, dir, blocked, blocker)
	if gotType != "blocks" {
		t.Errorf("bd link --type blocked-by stored type %q, want canonical \"blocks\"", gotType)
	}

	blockedOut := runCreateDepsBD(t, bd, dir, "blocked")
	if !strings.Contains(blockedOut, "Link Blocked") {
		t.Errorf("expected \"Link Blocked\" in `bd blocked` output after link --type blocked-by, got: %s", blockedOut)
	}

	readyOut := runCreateDepsBD(t, bd, dir, "ready")
	if strings.Contains(readyOut, "Link Blocked") {
		t.Errorf("expected \"Link Blocked\" excluded from `bd ready` after link --type blocked-by, got: %s", readyOut)
	}
}

// TestCLI_Link_UnknownTypeRejected mirrors the "unknown type rejected"
// coverage validateDependencyType already has for `bd dep add`/`bd create
// --deps`: before the fix, `bd link` only ran dt.IsValid() (non-empty, under
// the length cap), so a made-up type like "bogus-type" was silently accepted
// and stored, permanently unable to gate readiness.
func TestCLI_Link_UnknownTypeRejected(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	runCreateDepsBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "test",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	a := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link A", "-p", "1", "--json"))
	b := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link B", "-p", "1", "--json"))

	out, err := runCreateDepsBDRaw(bd, dir, "link", a, b, "--type", "bogus-type")
	if err == nil {
		t.Errorf("bd link --type bogus-type exited 0; output:\n%s", out)
	}
	if !strings.Contains(out, "unknown dependency type") {
		t.Errorf("expected 'unknown dependency type' error, got:\n%s", out)
	}
}

// TestCLI_Batch_DepAdd_TypeBlockedByAliasGates is the `bd batch` dep.add
// counterpart: the op built a types.DependencyType directly from the raw arg
// and only checked IsValid(), so "blocked-by" was stored verbatim instead of
// normalizing to "blocks".
func TestCLI_Batch_DepAdd_TypeBlockedByAliasGates(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	runCreateDepsBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "test",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	blocker := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch Blocker", "-p", "1", "--json"))
	blocked := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch Blocked", "-p", "1", "--json"))

	script := "dep add " + blocked + " " + blocker + " blocked-by\n"
	scriptFile := filepath.Join(dir, "batch-alias.txt")
	if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
		t.Fatalf("write batch script: %v", err)
	}

	out := runCreateDepsBD(t, bd, dir, "batch", "-f", scriptFile)
	if strings.Contains(strings.ToLower(out), "error") {
		t.Fatalf("bd batch dep.add blocked-by reported an error: %s", out)
	}

	gotType := linkBatchDepType(t, bd, dir, blocked, blocker)
	if gotType != "blocks" {
		t.Errorf("bd batch dep.add blocked-by stored type %q, want canonical \"blocks\"", gotType)
	}

	blockedOut := runCreateDepsBD(t, bd, dir, "blocked")
	if !strings.Contains(blockedOut, "Batch Blocked") {
		t.Errorf("expected \"Batch Blocked\" in `bd blocked` output after batch dep.add blocked-by, got: %s", blockedOut)
	}

	readyOut := runCreateDepsBD(t, bd, dir, "ready")
	if strings.Contains(readyOut, "Batch Blocked") {
		t.Errorf("expected \"Batch Blocked\" excluded from `bd ready` after batch dep.add blocked-by, got: %s", readyOut)
	}
}

// TestCLI_Batch_DepAdd_UnknownTypeRejected mirrors the unknown-type rejection
// coverage for the batch dep.add op.
func TestCLI_Batch_DepAdd_UnknownTypeRejected(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	runCreateDepsBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "test",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	a := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch A", "-p", "1", "--json"))
	b := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch B", "-p", "1", "--json"))

	script := "dep add " + a + " " + b + " bogus-type\n"
	scriptFile := filepath.Join(dir, "batch-bogus.txt")
	if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
		t.Fatalf("write batch script: %v", err)
	}

	out, err := runCreateDepsBDRaw(bd, dir, "batch", "-f", scriptFile)
	if err == nil {
		t.Errorf("bd batch dep.add bogus-type exited 0; output:\n%s", out)
	}
	if !strings.Contains(out, "unknown dependency type") {
		t.Errorf("expected 'unknown dependency type' error, got:\n%s", out)
	}
}
