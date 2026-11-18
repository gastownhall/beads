//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestInitReinitLocalRefusesEngineFamilySwitchLiveServer is the server-side behavioural
// proof of the GH#3495 store-switch guard: a non-interactive reinit that
// would move a workspace recorded as server-mode to the embedded engine must
// be refused without a destroy token, and the refusal may not depend on the
// recorded store's contents — the old store is never opened or counted, the
// recorded metadata is not rewritten, and no parallel embedded database is
// created beside it.
//
// Runs the real binary against a live dolt sql-server, because the guard is
// wired into init's RunE guard order (mode resolution, the missing-database
// guard, the mutation gate) and only the end-to-end path exercises that order.
func TestInitReinitLocalRefusesEngineFamilySwitchLiveServer(t *testing.T) {
	beadsDir := startProjectServerModeGuardFixture(t, "la")
	port, err := strconv.Atoi(os.Getenv("BEADS_DOLT_SERVER_PORT"))
	if err != nil {
		t.Fatalf("fixture must pin BEADS_DOLT_SERVER_PORT: %v", err)
	}
	// The legacy-upgrade guard refuses a server workspace whose local Dolt
	// storage has no current-era version witness; a workspace bd initialized
	// has one.
	if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), Version); err != nil {
		t.Fatalf("write local version: %v", err)
	}

	projectDir := filepath.Dir(beadsDir)
	env := initGuardE2EEnv(t, port)

	// The configured database must EXIST on the server so the
	// missing-database guard — an earlier guard whose outcome init must keep —
	// is out of the way and the store-switch guard is the one deciding.
	// --recreate-missing is the documented way to put it there.
	setupOut, err := runExternalServerBD(t, projectDir, env,
		"init", "--prefix", "la", "--database", "la", "--skip-hooks", "--skip-agents", "--recreate-missing")
	if err != nil {
		t.Fatalf("setup bd init --recreate-missing: %v\n%s", err, setupOut)
	}
	if check := checkDatabaseOnServer("127.0.0.1", port, "root", "", "la", false); !check.Reachable || !check.Exists {
		t.Fatalf("precondition: database must exist on the server after setup, got %+v\n%s", check, setupOut)
	}

	metadataBefore, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata after setup: %v", err)
	}
	configBefore, configErr := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))

	// The workspace records server mode; BEADS_DOLT_SERVER_MODE=0 selects
	// embedded explicitly (any non-empty value counts as an explicit mode, so
	// inheritance is blocked), keeping the same database so only the
	// engine-family axis can fire.
	switchEnv := append(append([]string{}, env...), "BEADS_DOLT_SERVER_MODE=0")
	out, err := runExternalServerBD(t, projectDir, switchEnv,
		"init", "--reinit-local", "--prefix", "la", "--database", "la", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("reinit switching engine families failed without an exit status: %v\n%s", err, out)
		}
		exitCode = exitErr.ExitCode()
	}
	if exitCode != ExitDestroyTokenMissing {
		t.Fatalf("reinit switching engine families without a destroy token exited %d, want %d:\n%s", exitCode, ExitDestroyTokenMissing, out)
	}
	for _, want := range []string{"--destroy-token", "bd help init-safety", "server", "embedded"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal missing %q:\n%s", want, out)
		}
	}
	// The refusal happens inside the mutation gate, before any workspace
	// write: the recorded metadata and config must be untouched, and no
	// parallel embedded database may have been created.
	metadataAfter, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata after refusal: %v", err)
	}
	if string(metadataBefore) != string(metadataAfter) {
		t.Errorf("refusal rewrote the recorded metadata:\nbefore: %s\nafter:  %s", metadataBefore, metadataAfter)
	}
	if configErr == nil {
		configAfter, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
		if err != nil {
			t.Fatalf("read config after refusal: %v", err)
		}
		if string(configBefore) != string(configAfter) {
			t.Errorf("refusal rewrote the recorded config:\nbefore: %s\nafter:  %s", configBefore, configAfter)
		}
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); statErr == nil {
		t.Errorf("refused switch created a parallel embedded database at %s", filepath.Join(beadsDir, "embeddeddolt"))
	}
	if check := checkDatabaseOnServer("127.0.0.1", port, "root", "", "la", false); !check.Reachable || !check.Exists {
		t.Errorf("refusal disturbed the recorded database on the server: %+v", check)
	}

	t.Run("earlier guard keeps its own outcome", func(t *testing.T) {
		// An unreachable server is the missing-database guard's case, and that
		// guard runs BEFORE the store-switch preflight: the reinit must keep
		// its refusal rather than answer the store-switch question instead.
		unreachable := initGuardE2EEnv(t, 1)
		switchEnv := append(append([]string{}, unreachable...), "BEADS_DOLT_SERVER_MODE=0")
		out, err := runExternalServerBD(t, projectDir, switchEnv,
			"init", "--reinit-local", "--prefix", "la", "--database", "la", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
		if err == nil {
			t.Fatalf("reinit against an unreachable server must be refused, got success:\n%s", out)
		}
		if !strings.Contains(out, "not found on server") {
			t.Errorf("wanted the missing-database guard's refusal, got:\n%s", out)
		}
		if strings.Contains(out, "--destroy-token") {
			t.Errorf("the store-switch guard must not have run; the earlier guard already refused:\n%s", out)
		}
	})
}

// TestInitReinitLocalKeepsManagedServerStore is the GH#3495 regression lock:
// the reporter's bare reinit command, run against a CORRECTLY recorded
// server workspace — same database, no explicit alternate mode — must keep
// using the existing server store. Mode inheritance (bd init's own, #3885)
// already covers this workspace, which is exactly why the bug bit only after
// a mode override appeared; this test pins that the store-switch guard does
// not widen into the reporter's original command. A zero-issue store with
// custom types is the shape a count-based preflight cannot see, so the
// assertions below are the ones that would catch a silent switch.
//
// Predicted to pass at the base; if it ever fails, mode inheritance or the
// guard's no-switch path regressed.
func TestInitReinitLocalKeepsManagedServerStore(t *testing.T) {
	beadsDir := startProjectServerModeGuardFixture(t, "la")
	port, err := strconv.Atoi(os.Getenv("BEADS_DOLT_SERVER_PORT"))
	if err != nil {
		t.Fatalf("fixture must pin BEADS_DOLT_SERVER_PORT: %v", err)
	}
	if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), Version); err != nil {
		t.Fatalf("write local version: %v", err)
	}

	projectDir := filepath.Dir(beadsDir)
	env := initGuardE2EEnv(t, port)

	// A real Gas City-shaped server workspace: the database exists on the
	// server (--recreate-missing is the documented way to put it there), the
	// recorded metadata says server mode, and the store holds zero issues
	// plus database-stored custom types.
	setupOut, err := runExternalServerBD(t, projectDir, env,
		"init", "--prefix", "la", "--database", "la", "--skip-hooks", "--skip-agents", "--recreate-missing")
	if err != nil {
		t.Fatalf("setup bd init --recreate-missing: %v\n%s", err, setupOut)
	}
	if out, err := runExternalServerBD(t, projectDir, env, "config", "set", "types.custom", "molecule,convoy"); err != nil {
		t.Fatalf("bd config set types.custom: %v\n%s", err, out)
	}
	if out, err := runExternalServerBD(t, projectDir, env, "config", "set", "gc.endpoint_origin", "managed_city"); err != nil {
		t.Fatalf("bd config set gc.endpoint_origin: %v\n%s", err, out)
	}
	if out, err := runExternalServerBD(t, projectDir, env, "config", "get", "types.custom"); err != nil || strings.TrimSpace(out) != "molecule,convoy" {
		t.Fatalf("fixture: bd config get types.custom = %q (err=%v), want molecule,convoy", out, err)
	}

	metadataBefore, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata after setup: %v", err)
	}
	var recorded struct {
		DoltMode     string `json:"dolt_mode"`
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(metadataBefore, &recorded); err != nil {
		t.Fatalf("parse metadata after setup: %v\n%s", err, metadataBefore)
	}
	if recorded.DoltMode != "server" || recorded.DoltDatabase != "la" {
		t.Fatalf("fixture: recorded %s/%s, want server/la", recorded.DoltMode, recorded.DoltDatabase)
	}

	// The reporter's bare reinit: same database, no explicit alternate mode.
	out, err := runExternalServerBD(t, projectDir, env,
		"init", "--reinit-local", "--prefix", "la", "--database", "la", "--non-interactive", "--role", "maintainer")
	if err != nil {
		t.Fatalf("the reporter's bare reinit must succeed on a correctly recorded server workspace: %v\n%s", err, out)
	}

	// Still the same server store: recorded mode and database unchanged, the
	// custom types preserved, no parallel embedded database, and the database
	// still on the server.
	metadataAfter, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata after reinit: %v", err)
	}
	var after struct {
		DoltMode     string `json:"dolt_mode"`
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(metadataAfter, &after); err != nil {
		t.Fatalf("parse metadata after reinit: %v\n%s", err, metadataAfter)
	}
	if after.DoltMode != "server" || after.DoltDatabase != "la" {
		t.Errorf("reinit switched the recorded store: %s/%s, want server/la (the reporter's command names no override)", after.DoltMode, after.DoltDatabase)
	}
	if got, err := runExternalServerBD(t, projectDir, env, "config", "get", "types.custom"); err != nil || strings.TrimSpace(got) != "molecule,convoy" {
		t.Errorf("reinit lost the server store's custom types: got %q (err=%v), want molecule,convoy", got, err)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); statErr == nil {
		t.Errorf("reinit created a parallel embedded database at %s", filepath.Join(beadsDir, "embeddeddolt"))
	}
	if check := checkDatabaseOnServer("127.0.0.1", port, "root", "", "la", false); !check.Reachable || !check.Exists {
		t.Errorf("the recorded database is no longer on the server: %+v", check)
	}
}
