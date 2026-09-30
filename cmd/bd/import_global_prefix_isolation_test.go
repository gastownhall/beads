//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestCLI_Import_GlobalModeIgnoresProjectConfigYAMLPrefix pins that `bd
// import --global` does not seed or sync issue_prefix from the current
// project's config.yaml: the shared global store's own prefix must win in
// --global mode (selectCreateIDPrefix), the same rule bd create already
// follows. A project's config.yaml leaking into the global store would let
// whichever project last ran a global import silently repoint every other
// project's global issue IDs. Uses the shared-server harness from
// global_identity_integration_test.go — --global requires shared-server mode.
func TestCLI_Import_GlobalModeIgnoresProjectConfigYAMLPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	cp, err := testutil.NewContainerProvider()
	if err != nil {
		t.Skipf("skipping: Dolt container not available: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	sharedDir := t.TempDir()
	if err := cp.WritePortFile(sharedDir); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	projectDir := filepath.Join(t.TempDir(), "localproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := gitInit(ctx, projectDir); err != nil {
		t.Fatalf("git init: %v", err)
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOROOT=" + os.Getenv("GOROOT"),
		"BEADS_SHARED_SERVER_DIR=" + sharedDir,
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(cp.Port()),
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	initArgs := []string{"init", "--shared-server", "--global", "--external", "--prefix", "globalinit", "--quiet", "--non-interactive"}
	if out, err := ssExec(ctx, bdBinary, projectDir, env, initArgs...); err != nil {
		t.Fatalf("bd %s (global init) failed: %v\noutput:\n%s", strings.Join(initArgs, " "), err, out)
	}

	// bd init (non-no-db) stores the prefix in the DB, not config.yaml; write
	// a DIFFERENT project-local prefix into config.yaml directly, the same
	// way global_identity_integration_test.go exercises the YAML-first
	// prefix-selection path (selectCreateIDPrefix) that bd-4646 regressed:
	// this is what a project's config.yaml looks like in practice, and
	// --global mode must not read it.
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: localproj\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml issue-prefix: %v", err)
	}

	globalOut, err := ssExec(ctx, bdBinary, projectDir, env, "config", "get", "issue_prefix", "--global")
	if err != nil {
		t.Fatalf("bd config get issue_prefix --global (before import) failed: %v\n%s", err, globalOut)
	}
	globalPrefixBefore := strings.TrimSpace(globalOut)
	// Anchor what `before` IS, so the after == before comparison below cannot
	// go vacuous: if this read ever stops resolving the global store's row it
	// returns "" or the "issue_prefix (not set)" line (cmd/bd/config.go), and
	// an unanchored after == before would then pass while measuring nothing.
	//
	// The expected value is doltserver.GlobalIssuePrefix, NOT the --prefix
	// passed to init above: --prefix names the PROJECT's prefix, and bd init
	// --global always writes the constant into the shared store (init.go).
	if globalPrefixBefore != doltserver.GlobalIssuePrefix {
		t.Fatalf("global issue_prefix before import = %q, want %q — the --global config read is not resolving the global store's row", globalPrefixBefore, doltserver.GlobalIssuePrefix)
	}

	issue := `{"id":"globaltest-1","title":"Global import test","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`
	jsonlPath := filepath.Join(projectDir, "global.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(issue+"\n"), 0644); err != nil {
		t.Fatalf("failed to write JSONL fixture: %v", err)
	}

	if out, err := ssExec(ctx, bdBinary, projectDir, env, "import", "--global", "-i", "global.jsonl"); err != nil {
		t.Fatalf("bd import --global failed: %v\n%s", err, out)
	}

	afterOut, err := ssExec(ctx, bdBinary, projectDir, env, "config", "get", "issue_prefix", "--global")
	if err != nil {
		t.Fatalf("bd config get issue_prefix --global (after import) failed: %v\n%s", err, afterOut)
	}
	if got := strings.TrimSpace(afterOut); got != doltserver.GlobalIssuePrefix {
		t.Fatalf("global issue_prefix changed from %q to %q — a --global import must not adopt the local project's config.yaml prefix (localproj)", globalPrefixBefore, got)
	}
}
