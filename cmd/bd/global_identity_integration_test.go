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

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestGlobalDBIdentityCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	cp, err := testutil.NewContainerProvider()
	if err != nil {
		t.Skipf("skipping: Dolt container not available: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })
	containerPort := cp.Port()

	sharedDir := t.TempDir()
	if err := cp.WritePortFile(sharedDir); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	projectDir := filepath.Join(t.TempDir(), "proj0")
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
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(containerPort),
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	initArgs := []string{
		"init",
		"--shared-server",
		"--global",
		"--external",
		"--prefix", "proj0",
		"--quiet",
		"--non-interactive",
	}
	if out, err := ssExec(ctx, bdBinary, projectDir, env, initArgs...); err != nil {
		t.Fatalf("bd %s failed: %v\noutput:\n%s",
			strings.Join(initArgs, " "), err, out)
	}

	beadsDir := filepath.Join(projectDir, ".beads")
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load metadata.json from %s: %v", beadsDir, err)
	}
	if cfg == nil {
		t.Fatalf("metadata.json missing in %s", beadsDir)
	}
	if cfg.GlobalDoltDatabase != doltserver.GlobalDatabaseName {
		t.Errorf("metadata.json global_dolt_database = %q, want %q",
			cfg.GlobalDoltDatabase, doltserver.GlobalDatabaseName)
	}
	if cfg.ProjectID == "" {
		t.Error("metadata.json project_id is empty; expected a generated UUID")
	}
	if cfg.ProjectID == doltserver.GlobalProjectID {
		t.Errorf("metadata.json project_id = %q (the global sentinel); "+
			"a per-project UUID was expected", cfg.ProjectID)
	}
	if cfg.GlobalProjectID != doltserver.GlobalProjectID {
		t.Errorf("metadata.json global_project_id = %q, want %q",
			cfg.GlobalProjectID, doltserver.GlobalProjectID)
	}
	// bd init (non-no-db) stores the prefix in the DB, not config.yaml
	// (the generated file only carries a commented `# issue-prefix: ""`).
	// Write the project prefix into config.yaml explicitly so that the
	// create/show steps below exercise the YAML-first prefix-selection
	// path exactly as bd-4646 regressed it: unpatched selectCreateIDPrefix
	// would then incorrectly prefer this "proj0" YAML prefix over the
	// global store's prefix even in --global mode, and the ID validation
	// below would fail. See cmd/bd/init_embedded_test.go for the same
	// pattern.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: proj0\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml issue-prefix: %v", err)
	}
	if yamlPrefix := config.GetStringFromDir(beadsDir, "issue-prefix"); yamlPrefix != "proj0" {
		t.Fatalf("project YAML issue-prefix = %q, want %q", yamlPrefix, "proj0")
	}

	explicitGlobalID := doltserver.GlobalIssuePrefix + "-explicit"
	out, err := ssExec(ctx, bdBinary, projectDir, env,
		"create", "Global explicit ID", "--global", "--id", explicitGlobalID, "--silent")
	if err != nil {
		t.Fatalf("bd create --global --id %s failed: %v\noutput:\n%s", explicitGlobalID, err, out)
	}
	if got := strings.TrimSpace(out); got != explicitGlobalID {
		t.Fatalf("bd create --global --id returned %q, want %q", got, explicitGlobalID)
	}

	out, err = ssExec(ctx, bdBinary, projectDir, env, "show", "--global", explicitGlobalID)
	if err != nil {
		t.Fatalf("bd show --global %s failed: %v\noutput:\n%s", explicitGlobalID, err, out)
	}
	if !strings.Contains(out, explicitGlobalID) {
		t.Fatalf("bd show --global did not return %s from %s:\n%s",
			explicitGlobalID, doltserver.GlobalDatabaseName, out)
	}

	out, err = ssExec(ctx, bdBinary, projectDir, env, "list", "--global")
	if err != nil {
		t.Fatalf("bd list --global failed: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "PROJECT IDENTITY MISMATCH") {
		t.Fatalf("bd list --global returned identity mismatch error against the global DB:\n%s", out)
	}
}
