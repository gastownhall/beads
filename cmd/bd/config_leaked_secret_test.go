package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/notion"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// recordingWorkspaceConfig is the config plane reduced to the two verbs
// removeStoredSecretRow uses, so a test can assert what it did NOT call.
type recordingWorkspaceConfig struct {
	issueops.WorkspaceConfig

	stored   map[string]string
	getErr   error
	unsetErr error

	gets   []string
	unsets []string
}

func (c *recordingWorkspaceConfig) GetSetting(_ context.Context, req issueops.GetSettingRequest) (issueops.SettingResult, error) {
	c.gets = append(c.gets, req.Key)
	if c.getErr != nil {
		return issueops.SettingResult{}, c.getErr
	}
	return issueops.SettingResult{Key: req.Key, Value: c.stored[req.Key]}, nil
}

func (c *recordingWorkspaceConfig) UnsetSetting(_ context.Context, req issueops.UnsetSettingRequest) (issueops.UnsetSettingResult, error) {
	c.unsets = append(c.unsets, req.Key)
	if c.unsetErr != nil {
		return issueops.UnsetSettingResult{}, c.unsetErr
	}
	delete(c.stored, req.Key)
	return issueops.UnsetSettingResult{Key: req.Key}, nil
}

// TestRemoveStoredSecretRowDeletesTheLeakedRow is the GH#6676 regression: a
// workspace configured before notion.token became yaml-only has the token in
// the config table, which `bd dolt push` replicated. Moving the key to
// config.yaml made `bd config unset` return before the store, which left that
// row with no remover at all in embedded mode.
func TestRemoveStoredSecretRowDeletesTheLeakedRow(t *testing.T) {
	settings := &recordingWorkspaceConfig{stored: map[string]string{"notion.token": "secret-ntn-abc"}}

	present, removed, err := removeStoredSecretRow(context.Background(), settings, "notion.token")
	if err != nil {
		t.Fatalf("removeStoredSecretRow: %v", err)
	}
	if !present {
		t.Error("present = false, want true — the row was there to read")
	}
	if !removed {
		t.Error("removed = false, want true — the caller reports nothing to the user when this is false, so the leaked row would be deleted silently or not at all")
	}
	if got := settings.unsets; len(got) != 1 || got[0] != "notion.token" {
		t.Errorf("unset calls = %v, want exactly [notion.token]", got)
	}
	if _, still := settings.stored["notion.token"]; still {
		t.Error("the row survived the removal")
	}
}

// TestRemoveStoredSecretRowIsSilentWhenNothingLeaked pins the read-before-delete.
// Deleting an absent key is a success that reports no miss, so without the read
// every `bd config unset notion.token` would claim it had cleaned up a database
// row — on the overwhelmingly common workspace that never had one.
func TestRemoveStoredSecretRowIsSilentWhenNothingLeaked(t *testing.T) {
	for name, stored := range map[string]map[string]string{
		"no row at all":     {},
		"row stored empty":  {"notion.token": ""},
		"row of whitespace": {"notion.token": "   \n"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := &recordingWorkspaceConfig{stored: stored}

			present, removed, err := removeStoredSecretRow(context.Background(), settings, "notion.token")
			if err != nil {
				t.Fatalf("removeStoredSecretRow: %v", err)
			}
			if present {
				t.Error("present = true, want false — nothing usable was stored, so nothing is still in the database")
			}
			if removed {
				t.Error("removed = true, want false — this would tell the user a credential was deleted from the database when none was there")
			}
			if len(settings.unsets) != 0 {
				t.Errorf("unset calls = %v, want none: the row was already absent", settings.unsets)
			}
		})
	}
}

// TestRemoveStoredSecretRowPropagatesFailures keeps a failed cleanup
// distinguishable from a clean workspace. Both report removed=false, so only
// the error tells the caller to warn instead of staying quiet — reporting
// success here is the exact harm GH#6676 is about: the user believes the
// secret is gone while it is still in the pushed database.
//
// The two failures are NOT the same warning. A failed read means bd does not
// know whether anything leaked; a failed delete means it does, and the
// credential is still in the table `bd dolt push` replicates. That is what the
// `present` bit carries out to the caller.
func TestRemoveStoredSecretRowPropagatesFailures(t *testing.T) {
	readFailed := errors.New("store unreachable")
	deleteFailed := errors.New("delete rejected")

	t.Run("read fails", func(t *testing.T) {
		settings := &recordingWorkspaceConfig{stored: map[string]string{}, getErr: readFailed}

		present, removed, err := removeStoredSecretRow(context.Background(), settings, "notion.token")
		if !errors.Is(err, readFailed) {
			t.Fatalf("err = %v, want %v", err, readFailed)
		}
		if present {
			t.Error("present = true after a failed read — the read is what would have told us, so this claims knowledge bd does not have")
		}
		if removed {
			t.Error("removed = true after a failed read")
		}
		if len(settings.unsets) != 0 {
			t.Errorf("deleted %v despite not knowing what was there", settings.unsets)
		}
	})

	t.Run("delete fails", func(t *testing.T) {
		settings := &recordingWorkspaceConfig{
			stored:   map[string]string{"notion.token": "secret-ntn-abc"},
			unsetErr: deleteFailed,
		}

		present, removed, err := removeStoredSecretRow(context.Background(), settings, "notion.token")
		if !errors.Is(err, deleteFailed) {
			t.Fatalf("err = %v, want %v", err, deleteFailed)
		}
		if !present {
			t.Error("present = false, want true — the read found the row, so this is the certain case, not an inconclusive lookup")
		}
		if removed {
			t.Error("removed = true, want false — the row is still in the database")
		}
	})
}

// TestYamlOnlySecretKeysAreTheWholeClass pins the population the unset branch
// cleans up. The fix is deliberately keyed on the IsYamlOnlyKey/IsSecretKey
// pair rather than on "notion.token", because every tracker credential reached
// yaml-only status the same way and would otherwise each need their own fix.
//
// IsYamlOnlyKey also matches whole prefixes (`ai.`, `sync.`, `federation.`,
// ...), so this list is a floor, not a census.
func TestYamlOnlySecretKeysAreTheWholeClass(t *testing.T) {
	for _, key := range []string{
		"notion.token",
		"github.token",
		"gitlab.token",
		"jira.api_token",
		"linear.api_key",
		"linear.oauth_client_secret",
		"ado.pat",
		"ai.api_key",
	} {
		if !config.IsYamlOnlyKey(key) {
			t.Errorf("%s: IsYamlOnlyKey = false, so config unset never reaches the branch this fix lives in", key)
		}
		if !config.IsSecretKey(key) {
			t.Errorf("%s: IsSecretKey = false, so its leaked database row would be left behind", key)
		}
	}

	// The other side of the gate: an ordinary yaml-only key must not drag a
	// database open into a command that has no row to clean up.
	for _, key := range []string{"routing.mode", "backup.enabled", "no-db"} {
		if !config.IsYamlOnlyKey(key) {
			t.Errorf("%s: expected a yaml-only key for this half of the test", key)
		}
		if config.IsSecretKey(key) {
			t.Errorf("%s: IsSecretKey = true, which would open the store on an ordinary unset", key)
		}
	}
}

// TestNotionStatusWarnsOnlyForTheLeakedSource is the user-visible half of the
// same GH#6676 gap. The distinct auth source is only worth having if something
// acts on it: this is the one place a workspace is told that the token it is
// authenticating with sits in a table `bd dolt push` replicates.
//
// The negative cases matter as much as the positive one — warning a workspace
// whose token is in config.yaml or the environment would train users to ignore
// the line that matters.
func TestNotionStatusWarnsOnlyForTheLeakedSource(t *testing.T) {
	for _, tc := range []struct {
		name     string
		source   notion.AuthSource
		wantWarn bool
	}{
		{"legacy database row", notion.AuthSourceDatabaseLegacy, true},
		{"config.yaml", notion.AuthSourceConfigToken, false},
		{"environment", notion.AuthSourceEnv, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)

			renderNotionStatus(cmd,
				&notion.ResolvedAuth{Token: "secret-ntn-abcdef", Source: tc.source},
				notionConfig{DataSourceID: "ds-1"},
				&notion.StatusResponse{})

			got := out.String()
			if warned := strings.Contains(got, "bd config unset notion.token"); warned != tc.wantWarn {
				t.Errorf("rotation warning present = %v, want %v; output:\n%s", warned, tc.wantWarn, got)
			}
			if !strings.Contains(got, "Auth source: "+string(tc.source)) {
				t.Errorf("auth source line missing for %q; output:\n%s", tc.source, got)
			}
			if strings.Contains(got, "secret-ntn-abcdef") {
				t.Errorf("status printed the raw token; output:\n%s", got)
			}
		})
	}
}

// leakedRowStore is the production store reduced to the one accessor the
// cleanup reaches through. Its nil embedded storage makes any other call panic,
// so these tests cannot silently exercise more of the store than the seam.
type leakedRowStore struct {
	storage.DoltStorage
	settings *recordingWorkspaceConfig
}

func (s *leakedRowStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return s.settings, nil
}

// newLeakedSecretWorkspace builds the workspace shapes GH#6676 produced: a
// .beads directory that may or may not hold a config.yaml, and may or may not
// hold a database directory — which is what FindDatabasePath answers on, and
// therefore what decides whether the cleanup may open a store at all.
func newLeakedSecretWorkspace(t *testing.T, configYAML string, withDatabase bool) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if configYAML != "" {
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
	}
	if withDatabase {
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
			t.Fatalf("create embeddeddolt: %v", err)
		}
	}
	t.Setenv("BEADS_DIR", beadsDir)
	initConfigForTest(t)
	return beadsDir
}

// installLeakedRowSettings puts the recording config plane behind the store
// accessor and marks the store active, so openWorkspaceConfig hands it back
// without opening — or creating — anything.
func installLeakedRowSettings(t *testing.T, settings *recordingWorkspaceConfig) {
	t.Helper()
	saveAndRestoreGlobals(t)
	store = &leakedRowStore{settings: settings}
	setStoreActive(true)
}

// runConfigUnset drives the real cobra command, which is the point: every other
// test in this file stubs in below the wiring.
func runConfigUnset(t *testing.T, key string, asJSON bool) (stdout, stderr string, err error) {
	t.Helper()
	savedJSON := jsonOutput
	jsonOutput = asJSON
	t.Cleanup(func() { jsonOutput = savedJSON })

	stdout, stderr = captureStdio(t, func() {
		err = configUnsetCmd.RunE(configUnsetCmd, []string{key})
	})
	return stdout, stderr, err
}

func captureStdio(t *testing.T, fn func()) (string, string) {
	t.Helper()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr
	func() {
		defer func() { os.Stdout, os.Stderr = savedOut, savedErr }()
		fn()
	}()
	_ = wOut.Close()
	_ = wErr.Close()

	var outBuf, errBuf bytes.Buffer
	if _, err := io.Copy(&outBuf, rOut); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if _, err := io.Copy(&errBuf, rErr); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	_ = rOut.Close()
	_ = rErr.Close()
	return outBuf.String(), errBuf.String()
}

// snapshotBeadsStorage is the "did this command create a database" probe: every
// path under .beads with its size, so a new embeddeddolt/ directory, a gate
// lock or a grown file shows up as a difference instead of having to be
// predicted.
//
// config.yaml is excluded because editing it IS the command's contract; every
// other byte under .beads is storage that `bd config unset` has no business
// bringing into existence. removed_backend_heal_embedded_test.go has a
// `cgo`-tagged twin of this walk; this file must also compile in the
// CGO_ENABLED=0 lane, which does not see that one.
func snapshotBeadsStorage(t *testing.T, beadsDir string) []string {
	t.Helper()
	var entries []string
	if err := filepath.Walk(beadsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(beadsDir, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			entries = append(entries, rel+"/")
			return nil
		}
		if rel == "config.yaml" {
			return nil
		}
		entries = append(entries, fmt.Sprintf("%s (%d bytes)", rel, info.Size()))
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", beadsDir, err)
	}
	sort.Strings(entries)
	return entries
}

// TestConfigUnsetCommandCleansTheLeakedRow pins the wiring, which is the one
// thing the unit tests above cannot see: they call removeStoredSecretRow
// directly, so deleting the cleanup call in the command left them all green.
//
// The negative half is the same assertion the cleanup's comment makes about
// itself — an ordinary yaml-only key must not reach the store at all — and it
// is checked against a recorder that fails on any unexpected call.
func TestConfigUnsetCommandCleansTheLeakedRow(t *testing.T) {
	t.Run("secret key: the row is read, deleted and reported", func(t *testing.T) {
		beadsDir := newLeakedSecretWorkspace(t, "notion:\n  token: yaml-copy\n", true)
		settings := &recordingWorkspaceConfig{stored: map[string]string{"notion.token": "leaked-db-value"}}
		installLeakedRowSettings(t, settings)

		stdout, _, err := runConfigUnset(t, "notion.token", false)
		if err != nil {
			t.Fatalf("config unset notion.token: %v", err)
		}
		if got := settings.gets; len(got) != 1 || got[0] != "notion.token" {
			t.Errorf("GetSetting calls = %v, want exactly [notion.token]", got)
		}
		if got := settings.unsets; len(got) != 1 || got[0] != "notion.token" {
			t.Errorf("UnsetSetting calls = %v, want exactly [notion.token] — the command never reached the cleanup", got)
		}
		if !strings.Contains(stdout, "Also removed a stored notion.token row") {
			t.Errorf("the removal was not reported; stdout:\n%s", stdout)
		}
		if !strings.Contains(stdout, "Rotate this credential") {
			t.Errorf("the rotation obligation was not reported; stdout:\n%s", stdout)
		}
		if yaml := readWorkspaceYAML(t, beadsDir); strings.Contains(yaml, "\n  token: yaml-copy") {
			t.Errorf("the config.yaml copy survived:\n%s", yaml)
		}
	})

	t.Run("non-secret yaml-only key: the store is never reached", func(t *testing.T) {
		newLeakedSecretWorkspace(t, "routing:\n  mode: hybrid\n", true)
		settings := &recordingWorkspaceConfig{stored: map[string]string{"notion.token": "leaked-db-value"}}
		installLeakedRowSettings(t, settings)

		if _, _, err := runConfigUnset(t, "routing.mode", false); err != nil {
			t.Fatalf("config unset routing.mode: %v", err)
		}
		if len(settings.gets)+len(settings.unsets) != 0 {
			t.Errorf("an ordinary yaml-only unset touched the config plane: gets=%v unsets=%v", settings.gets, settings.unsets)
		}
	})

	t.Run("--json carries the database half", func(t *testing.T) {
		newLeakedSecretWorkspace(t, "notion:\n  token: yaml-copy\n", true)
		settings := &recordingWorkspaceConfig{stored: map[string]string{"notion.token": "leaked-db-value"}}
		installLeakedRowSettings(t, settings)

		stdout, _, err := runConfigUnset(t, "notion.token", true)
		if err != nil {
			t.Fatalf("config unset --json: %v", err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("unmarshal %q: %v", stdout, err)
		}
		if payload["database_row_removed"] != true {
			t.Errorf("database_row_removed = %v, want true; payload: %v", payload["database_row_removed"], payload)
		}
	})
}

// TestConfigUnsetDoesNotProvisionADatabase is the store-creation regression.
// `config unset <yaml-only key>` is classified runnable with no store, and the
// direct open is open-OR-CREATE, so a best-effort cleanup that reaches for the
// store unconditionally turns a YAML edit into a 2.1 MB embedded database in a
// workspace that never had one.
//
// Both assertions are load-bearing, because removing the gate can fail either
// way: the open succeeds and materializes the database (caught by the directory
// snapshot), or it fails and the command reports an unreachable database
// (caught by the stderr assertion). Nothing about a workspace with no database
// warrants either.
func TestConfigUnsetDoesNotProvisionADatabase(t *testing.T) {
	beadsDir := newLeakedSecretWorkspace(t, "notion:\n  token: yaml-copy\n", false)
	saveAndRestoreGlobals(t)
	store = nil
	setStoreActive(false)

	before := snapshotBeadsStorage(t, beadsDir)

	stdout, stderr, err := runConfigUnset(t, "notion.token", false)
	if err != nil {
		t.Fatalf("config unset notion.token: %v", err)
	}
	if after := snapshotBeadsStorage(t, beadsDir); !reflect.DeepEqual(before, after) {
		t.Errorf("unsetting a yaml-only secret changed .beads/:\nbefore: %v\nafter:  %v", before, after)
	}
	if strings.Contains(stderr, "Could not check the database") {
		t.Errorf("the command tried to reach a database that does not exist; stderr:\n%s", stderr)
	}
	if !strings.Contains(stdout, "Unset notion.token") {
		t.Errorf("the YAML half did not run; stdout:\n%s", stdout)
	}
}

// TestConfigUnsetCleansTheRowWithNoLocalDatabaseDirectory pins the two arms of
// the provisioning gate that the snapshot test above cannot see. It is that
// test's mirror: "no local database directory" is the state the three share, so
// the route to the database is the only thing that distinguishes them, and
// neither arm had a behavioral test — the clause a later simplification is most
// likely to read as redundant was the one clause nothing defended.
//
// Both shapes are workspaces whose database is real and remote. That is the
// population whose row `bd dolt push` replicated the furthest, so a gate that
// answers "no database here" from the local disk silently skips the cleanup
// exactly where it matters most, and exits 0 while it does.
func TestConfigUnsetCleansTheRowWithNoLocalDatabaseDirectory(t *testing.T) {
	// The metadata-less server workspace: config.yaml names the server, there is
	// nothing on disk to discover (FindDatabasePath answers on metadata.json,
	// embeddeddolt/ and dolt/ — none of which exist here), and PersistentPreRun
	// has already connected the store. gc provisions exactly this shape on every
	// `gc-beads-bd start` — see metadataless_server_workspace_test.go.
	t.Run("server mode, store already connected: the row is read and deleted", func(t *testing.T) {
		beadsDir := newLeakedSecretWorkspace(t, "dolt:\n  mode: server\n  server_host: 127.0.0.1\nnotion:\n  token: yaml-copy\n", false)
		settings := &recordingWorkspaceConfig{stored: map[string]string{"notion.token": "leaked-db-value"}}
		installLeakedRowSettings(t, settings)

		before := snapshotBeadsStorage(t, beadsDir)

		stdout, _, err := runConfigUnset(t, "notion.token", false)
		if err != nil {
			t.Fatalf("config unset notion.token: %v", err)
		}
		if got := settings.gets; len(got) != 1 || got[0] != "notion.token" {
			t.Errorf("GetSetting calls = %v, want exactly [notion.token] — the cleanup was skipped for a server workspace whose row is on the server", got)
		}
		if got := settings.unsets; len(got) != 1 || got[0] != "notion.token" {
			t.Errorf("UnsetSetting calls = %v, want exactly [notion.token] — the leaked row was left on the shared server", got)
		}
		if !strings.Contains(stdout, "Also removed a stored notion.token row") {
			t.Errorf("the removal was not reported; stdout:\n%s", stdout)
		}
		// The gate's purpose still has to hold on this arm: reaching an
		// already-connected store must not bring a local database into being.
		if after := snapshotBeadsStorage(t, beadsDir); !reflect.DeepEqual(before, after) {
			t.Errorf("cleaning the row provisioned local storage:\nbefore: %v\nafter:  %v", before, after)
		}
	})

	// The proxied route, which has no local database directory by construction.
	// The store is deliberately left inactive so this subtest exercises the
	// proxied arm alone rather than the connected-store arm above; the proxied
	// provider is not initialized in-process, so "reached" shows up as the
	// best-effort could-not-check report. Silence is the failure being pinned.
	t.Run("proxied server: the cleanup is reached, not silently skipped", func(t *testing.T) {
		newLeakedSecretWorkspace(t, "notion:\n  token: yaml-copy\n", false)
		saveAndRestoreGlobals(t)
		store = nil
		setStoreActive(false)
		origProxied := proxiedServerMode
		t.Cleanup(func() { proxiedServerMode = origProxied })
		proxiedServerMode = true

		stdout, stderr, err := runConfigUnset(t, "notion.token", false)
		if err != nil {
			t.Fatalf("config unset notion.token: %v", err)
		}
		if !strings.Contains(stderr, "Could not check the database") {
			t.Errorf("the proxied workspace's row was never looked for; stderr:\n%s", stderr)
		}
		if !strings.Contains(stdout, "Unset notion.token") {
			t.Errorf("the YAML half did not run; stdout:\n%s", stdout)
		}
	})
}

// TestConfigUnsetCleansTheRowWhenTheYamlUnsetFails covers the population the
// cleanup exists for and the YAML half cannot serve: a workspace whose token
// leaked into the database and whose config.yaml is gone (or holds a shape the
// unset refuses). Gating the cleanup on the YAML edit succeeding leaves the
// credential in the table `bd dolt push` replicates while telling the user to
// run `bd init` — and `bd notion status` points at this very command.
func TestConfigUnsetCleansTheRowWhenTheYamlUnsetFails(t *testing.T) {
	newLeakedSecretWorkspace(t, "", true)
	settings := &recordingWorkspaceConfig{stored: map[string]string{"notion.token": "leaked-db-value"}}
	installLeakedRowSettings(t, settings)

	_, stderr, err := runConfigUnset(t, "notion.token", false)
	if err == nil {
		t.Fatal("config unset succeeded with no config.yaml, want the YAML failure to still be reported")
	}
	if got := settings.unsets; len(got) != 1 || got[0] != "notion.token" {
		t.Errorf("UnsetSetting calls = %v, want exactly [notion.token] — the leaked row survived a failure on the other copy", got)
	}
	if !strings.Contains(stderr, "stored notion.token row WAS removed") {
		t.Errorf("the error never mentions the database row; stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "rotate that credential") {
		t.Errorf("the rotation obligation was dropped on the failure path; stderr:\n%s", stderr)
	}
}

// TestConfigUnsetDistinguishesADeleteFailure keeps the one certain case out of
// the hedged one. A failed read means bd does not know whether anything leaked;
// a failed delete means it read the row and it is still there — the single
// outcome that warrants "a live credential is in the pushed table", and the one
// that was being rendered as an unreachable database.
func TestConfigUnsetDistinguishesADeleteFailure(t *testing.T) {
	deleteFailed := errors.New("delete rejected")

	t.Run("human output names the surviving row", func(t *testing.T) {
		newLeakedSecretWorkspace(t, "notion:\n  token: yaml-copy\n", true)
		settings := &recordingWorkspaceConfig{
			stored:   map[string]string{"notion.token": "leaked-db-value"},
			unsetErr: deleteFailed,
		}
		installLeakedRowSettings(t, settings)

		_, stderr, err := runConfigUnset(t, "notion.token", false)
		if err != nil {
			t.Fatalf("config unset notion.token: %v", err)
		}
		if !strings.Contains(stderr, "still in the database") {
			t.Errorf("a failed delete was not reported as a surviving row; stderr:\n%s", stderr)
		}
		if strings.Contains(stderr, "Could not check the database") {
			t.Errorf("a failed delete was reported as an inconclusive lookup; stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Rotate this credential") {
			t.Errorf("the rotation obligation was dropped; stderr:\n%s", stderr)
		}
	})

	t.Run("--json separates present-but-undeleted from unreachable", func(t *testing.T) {
		newLeakedSecretWorkspace(t, "notion:\n  token: yaml-copy\n", true)
		settings := &recordingWorkspaceConfig{
			stored:   map[string]string{"notion.token": "leaked-db-value"},
			unsetErr: deleteFailed,
		}
		installLeakedRowSettings(t, settings)

		stdout, _, err := runConfigUnset(t, "notion.token", true)
		if err != nil {
			t.Fatalf("config unset --json: %v", err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("unmarshal %q: %v", stdout, err)
		}
		if payload["database_row_present"] != true {
			t.Errorf("database_row_present = %v, want true; payload: %v", payload["database_row_present"], payload)
		}
		if _, claimed := payload["database_unreachable"]; claimed {
			t.Errorf("a reachable database that refused the delete was reported unreachable; payload: %v", payload)
		}
		if got, ok := payload["database_delete_failed"].(string); !ok || !strings.Contains(got, "delete rejected") {
			t.Errorf("database_delete_failed = %v, want the delete error; payload: %v", payload["database_delete_failed"], payload)
		}
	})
}

func readWorkspaceYAML(t *testing.T, beadsDir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	return string(content)
}
