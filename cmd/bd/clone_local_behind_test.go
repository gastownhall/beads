package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestCloneLocalRefusalRendering pins #7038's AC-6: the typed clone-local
// refusal's CLI surface, on both output arms. The human output must be the
// refusal block — the one-line mismatch, the per-database migration command,
// the escape hatch with its risk spelled out — rather than the raw
// unknown-column SQL error the read used to die on; and `--json` must emit one
// parseable error document whose typed clone_local_schema block carries the
// versions and the missing columns, with no prose around it: an agent parsing
// the structured stream gets the document and nothing else.
//
// Deliberately self-contained, and deliberately free of any reference to the
// refusal's Go type: it pins rendered output, so it must run unchanged
// against a base whose reads fail on the raw column error.
func TestCloneLocalRefusalRendering(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	// newRegressedWorkspace builds the #7038 workspace: a server-mode config
	// and a database on the test server in the pull-then-upgrade shape —
	// main series current, clone-local series behind with granted_node
	// (ignored/0016) missing, no local dolt data dir, no upgrade witness, so
	// the very first read meets the refusal.
	newRegressedWorkspace := func(prefix string) (run func(args ...string) (string, string, int)) {
		repo := t.TempDir()
		beadsDir := filepath.Join(repo, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("create .beads: %v", err)
		}
		database := uniqueTestDBName(t)
		if err := (&configfile.Config{
			Backend:        configfile.BackendDolt,
			DoltMode:       configfile.DoltModeServer,
			DoltServerHost: "127.0.0.1",
			DoltServerPort: testDoltServerPort,
			DoltDatabase:   database,
		}).Save(beadsDir); err != nil {
			t.Fatalf("save metadata: %v", err)
		}
		// These two setup touches keep the CLI's startup chatter off stderr
		// so the JSON subtest can pin that the refusal path itself prepends
		// no prose to the structured stream: resolve the server port through
		// the port file (the deprecated metadata fallback prints a warning
		// when it is the source), and open .beads with the recommended
		// permissions (0o755 draws a warning).
		if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PortFileName),
			[]byte(strconv.Itoa(testDoltServerPort)), 0o600); err != nil {
			t.Fatalf("write port file: %v", err)
		}
		if err := os.Chmod(beadsDir, 0o700); err != nil {
			t.Fatalf("chmod .beads: %v", err)
		}
		ctx := context.Background()
		store, err := dolt.New(ctx, &dolt.Config{
			Path:            filepath.Join(beadsDir, "dolt"),
			BeadsDir:        beadsDir,
			ServerHost:      "127.0.0.1",
			ServerPort:      testDoltServerPort,
			Database:        database,
			CreateIfMissing: true,
		})
		if err != nil {
			t.Fatalf("create test store: %v", err)
		}
		if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}
		now := time.Now()
		if err := store.CreateIssue(ctx, &types.Issue{
			ID:        prefix + "-1",
			Title:     "Readable once the migration completes",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		}, "test-user"); err != nil {
			t.Fatalf("create issue: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close setup store: %v", err)
		}
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
		admin, err := sql.Open("mysql", doltutil.ServerDSN{
			Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database,
		}.String())
		if err != nil {
			t.Fatalf("connect to test dolt server: %v", err)
		}
		t.Cleanup(func() { admin.Close() })
		// Both objects are dolt-ignored, so the working set stays clean.
		for _, stmt := range []string{
			"ALTER TABLE leases DROP COLUMN granted_node",
			"DELETE FROM ignored_schema_migrations WHERE version > 13",
		} {
			if _, err := admin.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		bd := buildBDForInitTests(t)
		run = func(args ...string) (string, string, int) {
			t.Helper()
			cmd := exec.Command(bd, args...)
			cmd.Dir = repo
			cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
				"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv,
				schema.AllowMigrateEnv, "BD_IGNORE_SCHEMA_SKEW"),
				"HOME="+t.TempDir(),
				"XDG_CONFIG_HOME="+t.TempDir(),
				"BEADS_TEST_MODE=1",
				"BEADS_TEST_SERVER=1",
				"BEADS_DIR="+beadsDir,
				schema.AllowRemoteMigrateEnv+"=0",
			)
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			code := 0
			if err := cmd.Run(); err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("running bd %v: %v", args, err)
				}
				code = exitErr.ExitCode()
			}
			return stdout.String(), stderr.String(), code
		}
		return run
	}

	t.Run("human output is the typed refusal block, not a raw SQL error", func(t *testing.T) {
		run := newRegressedWorkspace("clr")

		stdout, stderr, code := run("list")
		if code == 0 {
			t.Fatalf("bd list succeeded on a schema its reads cannot query\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		for _, want := range []string{
			"clone-local schema mismatch",
			"granted_node",
			"bd migrate schema",
			"BD_IGNORE_SCHEMA_SKEW=1 bd <command>",
			"never arrive with `bd dolt pull`",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("refusal block does not carry %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stderr, "Unknown column") {
			t.Errorf("the refusal let a query run and fail on the raw column instead of refusing at open:\n%s", stderr)
		}
	})

	t.Run("json output is one parseable document with the typed block", func(t *testing.T) {
		run := newRegressedWorkspace("clj")

		stdout, stderr, code := run("list", "--json")
		if code == 0 {
			t.Fatalf("bd list --json succeeded on a schema its reads cannot query\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		// The structured stream carries the error document and nothing else:
		// no prose lines around it for an agent's parser to trip on.
		trimmed := strings.TrimSpace(stderr)
		if !json.Valid([]byte(trimmed)) {
			t.Fatalf("stderr under --json is not one JSON document (prose leaked into the structured stream):\n%s", stderr)
		}
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			t.Fatalf("unmarshal error document: %v\n%s", err, stderr)
		}
		// BD_JSON_ENVELOPE wraps the same inner document under "data".
		if data, ok := doc["data"].(map[string]interface{}); ok {
			doc = data
		}
		msg, _ := doc["error"].(string)
		if !strings.Contains(msg, "clone-local schema mismatch") || !strings.Contains(msg, "granted_node") {
			t.Errorf("json error %q does not name the clone-local mismatch and the missing column", msg)
		}
		block, ok := doc["clone_local_schema"].(map[string]interface{})
		if !ok {
			t.Fatalf("error document has no clone_local_schema block:\n%s", stderr)
		}
		latest := schema.LatestIgnoredVersion()
		if got, _ := block["latest_local_version"].(float64); int(got) != latest {
			t.Errorf("clone_local_schema.latest_local_version = %v, want %d", block["latest_local_version"], latest)
		}
		current, _ := block["current_local_version"].(float64)
		if int(current) <= 0 || int(current) >= latest {
			t.Errorf("clone_local_schema.current_local_version = %v, want an effective cursor between 0 and %d", block["current_local_version"], latest)
		}
		if got, _ := block["severity"].(string); got != "blocking" {
			t.Errorf("clone_local_schema.severity = %v, want blocking", block["severity"])
		}
		if got, _ := block["gateway"].(bool); got {
			t.Errorf("clone_local_schema.gateway = true, want false: this CLI open is not a gateway's")
		}
		if got, _ := block["main_version"].(float64); got != block["latest_main_version"] {
			t.Errorf("clone_local_schema main_version = %v but latest_main_version = %v; the pull brought the shared schema current", block["main_version"], block["latest_main_version"])
		}
		cols, _ := block["missing_columns"].([]interface{})
		if len(cols) != 1 || cols[0] != "granted_node" {
			t.Errorf("clone_local_schema.missing_columns = %v, want [granted_node]", block["missing_columns"])
		}
	})
}

// TestRenderTypedOpenErrorCloneLocalBehind covers renderTypedOpenError's
// clone-local arm in-process. The refusal reaches it wrapped, as a refused
// store open does, and must come out as the full guidance block on a human
// run (plus the --global verb when the invocation targeted the global
// database) and as one valid JSON error carrying the remedy under --json,
// never as a one-line `%v` of the raw error. A gateway refusal sends the
// operator to the server administrator and names no command on either arm.
func TestRenderTypedOpenErrorCloneLocalBehind(t *testing.T) {
	orig := globalFlag
	t.Cleanup(func() { globalFlag = orig })
	globalFlag = false

	direct := func() *schema.CloneLocalBehindError {
		return &schema.CloneLocalBehindError{
			DBIgnoredVersion: 11, LatestIgnoredVersion: 31,
			MissingColumns: []string{"granted_node"},
			MainVersion:    74, LatestMainVersion: 74,
		}
	}
	render := func(t *testing.T, refusal *schema.CloneLocalBehindError) string {
		t.Helper()
		var handled bool
		out := captureNoticeStderr(t, func() {
			handled = renderTypedOpenError(fmt.Errorf("failed to open database: %w", refusal))
		})
		if !handled {
			t.Fatal("renderTypedOpenError did not handle a wrapped CloneLocalBehindError")
		}
		return out
	}

	t.Run("human", func(t *testing.T) {
		pinJSONOutput(t, false)
		refusal := direct()
		out := render(t, refusal)
		if out != refusal.UserMessage() {
			t.Errorf("human refusal is not the UserMessage block:\n%s", out)
		}
		for _, want := range []string{"granted_node", "bd dolt pull", "bd migrate schema", "no further shared-schema"} {
			if !strings.Contains(out, want) {
				t.Errorf("human refusal does not mention %q:\n%s", want, out)
			}
		}
	})

	t.Run("human main behind too", func(t *testing.T) {
		pinJSONOutput(t, false)
		refusal := direct()
		refusal.MainVersion = 72
		out := render(t, refusal)
		if !strings.Contains(out, "designated-migrator") || strings.Contains(out, "no further shared-schema") {
			t.Errorf("a main series behind too must send the operator through the migrator procedure first, not claim main is current:\n%s", out)
		}
	})

	t.Run("human --global", func(t *testing.T) {
		pinJSONOutput(t, false)
		globalFlag = true
		t.Cleanup(func() { globalFlag = false })
		out := render(t, direct())
		if !strings.Contains(out, schema.SharedConsentCommandGlobal) {
			t.Errorf("under --global the refusal must name %q:\n%s", schema.SharedConsentCommandGlobal, out)
		}
	})

	t.Run("json", func(t *testing.T) {
		pinJSONOutput(t, true)
		t.Setenv("BD_JSON_ENVELOPE", "")
		out := render(t, direct())
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("--json refusal is not one JSON document: %v\n%s", err, out)
		}
		if hint, _ := parsed["hint"].(string); !strings.Contains(hint, "bd migrate schema") {
			t.Errorf("hint = %q, want the per-database remedy", hint)
		}
		detail, ok := parsed["clone_local_schema"].(map[string]interface{})
		if !ok {
			t.Fatalf("clone_local_schema missing or wrong type: %T\n%s", parsed["clone_local_schema"], out)
		}
		if detail["command"] != schema.SharedConsentCommand || detail["gateway"] != false ||
			fmt.Sprint(detail["missing_columns"]) != "[granted_node]" {
			t.Errorf("clone_local_schema = %v", detail)
		}
	})

	t.Run("json --global", func(t *testing.T) {
		pinJSONOutput(t, true)
		t.Setenv("BD_JSON_ENVELOPE", "")
		globalFlag = true
		t.Cleanup(func() { globalFlag = false })
		out := render(t, direct())
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("--json refusal is not one JSON document: %v\n%s", err, out)
		}
		detail, _ := parsed["clone_local_schema"].(map[string]interface{})
		if detail["command"] != schema.SharedConsentCommandGlobal {
			t.Errorf("clone_local_schema.command under --global = %v, want %q", detail["command"], schema.SharedConsentCommandGlobal)
		}
	})

	t.Run("gateway", func(t *testing.T) {
		for _, jsonMode := range []bool{false, true} {
			pinJSONOutput(t, jsonMode)
			t.Setenv("BD_JSON_ENVELOPE", "")
			refusal := direct()
			refusal.Gateway = true
			out := render(t, refusal)
			if !strings.Contains(out, "server administrator") || strings.Contains(out, "bd migrate schema") {
				t.Errorf("gateway refusal (json=%v) must send the operator to the server administrator, not to `bd migrate schema`:\n%s", jsonMode, out)
			}
			if jsonMode {
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(out), &parsed); err != nil {
					t.Fatalf("--json gateway refusal is not one JSON document: %v\n%s", err, out)
				}
				if _, has := parsed["clone_local_schema"].(map[string]interface{})["command"]; has {
					t.Errorf("gateway JSON refusal names a command: %s", out)
				}
			}
		}
	})
}
