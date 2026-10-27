//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// seedPrefixServerFixture builds a server-mode workspace whose config.yaml
// carries an issue-prefix and nothing recoverable (no sync.remote, no backup,
// no issues.jsonl), so the plan hinges entirely on what the database probe
// says. projectID and shadowDir select the two shapes the seed-prefix plan
// arrives in: a populated shadow directory (the probe runs inside
// existingBootstrapDBPlan) and a ProjectID-less, shadow-less workspace (the
// probe only runs at detectBootstrapPlan's init fall-through).
func seedPrefixServerFixture(t *testing.T, projectID string, shadowDir bool) (beadsDir string, cfg *configfile.Config) {
	t.Helper()
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: la\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize failed: %v", err)
	}
	t.Chdir(tmpDir)
	cfg = configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = filepath.Join(tmpDir, "dolt-data")
	cfg.ProjectID = projectID
	cfg.DoltServerPort = 13307
	t.Setenv("BEADS_DOLT_DATA_DIR", cfg.DoltDataDir)
	if shadowDir {
		if err := os.MkdirAll(filepath.Join(cfg.DoltDataDir, "mydb"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return beadsDir, cfg
}

// fakeSeedPrefixProbe swaps checkBootstrapServerDB for one answering check and
// counts probes. Callers defer the returned restore.
func fakeSeedPrefixProbe(t *testing.T, check bootstrapServerDBCheck) *int {
	t.Helper()
	probes := 0
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probes++
		return check
	}
	t.Cleanup(func() { checkBootstrapServerDB = orig })
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	t.Cleanup(func() { bootstrapRetryDelay = origDelay })
	return &probes
}

// TestDetectBootstrapAction_ServerDBMissingPrefixPlansSeedPrefix pins the
// GH#3494 core: a server-mode workspace whose database EXISTS but never got an
// issue_prefix row (the config.yaml-only shape `gc init`/`gc rig add`
// produce) must plan "seed-prefix", not "none"/"init". The plan carries
// HasExisting, is not Blocked, and its reason names the prefix and its source.
func TestDetectBootstrapAction_ServerDBMissingPrefixPlansSeedPrefix(t *testing.T) {
	beadsDir, cfg := seedPrefixServerFixture(t, "proj-seed-1", true)
	probes := fakeSeedPrefixProbe(t, bootstrapServerDBCheck{Exists: true, Reachable: true, Prefix: "", PrefixKnown: true})

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "seed-prefix" {
		t.Fatalf("action = %q (reason %q), want seed-prefix", plan.Action, plan.Reason)
	}
	if !plan.HasExisting {
		t.Error("plan.HasExisting = false, want true: the database exists")
	}
	if plan.Blocked {
		t.Error("plan.Blocked = true, want false: seeding an existing database is an action, not a refusal")
	}
	if !strings.Contains(plan.Reason, `"la"`) || !strings.Contains(plan.Reason, "config.yaml issue-prefix") {
		t.Errorf("reason = %q, want it to name the prefix \"la\" and its source config.yaml issue-prefix", plan.Reason)
	}
	if *probes != 1 {
		t.Errorf("probes = %d, want 1: the settled shadow-dir verdict reuses the plan-time probe", *probes)
	}

	// --dry-run --json consumers switch on these exact fields.
	blob, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	if fields["action"] != "seed-prefix" {
		t.Errorf("json action = %v, want seed-prefix", fields["action"])
	}
	if has, _ := fields["has_existing"].(bool); !has {
		t.Errorf("json has_existing = %v, want true", fields["has_existing"])
	}
	if _, blocked := fields["blocked"]; blocked {
		t.Errorf("json carries \"blocked\" for a seed-prefix plan: %s", blob)
	}

	out := captureStdout(t, func() error {
		printBootstrapPlan(plan)
		return nil
	})
	if !strings.Contains(out, "Bootstrap plan: set missing issue_prefix on existing database") {
		t.Errorf("printBootstrapPlan output does not announce the seed-prefix plan:\n%s", out)
	}
}

// TestDetectBootstrapAction_ServerNameOnlyDBMissingPrefixPlansSeedPrefix
// covers the shape GH#3494 actually ships in: no project_id and no local
// shadow directory, so the plan-time probe never ran and the database is
// discovered only at the init fall-through — with retryTransient=false, one
// round-trip, no 70s budget. A database found there with no issue_prefix also
// plans seed-prefix; one that already HAS a prefix plans "none" (bootstrap
// never offers to rewrite a set prefix).
func TestDetectBootstrapAction_ServerNameOnlyDBMissingPrefixPlansSeedPrefix(t *testing.T) {
	t.Run("database exists without issue_prefix", func(t *testing.T) {
		beadsDir, cfg := seedPrefixServerFixture(t, "", false)
		probes := fakeSeedPrefixProbe(t, bootstrapServerDBCheck{Exists: true, Reachable: true, Prefix: "", PrefixKnown: true})

		plan := detectBootstrapAction(beadsDir, cfg)

		if plan.Action != "seed-prefix" {
			t.Fatalf("action = %q (reason %q), want seed-prefix", plan.Action, plan.Reason)
		}
		if !plan.HasExisting || plan.Blocked {
			t.Errorf("HasExisting=%v Blocked=%v, want true/false", plan.HasExisting, plan.Blocked)
		}
		if *probes != 1 {
			t.Errorf("probes = %d, want 1 — the fall-through probe must run exactly once", *probes)
		}
	})

	t.Run("database exists with a prefix", func(t *testing.T) {
		beadsDir, cfg := seedPrefixServerFixture(t, "", false)
		probes := fakeSeedPrefixProbe(t, bootstrapServerDBCheck{Exists: true, Reachable: true, Prefix: "xx", PrefixKnown: true})

		plan := detectBootstrapAction(beadsDir, cfg)

		if plan.Action != "none" {
			t.Fatalf("action = %q (reason %q), want none: an existing prefix is never rewritten", plan.Action, plan.Reason)
		}
		if !plan.HasExisting {
			t.Error("plan.HasExisting = false, want true")
		}
		if plan.Blocked {
			t.Error("plan.Blocked = true, want false: the database exists, so this is the benign none")
		}
		if *probes != 1 {
			t.Errorf("probes = %d, want 1", *probes)
		}
	})

	t.Run("database absent stays an init plan", func(t *testing.T) {
		beadsDir, cfg := seedPrefixServerFixture(t, "", false)
		probes := fakeSeedPrefixProbe(t, bootstrapServerDBCheck{Exists: false, Reachable: true})

		plan := detectBootstrapAction(beadsDir, cfg)

		if plan.Action != "init" {
			t.Fatalf("action = %q (reason %q), want init: a database the probe did not find is a fresh setup", plan.Action, plan.Reason)
		}
		if plan.HasExisting {
			t.Error("plan.HasExisting = true, want false")
		}
		if *probes != 1 {
			t.Errorf("probes = %d, want 1", *probes)
		}
	})

	t.Run("unreachable server stays an init plan", func(t *testing.T) {
		beadsDir, cfg := seedPrefixServerFixture(t, "", false)
		probes := fakeSeedPrefixProbe(t, bootstrapServerDBCheck{Reachable: false, Err: errors.New("dial tcp 127.0.0.1:13307: connect: connection refused")})

		plan := detectBootstrapAction(beadsDir, cfg)

		if plan.Action != "init" {
			t.Fatalf("action = %q (reason %q), want init: an unreachable server is UNKNOWN, never 'exists'", plan.Action, plan.Reason)
		}
		if plan.HasExisting {
			t.Error("plan.HasExisting = true, want false")
		}
		if *probes != 1 {
			t.Errorf("probes = %d, want 1", *probes)
		}
	})
}

// TestDetectBootstrapAction_EmbeddedDBMissingPrefixPlansSeedPrefix is the
// embedded twin of the seed-prefix plan: an existing .beads/embeddeddolt
// database whose issue_prefix row is empty plans seed-prefix from config.yaml,
// and one that already has a prefix plans "none".
func TestDetectBootstrapAction_EmbeddedDBMissingPrefixPlansSeedPrefix(t *testing.T) {
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: la\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize failed: %v", err)
	}
	t.Chdir(tmpDir)
	cfg := configfile.DefaultConfig()
	cfg.DoltDatabase = "mydb"

	orig := readBootstrapEmbeddedPrefix
	readBootstrapEmbeddedPrefix = func(context.Context, string) (string, error) { return "", nil }
	t.Cleanup(func() { readBootstrapEmbeddedPrefix = orig })

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "seed-prefix" {
		t.Fatalf("action = %q (reason %q), want seed-prefix", plan.Action, plan.Reason)
	}
	if !plan.HasExisting || plan.Blocked {
		t.Errorf("HasExisting=%v Blocked=%v, want true/false", plan.HasExisting, plan.Blocked)
	}

	readBootstrapEmbeddedPrefix = func(context.Context, string) (string, error) { return "xx", nil }
	plan = detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "none" {
		t.Fatalf("action = %q (reason %q), want none: an existing embedded prefix is never rewritten", plan.Action, plan.Reason)
	}
	if !plan.HasExisting || plan.Blocked {
		t.Errorf("HasExisting=%v Blocked=%v, want true/false", plan.HasExisting, plan.Blocked)
	}
}
