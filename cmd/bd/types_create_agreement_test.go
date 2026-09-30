//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// typesAgreementRunner runs bd in a workspace and returns stdout (or stdout+stderr on
// failure) plus the exit error.
type typesAgreementRunner func(t *testing.T, args ...string) ([]byte, error)

// assertTypesAgreeWithCreate is the `bd types` ⇔ `bd create --type` contract:
// every type `bd types --json` lists is accepted by create and by
// storage-class config validation, and an unlisted type is rejected.
// Custom types are seeded from both sources — the database (bd config set)
// and the project's .beads/config.yaml — because each storage mode resolves
// them; the listing and validation paths must compose them identically.
func assertTypesAgreeWithCreate(t *testing.T, run typesAgreementRunner, beadsDir string) {
	t.Helper()

	if out, err := run(t, "config", "set", "types.custom", "dbtype"); err != nil {
		t.Fatalf("config set types.custom: %v\n%s", err, out)
	}
	cfgPath := filepath.Join(beadsDir, "config.yaml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if strings.Contains(string(cfg), "types.custom") || strings.Contains(string(cfg), "\ntypes:") {
		t.Fatalf("fixture config.yaml already declares types; test assumes it does not:\n%s", cfg)
	}
	cfg = append(cfg, []byte("\ntypes.custom: \"yamltype\"\n")...)
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	out, err := run(t, "types", "--json")
	if err != nil {
		t.Fatalf("types --json: %v\n%s", err, out)
	}
	var listed struct {
		CoreTypes   []typeInfo `json:"core_types"`
		SystemTypes []typeInfo `json:"system_types"`
		CustomTypes []string   `json:"custom_types"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("unmarshal types --json: %v\n%s", err, out)
	}
	names := map[string]bool{}
	for _, ti := range append(listed.CoreTypes, listed.SystemTypes...) {
		names[ti.Name] = true
	}
	for _, c := range listed.CustomTypes {
		names[c] = true
	}

	// Types create is known to accept must be listed.
	for _, want := range []string{"task", "message", "molecule", "gate", "event", "dbtype", "yamltype"} {
		if !names[want] {
			t.Errorf("bd types does not list %q, but bd create accepts it; listed=%v", want, names)
		}
	}

	// Every listed type must be accepted by create ...
	for name := range names {
		if out, err := run(t, "create", "--silent", "--type", name, "agreement "+name); err != nil {
			t.Errorf("bd types lists %q but bd create --type %s failed: %v\n%s", name, name, err, out)
		}
	}
	// ... and by storage-class validation, which names issue types too.
	for _, name := range listed.CustomTypes {
		if out, err := run(t, "config", "set", "storage-class."+name, "versioned"); err != nil {
			t.Errorf("bd types lists %q but config set storage-class.%s failed: %v\n%s", name, name, err, out)
		}
	}

	// An unlisted type is rejected.
	if out, err := run(t, "create", "--silent", "--type", "notregistered", "agreement unlisted"); err == nil {
		t.Errorf("bd create accepted unlisted type notregistered: %s", out)
	}
}

func TestProxiedServerTypesAgreeWithCreate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "tac")
	assertTypesAgreeWithCreate(t, func(t *testing.T, args ...string) ([]byte, error) {
		return bdProxiedRun(t, bd, p.dir, args...)
	}, p.beadsDir)
}

func TestEmbeddedTypesAgreeWithCreate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tac")
	assertTypesAgreeWithCreate(t, func(t *testing.T, args ...string) ([]byte, error) {
		return bdRunWithFlockRetry(t, bd, dir, args...)
	}, beadsDir)
}

// Server mode (`bd init --server` against an external dolt sql-server) lists
// through DoltStore.GetCustomTypes, the resolver that used to drop
// config.yaml types once the database had any.
func TestServerModeTypesAgreeWithCreate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "tas")
	assertTypesAgreeWithCreate(t, func(t *testing.T, args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = p.dir
		cmd.Env = p.env
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			return append(stdout.Bytes(), stderr.Bytes()...), err
		}
		return stdout.Bytes(), nil
	}, p.beadsDir)
}
