package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// writeReinitTargetMetadata writes metadata.json into beadsDir, creating it.
func writeReinitTargetMetadata(t *testing.T, beadsDir string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// requireExitCode fails unless err is an *exitError carrying want.
func requireExitCode(t *testing.T, err error, want int) {
	t.Helper()
	var exitErr *exitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exitError with code %d, got %v", want, err)
	}
	if exitErr.Code != want {
		t.Fatalf("want exit code %d, got %d", want, exitErr.Code)
	}
}

// setRecreateMissing sets initAllowRecreateMissing, what RunE reads
// --recreate-missing into, for the rest of the test.
func setRecreateMissing(t *testing.T, allow bool) {
	t.Helper()
	orig := initAllowRecreateMissing
	initAllowRecreateMissing = allow
	t.Cleanup(func() { initAllowRecreateMissing = orig })
}

// TestRunInitReinitPreflight_CountErrorRefuses pins GH#5628 part 1: when the
// preflight cannot count the database it is about to destroy, --reinit-local
// must refuse rather than fall through to the destructive path with no count,
// no destroy token and no confirmation.
//
// BEADS_DIR names a different workspace than the one passed in, so the
// refusal must be about the argument (the gate's target), not about whatever
// BEADS_DIR or discovery would have found.
func TestRunInitReinitPreflight_CountErrorRefuses(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, "target", ".beads")
	// An unloadable metadata.json makes the store open (and so the count) fail.
	writeReinitTargetMetadata(t, beadsDir, []byte("{not json"))
	otherDir := filepath.Join(root, "other", ".beads")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", otherDir)

	if _, err := countReinitTargetIssues(beadsDir, "bd"); err == nil {
		t.Fatal("precondition: countReinitTargetIssues should fail on an unloadable metadata.json")
	}

	var err error
	out := captureStderr(t, func() {
		err = runInitReinitPreflight(true, false, beadsDir, "bd", "bd", "")
	})
	if err == nil {
		t.Fatalf("runInitReinitPreflight returned nil after the count failed: the destroy confirmation was silently skipped\n%s", out)
	}
	requireExitCode(t, err, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Target: "+beadsDir) {
		t.Errorf("warning must name the target %s, got:\n%s", beadsDir, out)
	}
	if strings.Contains(out, otherDir) {
		t.Errorf("warning names BEADS_DIR's workspace instead of the target, got:\n%s", out)
	}
	if !strings.Contains(out, "could not count them") {
		t.Errorf("warning must report the count error, got:\n%s", out)
	}
	if strings.Contains(out, "DESTROY-") {
		t.Errorf("refusal must never print the destroy token value (ADR-0002 Invariant 4), got:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); statErr != nil {
		t.Errorf("the target must be left unchanged: %v", statErr)
	}
}

// TestConfirmReinitDestroy pins the confirmation for a known and an unknown
// issue count, interactive and not.
func TestConfirmReinitDestroy(t *testing.T) {
	const target = "/work/project/.beads"
	countErr := errors.New("open store: connection refused")
	tests := []struct {
		name        string
		count       int
		countErr    error
		interactive bool
		answer      string
		token       string
		wantCode    int // 0 means proceed (nil error)
		wantOut     []string
	}{
		{
			name:     "unknown count, non-interactive, no token",
			countErr: countErr,
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Existing issues: unknown (could not count them: open store: connection refused)", "bd doctor", "Refusing to destroy an unknown number of issues", "bd help init-safety"},
		},
		{
			name:     "unknown count, non-interactive, wrong token",
			countErr: countErr,
			token:    "DESTROY-other",
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Refusing to destroy an unknown number of issues"},
		},
		{
			name:     "unknown count, non-interactive, token",
			countErr: countErr,
			token:    "DESTROY-bd",
			wantOut:  []string{"Existing issues: unknown", "Destroy token accepted"},
		},
		{
			name:        "unknown count, interactive, confirmed",
			countErr:    countErr,
			interactive: true,
			answer:      "destroy all issues\n",
			wantOut:     []string{"Existing issues: unknown (could not count them: open store: connection refused)", "Type 'destroy all issues' to confirm"},
		},
		{
			name:        "unknown count, interactive, declined",
			countErr:    countErr,
			interactive: true,
			answer:      "yes\n",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Type 'destroy all issues' to confirm", "Aborted. Database was NOT modified."},
		},
		{
			name:        "unknown count, interactive, a count is not all",
			countErr:    countErr,
			interactive: true,
			answer:      "destroy 0 issues\n",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
		{
			name:     "known count, non-interactive, no token",
			count:    3,
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Existing issues: 3", "Refusing to destroy 3 issues in non-interactive mode."},
		},
		{
			name:    "known count, non-interactive, token",
			count:   3,
			token:   "DESTROY-bd",
			wantOut: []string{"Existing issues: 3", "Destroy token accepted"},
		},
		{
			name:        "known count, interactive, confirmed",
			count:       3,
			interactive: true,
			answer:      "destroy 3 issues\n",
			wantOut:     []string{"Type 'destroy 3 issues' to confirm"},
		},
		{
			name:        "known count, interactive, declined",
			count:       3,
			interactive: true,
			answer:      "no\n",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := confirmReinitDestroy(&buf, strings.NewReader(tt.answer), tt.interactive, target, tt.count, tt.countErr, "bd", tt.token)
			out := buf.String()
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("want nil (proceed), got %v\n%s", err, out)
				}
			} else {
				requireExitCode(t, err, tt.wantCode)
			}
			if !strings.Contains(out, "Target: "+target) {
				t.Errorf("warning must name the target, got:\n%s", out)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q, got:\n%s", want, out)
				}
			}
			if strings.Contains(out, "DESTROY-") {
				t.Errorf("output must never print the destroy token value, got:\n%s", out)
			}
		})
	}

	t.Run("count error is spelled, not printed raw", func(t *testing.T) {
		var buf bytes.Buffer
		err := confirmReinitDestroy(&buf, strings.NewReader(""), false, target, 0, errors.New("bad \x1b[2Jterminal"), "bd", "")
		requireExitCode(t, err, ExitDestroyTokenMissing)
		if strings.Contains(buf.String(), "\x1b") {
			t.Errorf("count error reached the terminal raw:\n%q", buf.String())
		}
	})
}

// TestCountReinitTargetIssues pins what the reinit preflight treats as an
// uncountable workspace: one it cannot open is an error, never zero. Only
// positive evidence that there is nothing to destroy counts as zero, and a
// target without project files is settled without opening a store, so the
// count creates nothing before the confirmation.
func TestCountReinitTargetIssues(t *testing.T) {
	t.Run("missing directory counts zero and stays missing", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		count, err := countReinitTargetIssues(beadsDir, "myproject")
		if err != nil || count != 0 {
			t.Fatalf("want 0, nil; got %d, %v", count, err)
		}
		if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
			t.Fatalf("counting a missing target created it: %v", statErr)
		}
	})

	for name, files := range map[string][]string{
		"empty directory":        nil,
		"only a legacy registry": {"registry.json"},
	} {
		t.Run(name+" counts zero and creates nothing", func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(beadsDir, f), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			count, err := countReinitTargetIssues(beadsDir, "myproject")
			if err != nil || count != 0 {
				t.Fatalf("want 0, nil; got %d, %v", count, err)
			}
			entries, readErr := os.ReadDir(beadsDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != len(files) {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Fatalf("counting a target without project files created entries in it: %v", names)
			}
		})
	}

	t.Run("unparseable metadata.json is an error", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, []byte("{not json"))
		if _, err := countReinitTargetIssues(beadsDir, "myproject"); err == nil {
			t.Fatal("want an error for an unloadable metadata.json, got nil")
		}
	})

	t.Run("server mode with a local copy and no server is an error", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
		t.Setenv("BEADS_DOLT_AUTO_START", "0")
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		data, _ := json.Marshal(map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
			"project_id":    "existing-0000-1111-2222-333344445555",
		})
		writeReinitTargetMetadata(t, beadsDir, data)
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "myproject", ".dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := countReinitTargetIssues(beadsDir, "myproject"); err == nil {
			t.Fatal("want an error: a local copy of the database is never proof that it is absent")
		}
	})

	t.Run("server mode with no local copy and no server is an error", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
		t.Setenv("BEADS_DOLT_AUTO_START", "0")
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		data, _ := json.Marshal(map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
			"project_id":    "existing-0000-1111-2222-333344445555",
		})
		writeReinitTargetMetadata(t, beadsDir, data)
		if _, err := countReinitTargetIssues(beadsDir, "myproject"); err == nil {
			t.Fatal("want an error: a server that cannot be reached never proves the database absent")
		}
	})

	// The cases below answer for the store the count opens.
	serverMetadata := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"myproject","project_id":"existing-0000-1111-2222-333344445555"}`)
	stubOpen := func(t *testing.T, store storage.DoltStorage, err error) {
		t.Helper()
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
		t.Setenv("BEADS_DOLT_DATA_DIR", "")
		orig := openReinitTarget
		openReinitTarget = func(context.Context, string) (storage.DoltStorage, error) { return store, err }
		t.Cleanup(func() { openReinitTarget = orig })
	}

	t.Run("under --recreate-missing the open's own not-found for a server-mode database with no local copy counts zero", func(t *testing.T) {
		setRecreateMissing(t, true)
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, serverMetadata)
		stubOpen(t, nil, fmt.Errorf("open: %w", dolt.ErrDatabaseNotFound))
		count, err := countReinitTargetIssues(beadsDir, "myproject")
		if err != nil || count != 0 {
			t.Fatalf("want 0, nil; got %d, %v", count, err)
		}
	})

	// The server the count's open dials need not be the one init's store
	// dials, so without the opt-in its not-found is no count.
	t.Run("without --recreate-missing the open's own not-found is an error", func(t *testing.T) {
		setRecreateMissing(t, false)
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, serverMetadata)
		stubOpen(t, nil, fmt.Errorf("open: %w", dolt.ErrDatabaseNotFound))
		count, err := countReinitTargetIssues(beadsDir, "myproject")
		if !errors.Is(err, dolt.ErrDatabaseNotFound) {
			t.Fatalf("want the open's not-found as the count error, got %d, %v", count, err)
		}
	})

	t.Run("a store that reports no statistics is an error", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, serverMetadata)
		stubOpen(t, reinitStatsStore{}, nil)
		if count, err := countReinitTargetIssues(beadsDir, "myproject"); err == nil {
			t.Fatalf("want an error: no statistics is no count, got %d, nil", count)
		}
	})

	t.Run("the statistics' total is the count", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, serverMetadata)
		stubOpen(t, reinitStatsStore{stats: &types.Statistics{TotalIssues: 7}}, nil)
		count, err := countReinitTargetIssues(beadsDir, "myproject")
		if err != nil || count != 7 {
			t.Fatalf("want 7, nil; got %d, %v", count, err)
		}
	})

	// The count is of the database init replaces, myproject here, so a store
	// that opens another one counts nothing, however empty that one is.
	unnamedServerMetadata := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","project_id":"existing-0000-1111-2222-333344445555"}`)
	emptyStore := reinitStatsStore{stats: &types.Statistics{}}
	for _, tc := range []struct {
		name     string
		metadata []byte
		env      string // BEADS_DOLT_SERVER_DATABASE
		store    storage.DoltStorage
		openErr  error
		opened   string // the database the store opens
	}{
		{name: "an empty beads when metadata.json omits dolt_database", metadata: unnamedServerMetadata, store: emptyStore, opened: "beads"},
		{name: "the not-found of beads when metadata.json omits dolt_database", metadata: unnamedServerMetadata, openErr: fmt.Errorf("open: %w", dolt.ErrDatabaseNotFound), opened: "beads"},
		{name: "an empty database BEADS_DOLT_SERVER_DATABASE names", metadata: serverMetadata, env: "other", store: emptyStore, opened: "other"},
	} {
		t.Run(tc.name+" is an error", func(t *testing.T) {
			// Even under the opt-in that lets a not-found count zero.
			setRecreateMissing(t, true)
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			writeReinitTargetMetadata(t, beadsDir, tc.metadata)
			stubOpen(t, tc.store, tc.openErr)
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.env)
			count, err := countReinitTargetIssues(beadsDir, "myproject")
			if err == nil {
				t.Fatalf("want an error: the store opens %s, not myproject, the database init replaces; got %d, nil", tc.opened, count)
			}
			if tc.store != nil && !strings.Contains(err.Error(), fmt.Sprintf("opens database %q, but re-initializing replaces database %q", tc.opened, "myproject")) {
				t.Errorf("the error must name the database the store opens and the one init replaces, got: %v", err)
			}
		})
	}

	t.Run("BEADS_DOLT_SERVER_DATABASE naming the database init replaces counts it", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, unnamedServerMetadata)
		stubOpen(t, reinitStatsStore{stats: &types.Statistics{TotalIssues: 4}}, nil)
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "myproject")
		count, err := countReinitTargetIssues(beadsDir, "myproject")
		if err != nil || count != 4 {
			t.Fatalf("want 4, nil; got %d, %v", count, err)
		}
	})

	// A .beads holding only config.yaml has project files but no metadata, so
	// its store opens the default database, "beads".
	configYAMLOnly := func(t *testing.T) (beadsDir string, opened *int) {
		t.Helper()
		beadsDir = filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: myproject\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
		opened = new(int)
		orig := openReinitTarget
		openReinitTarget = func(context.Context, string) (storage.DoltStorage, error) {
			*opened++
			return reinitStatsStore{stats: &types.Statistics{TotalIssues: 2}}, nil
		}
		t.Cleanup(func() { openReinitTarget = orig })
		return beadsDir, opened
	}

	t.Run("a .beads holding only config.yaml is an error for another database than beads, before any open", func(t *testing.T) {
		beadsDir, opened := configYAMLOnly(t)
		count, err := countReinitTargetIssues(beadsDir, "myproject")
		if err == nil {
			t.Fatalf("want an error: the store opens beads, not myproject, the database init replaces; got %d, nil", count)
		}
		if !strings.Contains(err.Error(), `opens database "beads", but re-initializing replaces database "myproject"`) {
			t.Errorf("the error must name the database the store opens and the one init replaces, got: %v", err)
		}
		if *opened != 0 {
			t.Errorf("the count opened the store %d time(s); a name it cannot count must be settled before the open creates anything", *opened)
		}
	})

	t.Run("a .beads holding only config.yaml counts beads through its store", func(t *testing.T) {
		beadsDir, opened := configYAMLOnly(t)
		count, err := countReinitTargetIssues(beadsDir, "beads")
		if err != nil || count != 2 {
			t.Fatalf("want 2, nil; got %d, %v", count, err)
		}
		if *opened != 1 {
			t.Errorf("want the count to open the store once, got %d", *opened)
		}
	})
}

// reinitStatsStore is a store the count opens: it answers GetStatistics with
// stats and closes. Any other method panics (the embedded store is nil).
type reinitStatsStore struct {
	storage.DoltStorage
	stats *types.Statistics
}

func (s reinitStatsStore) GetStatistics(context.Context) (*types.Statistics, error) {
	return s.stats, nil
}

func (reinitStatsStore) Close() error { return nil }

// TestCountReinitTargetIssues_ServerReportsDatabaseAbsent is the one open
// failure that can mean there is nothing to destroy: a server-mode workspace
// with no local copy of its database, whose reachable server reports that
// database absent. Under --recreate-missing that counts zero, so the opt-in
// against a reachable server still needs no destroy token. Without it the
// answer is about the server the count's open dialed, which need not be the
// one init's store dials, so the preflight asks for the token. A local copy of
// the database turns the answer back into an error under the opt-in too.
// Needs a Dolt binary.
func TestCountReinitTargetIssues_ServerReportsDatabaseAbsent(t *testing.T) {
	beadsDir := startProjectServerModeGuardFixture(t, "myproject")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	setRecreateMissing(t, false)
	count, err := countReinitTargetIssues(beadsDir, "myproject")
	if !errors.Is(err, dolt.ErrDatabaseNotFound) {
		t.Fatalf("want the real open's not-found as the count error without --recreate-missing; got %d, %v", count, err)
	}
	var out string
	out = captureStderr(t, func() {
		err = runInitReinitPreflight(true, false, beadsDir, "myproject", "myproject", "")
	})
	requireExitCode(t, err, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Existing issues: unknown") {
		t.Errorf("warning must report the unknown count, got:\n%s", out)
	}

	setRecreateMissing(t, true)
	count, err = countReinitTargetIssues(beadsDir, "myproject")
	if err != nil || count != 0 {
		t.Fatalf("want 0, nil under --recreate-missing for a database the reachable server reports absent; got %d, %v", count, err)
	}
	out = captureStderr(t, func() {
		err = runInitReinitPreflight(true, false, beadsDir, "myproject", "myproject", "")
	})
	if err != nil {
		t.Fatalf("preflight must pass under --recreate-missing with nothing to destroy, got %v\n%s", err, out)
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("preflight warned with nothing to destroy:\n%s", out)
	}

	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "myproject", ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := countReinitTargetIssues(beadsDir, "myproject"); err == nil {
		t.Fatal("want an error: a local copy of the database is never proof that it is absent, whatever the server reports")
	}
}

// TestReinitTargetDatabaseAbsent pins each rule that keeps a failed open from
// counting zero under --recreate-missing (without the opt-in none does,
// TestCountReinitTargetIssues). The open fails with the store's not-found
// unless a case says otherwise, and init replaces "myproject" unless a case
// names another database, so only the rule a case breaks can make the answer
// false: the guard's server-mode predicate (a config.yaml dolt.shared-server
// alone is not enough, GH#2946), no local copy in any place bd keeps one, an
// open that asked about the database init replaces, and an open that failed
// because its own server did not list the database. An open that failed any
// other way proves nothing, even one whose message reads like the not-found:
// no second connection, on another socket, user or host, is asked instead.
func TestReinitTargetDatabaseAbsent(t *testing.T) {
	notFound := fmt.Errorf("open: %w", dolt.ErrDatabaseNotFound)
	serverMetadata := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"myproject","project_id":"existing-0000-1111-2222-333344445555"}`)
	unnamedServerMetadata := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","project_id":"existing-0000-1111-2222-333344445555"}`)
	embeddedMetadata := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject","project_id":"existing-0000-1111-2222-333344445555"}`)
	sharedServerYAML := func(t *testing.T, beadsDir, _ string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdirs := func(t *testing.T, dirs ...string) {
		t.Helper()
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	embeddedDatabase := func(name string) func(t *testing.T, beadsDir, _ string) {
		return func(t *testing.T, beadsDir, _ string) {
			t.Helper()
			marker := filepath.Join(beadsDir, "embeddeddolt", name, ".dolt")
			mkdirs(t, marker)
			if err := os.WriteFile(filepath.Join(marker, "manifest"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	tests := []struct {
		name     string
		metadata []byte
		env      map[string]string
		setup    []func(t *testing.T, beadsDir, sharedDir string)
		openErr  error  // what the count's open returned; nil means notFound
		dbName   string // the database init replaces; "" means myproject
		want     bool
	}{
		{
			name:     "server mode with no local copy, reported absent by the open's server",
			metadata: serverMetadata,
			want:     true,
		},
		{
			name:     "server mode only from config.yaml dolt.shared-server",
			metadata: embeddedMetadata,
			setup:    []func(t *testing.T, beadsDir, sharedDir string){sharedServerYAML},
		},
		{
			name:     "GH#2946 embedded database under config.yaml dolt.shared-server",
			metadata: embeddedMetadata,
			setup:    []func(t *testing.T, beadsDir, sharedDir string){sharedServerYAML, embeddedDatabase("myproject")},
		},
		{
			name:     "server mode with an embedded database under another name",
			metadata: serverMetadata,
			setup:    []func(t *testing.T, beadsDir, sharedDir string){embeddedDatabase("legacy")},
		},
		{
			name:     "server mode with an embedded marker for its database",
			metadata: serverMetadata,
			setup: []func(t *testing.T, beadsDir, sharedDir string){func(t *testing.T, beadsDir, _ string) {
				mkdirs(t, filepath.Join(beadsDir, "embeddeddolt", "myproject", ".dolt"))
			}},
		},
		{
			name:     "server mode with a local copy in its data directory",
			metadata: serverMetadata,
			setup: []func(t *testing.T, beadsDir, sharedDir string){func(t *testing.T, beadsDir, _ string) {
				mkdirs(t, filepath.Join(beadsDir, "dolt", "myproject", ".dolt"))
			}},
		},
		{
			name:     "shared-server mode from its own config.yaml with a copy in the shared data directory",
			metadata: serverMetadata,
			setup: []func(t *testing.T, beadsDir, sharedDir string){sharedServerYAML, func(t *testing.T, _, sharedDir string) {
				mkdirs(t, filepath.Join(sharedDir, "dolt", "myproject", ".dolt"))
			}},
		},
		{
			name:     "an open that could not reach its server",
			metadata: serverMetadata,
			openErr:  errors.New("Dolt server unreachable at unix:/tmp/dolt.sock: connection refused"),
		},
		{
			name:     "an open its gateway refused",
			metadata: serverMetadata,
			openErr:  errors.New(`failed to connect to gateway server 127.0.0.1:3307 (database "myproject"): access denied`),
		},
		{
			name:     "an open error that only reads like the not-found",
			metadata: serverMetadata,
			openErr:  errors.New(`database "myproject" not found on Dolt server at 127.0.0.1:3307`),
		},
		{
			name:     "BEADS_DOLT_SERVER_DATABASE names another database than metadata.json",
			metadata: serverMetadata,
			env:      map[string]string{"BEADS_DOLT_SERVER_DATABASE": "other"},
		},
		{
			name:     "metadata.json omits dolt_database: the open asked about beads, init replaces the prefix's database",
			metadata: unnamedServerMetadata,
		},
		{
			name:     "metadata.json omits dolt_database and BEADS_DOLT_SERVER_DATABASE names the database init replaces",
			metadata: unnamedServerMetadata,
			env:      map[string]string{"BEADS_DOLT_SERVER_DATABASE": "myproject"},
			want:     true,
		},
		{
			name:     "--database names another database than metadata.json",
			metadata: serverMetadata,
			dbName:   "explicit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sharedDir := t.TempDir()
			for k, v := range map[string]string{
				"BEADS_DOLT_SERVER_MODE":     "",
				"BEADS_DOLT_SHARED_SERVER":   "",
				"BEADS_DOLT_SERVER_DATABASE": "",
				"BEADS_DOLT_DATA_DIR":        "",
				"BEADS_SHARED_SERVER_DIR":    sharedDir,
			} {
				t.Setenv(k, v)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			writeReinitTargetMetadata(t, beadsDir, tt.metadata)
			for _, setup := range tt.setup {
				setup(t, beadsDir, sharedDir)
			}
			openErr := tt.openErr
			if openErr == nil {
				openErr = notFound
			}
			dbName := tt.dbName
			if dbName == "" {
				dbName = "myproject"
			}
			if got := reinitTargetDatabaseAbsent(beadsDir, dbName, openErr); got != tt.want {
				t.Fatalf("reinitTargetDatabaseAbsent = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestInitReinitLocal_CountsTheGateTargetFromLinkedWorktree pins RunE's
// wiring for GH#5628 part 2: the preflight counts the .beads RunE hands the
// init gate, not the one discovery picks. It calls RunE directly with
// BEADS_DIR unset, where that gate target is the main checkout's .beads
// (GetWorktreeFallbackBeadsDir) while discovery prefers the worktree's own
// .beads, which holds a database. The CLI never reaches RunE in that state:
// the root pre-run exports the discovered workspace as BEADS_DIR, so from a
// worktree with its own database bd counts, and replaces, the worktree's
// .beads.
func TestInitReinitLocal_CountsTheGateTargetFromLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitAt(t, mainDir)
	for _, args := range [][]string{
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init"},
		{"worktree", "add", filepath.Join(root, "wt")},
	} {
		if out, err := initArtifactGitCommand(mainDir, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	metadata := []byte(`{"database":"dolt","backend":"dolt"}`)
	mainBeads := filepath.Join(mainDir, ".beads")
	writeReinitTargetMetadata(t, mainBeads, metadata)
	// The worktree's own .beads holds a database of its own, so discovery
	// picks it.
	wtBeads := filepath.Join(root, "wt", ".beads")
	writeReinitTargetMetadata(t, wtBeads, metadata)
	if err := os.MkdirAll(filepath.Join(wtBeads, "embeddeddolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(filepath.Join(root, "wt"))
	git.ResetCaches()
	beads.ResetCaches()
	t.Cleanup(func() {
		git.ResetCaches()
		beads.ResetCaches()
	})
	stubCallerBeadsDir(t, "")
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BD_NON_INTERACTIVE", "1")
	setInitFlag(t, "reinit-local", "true")
	setInitFlag(t, "prefix", "wt")
	setInitFlag(t, "skip-hooks", "true")
	oldCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldCtx })

	wantTarget := utils.CanonicalizePath(mainBeads)
	origCount := countReinitTarget
	countReinitTarget = func(beadsDir, _ string) (int, error) {
		if utils.CanonicalizePath(beadsDir) == wantTarget {
			return 5, nil
		}
		t.Errorf("preflight counted %s, not the .beads init replaces (%s)", beadsDir, wantTarget)
		// An error, not 0, so a wrong count still stops at the refusal
		// instead of re-initializing the test fixture.
		return 0, errors.New("counted the wrong workspace")
	}
	t.Cleanup(func() { countReinitTarget = origCount })

	var err error
	out := captureStderr(t, func() {
		err = initCmd.RunE(initCmd, nil)
	})
	requireExitCode(t, err, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Target: "+wantTarget) {
		t.Errorf("warning must name the main checkout's .beads %s, got:\n%s", wantTarget, out)
	}
	if !strings.Contains(out, "Existing issues: 5") {
		t.Errorf("warning must show the main checkout's issue count, got:\n%s", out)
	}
	if got, readErr := os.ReadFile(filepath.Join(mainBeads, "metadata.json")); readErr != nil || !bytes.Equal(got, metadata) {
		t.Errorf("the main checkout's .beads must survive the refusal: %v %q", readErr, got)
	}
}

// TestInitDatabaseName pins the one rule both the reinit preflight and init's
// storage step take the database name from.
func TestInitDatabaseName(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "from_the_environment")
	recorded := &configfile.Config{DoltDatabase: "kept"}
	for _, tc := range []struct {
		name     string
		existing *configfile.Config
		prefix   string
		database string
		want     string
	}{
		{name: "--database wins", existing: recorded, prefix: "my-proj", database: "explicit", want: "explicit"},
		{name: "metadata.json's dolt_database is kept", existing: recorded, prefix: "my-proj", want: "kept"},
		{name: "omitted dolt_database: the prefix's database", existing: &configfile.Config{}, prefix: "my-proj", want: "my_proj"},
		{name: "no metadata: the prefix's database", prefix: "my-proj", want: "my_proj"},
		{name: "no prefix: beads", want: "beads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := initDatabaseName(tc.existing, tc.prefix, tc.database); got != tc.want {
				t.Fatalf("initDatabaseName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInitReinitLocal_CountsTheDatabaseInitReplaces pins RunE's wiring: the
// preflight is handed the database init replaces, initDatabaseName's answer,
// not the one the workspace's store opens. With dolt_database omitted that
// store opens "beads" while init replaces the prefix's database.
func TestInitReinitLocal_CountsTheDatabaseInitReplaces(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata string
		flags    map[string]string
		want     string
	}{
		{name: "metadata.json omits dolt_database", metadata: `{"database":"dolt","backend":"dolt"}`, flags: map[string]string{"prefix": "my-proj"}, want: "my_proj"},
		{name: "metadata.json records dolt_database", metadata: `{"database":"dolt","backend":"dolt","dolt_database":"kept"}`, flags: map[string]string{"prefix": "other"}, want: "kept"},
		{name: "--database", metadata: `{"database":"dolt","backend":"dolt","dolt_database":"kept"}`, flags: map[string]string{"prefix": "other", "database": "explicit"}, want: "explicit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			gitInitAt(t, projectDir)
			beadsDir := filepath.Join(projectDir, ".beads")
			writeReinitTargetMetadata(t, beadsDir, []byte(tc.metadata))

			t.Chdir(projectDir)
			git.ResetCaches()
			beads.ResetCaches()
			t.Cleanup(func() {
				git.ResetCaches()
				beads.ResetCaches()
			})
			stubCallerBeadsDir(t, "")
			for _, name := range []string{"BEADS_DIR", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER"} {
				t.Setenv(name, "")
			}
			t.Setenv("BD_NON_INTERACTIVE", "1")
			// --database is the root command's persistent flag, which Cobra
			// merges into init's flags only when it runs a command.
			initCmd.InheritedFlags()
			setInitFlag(t, "reinit-local", "true")
			setInitFlag(t, "skip-hooks", "true")
			for name, value := range tc.flags {
				setInitFlag(t, name, value)
			}
			oldCtx := rootCtx
			rootCtx = context.Background()
			t.Cleanup(func() { rootCtx = oldCtx })

			var counted []string
			origCount := countReinitTarget
			countReinitTarget = func(_, dbName string) (int, error) {
				counted = append(counted, dbName)
				// Issues to destroy, so RunE stops at the refusal instead of
				// re-initializing the fixture.
				return 5, nil
			}
			t.Cleanup(func() { countReinitTarget = origCount })

			var err error
			out := captureStderr(t, func() {
				err = initCmd.RunE(initCmd, nil)
			})
			requireExitCode(t, err, ExitDestroyTokenMissing)
			if len(counted) != 1 || counted[0] != tc.want {
				t.Fatalf("preflight counted databases %q, want [%q], the one init replaces\n%s", counted, tc.want, out)
			}
		})
	}
}

// TestRunInitReinitPreflight_WorktreeCountsMainCheckout pins, without a Dolt
// server, that the preflight counts the directory it is given rather than the
// one discovery resolves: in a linked worktree whose own .beads holds an empty
// database, FindBeadsDir (what the old count resolved) picks the worktree's
// .beads, while GetWorktreeFallbackBeadsDir, the gate target RunE computes
// when BEADS_DIR is unset, is the main checkout's. Given that target, the
// preflight must refuse on the main checkout's uncountable database, name it,
// and leave it unchanged. It pins the function, not the CLI: there the root
// pre-run exports the discovered workspace as BEADS_DIR, so a worktree with
// its own database counts and replaces its own .beads.
//
// Deliberately not t.Parallel(): t.Chdir and a shared git worktree fixture.
func TestRunInitReinitPreflight_WorktreeCountsMainCheckout(t *testing.T) {
	isolateBeadsDirForTest(t)

	root := t.TempDir()
	mainRepo := filepath.Join(root, "main-repo")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitAt(t, mainRepo)
	worktreeDir := filepath.Join(root, "worktree")
	for _, args := range [][]string{
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init"},
		{"worktree", "add", worktreeDir},
	} {
		if out, err := initArtifactGitCommand(mainRepo, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// The main checkout's .beads: beads project files, uncountable.
	mainBeadsDir := filepath.Join(mainRepo, ".beads")
	writeReinitTargetMetadata(t, mainBeadsDir, []byte("{not json"))
	metadataPath := filepath.Join(mainBeadsDir, "metadata.json")
	beforeMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := countReinitTargetIssues(mainBeadsDir, "bd"); err == nil {
		t.Fatal("precondition: the main checkout's .beads must be uncountable")
	}
	// The worktree's own .beads: project files and an empty database of its
	// own, so discovery prefers it over the main checkout.
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	writeReinitTargetMetadata(t, worktreeBeadsDir, []byte(`{"backend": "dolt", "database": "dolt"}`))
	if err := os.MkdirAll(filepath.Join(worktreeBeadsDir, "dolt", "emptydb", ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()
	beads.ResetCaches()
	t.Cleanup(func() {
		git.ResetCaches()
		beads.ResetCaches()
	})
	if !git.IsWorktree() {
		t.Fatal("precondition: expected git.IsWorktree() inside the linked worktree")
	}
	if got, want := utils.CanonicalizePath(beads.FindBeadsDir()), utils.CanonicalizePath(worktreeBeadsDir); got != want {
		t.Fatalf("precondition: FindBeadsDir = %q, want the worktree's own %q", got, want)
	}
	gateTarget := beads.GetWorktreeFallbackBeadsDir()
	if got, want := utils.CanonicalizePath(gateTarget), utils.CanonicalizePath(mainBeadsDir); got != want {
		t.Fatalf("precondition: the gate target %q is not the main checkout's .beads %q", got, want)
	}

	var refusal error
	out := captureStderr(t, func() {
		refusal = runInitReinitPreflight(true, false, gateTarget, "bd", "bd", "")
	})
	requireExitCode(t, refusal, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Target: "+gateTarget) {
		t.Errorf("warning must name the main checkout's .beads as the target, got:\n%s", out)
	}
	if !strings.Contains(out, "Refusing to destroy an unknown number of issues") {
		t.Errorf("warning must report the unknown count, got:\n%s", out)
	}
	if got, err := os.ReadFile(metadataPath); err != nil || !bytes.Equal(got, beforeMetadata) {
		t.Errorf("the refusal must leave the main checkout's .beads unchanged, got %q, %v", got, err)
	}
	entries, err := os.ReadDir(mainBeadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "metadata.json" {
		t.Errorf("the refusal must create nothing in the main checkout's .beads, found %d entries", len(entries))
	}
}

// TestRunInitReinitPreflight_NothingToDestroyProceedsSilently pins the benign
// half: a target the count proves empty passes the preflight with no warning,
// no typed prompt and no token demand, so a fresh init asks nothing.
func TestRunInitReinitPreflight_NothingToDestroyProceedsSilently(t *testing.T) {
	isolateBeadsDirForTest(t)
	beadsDir := filepath.Join(t.TempDir(), ".beads") // never created

	var err error
	out := captureStderr(t, func() {
		err = runInitReinitPreflight(true, false, beadsDir, "bd", "bd", "")
	})
	if err != nil {
		t.Fatalf("runInitReinitPreflight = %v, want nil: a proven-empty target must proceed", err)
	}
	if out != "" {
		t.Errorf("a proven-empty target must produce no warning, prompt or token demand, got:\n%s", out)
	}
	if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
		t.Errorf("the preflight must not create its target, stat err %v", statErr)
	}
}

// TestRunInitReinitPreflight_UnnamedDatabaseNeedsTheToken pins the count to
// the database init replaces. With dolt_database omitted from metadata.json,
// the workspace's store opens "beads" while init replaces the database the
// issue prefix names, so neither an empty "beads" nor a server that does not
// list it, even under --recreate-missing, proves there is nothing to destroy:
// without a terminal the preflight still asks for the destroy token, and
// accepts it.
func TestRunInitReinitPreflight_UnnamedDatabaseNeedsTheToken(t *testing.T) {
	metadata := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","project_id":"existing-0000-1111-2222-333344445555"}`)
	for _, tc := range []struct {
		name    string
		store   storage.DoltStorage
		openErr error
	}{
		{name: "beads holds no issues", store: reinitStatsStore{stats: &types.Statistics{}}},
		{name: "the server does not list beads", openErr: fmt.Errorf("open: %w", dolt.ErrDatabaseNotFound)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_DATA_DIR"} {
				t.Setenv(name, "")
			}
			setRecreateMissing(t, true)
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			writeReinitTargetMetadata(t, beadsDir, metadata)
			orig := openReinitTarget
			openReinitTarget = func(context.Context, string) (storage.DoltStorage, error) { return tc.store, tc.openErr }
			t.Cleanup(func() { openReinitTarget = orig })

			dbName := reinitTargetDatabase(beadsDir, "myproj", "")
			if dbName != "myproj" {
				t.Fatalf("precondition: init replaces %q, want the prefix's myproj", dbName)
			}
			var err error
			out := captureStderr(t, func() {
				err = runInitReinitPreflight(true, false, beadsDir, dbName, "myproj", "")
			})
			requireExitCode(t, err, ExitDestroyTokenMissing)
			for _, want := range []string{"Target: " + beadsDir, "Existing issues: unknown", "Refusing to destroy an unknown number of issues"} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q, got:\n%s", want, out)
				}
			}

			out = captureStderr(t, func() {
				err = runInitReinitPreflight(true, false, beadsDir, dbName, "myproj", FormatDestroyToken("myproj"))
			})
			if err != nil {
				t.Fatalf("the destroy token must authorize the re-initialization, got %v\n%s", err, out)
			}
		})
	}
}

// TestRunInitReinitPreflight_NotFoundNeedsRecreateMissing pins, without a Dolt
// server, that the store's not-found spares the destroy confirmation only
// under --recreate-missing. The server the count's open dials need not be the
// one init's store dials (initStoreServerHost), so without the opt-in a
// server-mode database that server does not list is an unknown count: without
// a terminal the preflight asks for the destroy token, and accepts it. Under
// the opt-in it proceeds and prints nothing.
func TestRunInitReinitPreflight_NotFoundNeedsRecreateMissing(t *testing.T) {
	for _, name := range []string{"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_DATA_DIR"} {
		t.Setenv(name, "")
	}
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	writeReinitTargetMetadata(t, beadsDir, []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"myproject","project_id":"existing-0000-1111-2222-333344445555"}`))
	orig := openReinitTarget
	openReinitTarget = func(context.Context, string) (storage.DoltStorage, error) {
		return nil, fmt.Errorf("open: %w", dolt.ErrDatabaseNotFound)
	}
	t.Cleanup(func() { openReinitTarget = orig })

	t.Run("without --recreate-missing the destroy token is required", func(t *testing.T) {
		setRecreateMissing(t, false)
		var err error
		out := captureStderr(t, func() {
			err = runInitReinitPreflight(true, false, beadsDir, "myproject", "myproject", "")
		})
		requireExitCode(t, err, ExitDestroyTokenMissing)
		for _, want := range []string{"Target: " + beadsDir, "Existing issues: unknown", "Refusing to destroy an unknown number of issues"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q, got:\n%s", want, out)
			}
		}

		out = captureStderr(t, func() {
			err = runInitReinitPreflight(true, false, beadsDir, "myproject", "myproject", FormatDestroyToken("myproject"))
		})
		if err != nil {
			t.Fatalf("the destroy token must authorize the re-initialization, got %v\n%s", err, out)
		}
	})

	t.Run("under --recreate-missing it proceeds silently", func(t *testing.T) {
		setRecreateMissing(t, true)
		var err error
		out := captureStderr(t, func() {
			err = runInitReinitPreflight(true, false, beadsDir, "myproject", "myproject", "")
		})
		if err != nil {
			t.Fatalf("runInitReinitPreflight = %v, want nil under --recreate-missing\n%s", err, out)
		}
		if out != "" {
			t.Errorf("want no warning, prompt or token demand, got:\n%s", out)
		}
	})
}

// TestRunInitReinitPreflight_NonInteractiveAsksForTheToken pins that a
// non-interactive init (isNonInteractiveInit: --non-interactive,
// BD_NON_INTERACTIVE or CI) gets the destroy-token refusal even on a terminal,
// so an agent on a pseudo-terminal exits 12 instead of waiting for a phrase.
// Stdin holds the phrase that would confirm, so a prompt that read it would
// proceed. The interactive half shows that the stand-in terminal reaches the
// prompt.
func TestRunInitReinitPreflight_NonInteractiveAsksForTheToken(t *testing.T) {
	origTerminal := reinitStdinIsTerminal
	reinitStdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { reinitStdinIsTerminal = origTerminal })

	const target = "/work/project/.beads"
	for _, tc := range []struct {
		name     string
		count    int
		countErr error
		phrase   string
	}{
		{name: "unknown count", countErr: errors.New("count timed out"), phrase: "destroy all issues"},
		{name: "known count", count: 3, phrase: "destroy 3 issues"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origCount := countReinitTarget
			countReinitTarget = func(string, string) (int, error) { return tc.count, tc.countErr }
			t.Cleanup(func() { countReinitTarget = origCount })

			var err error
			var out string
			withTestStdin(t, tc.phrase+"\n", func() {
				out = captureStderr(t, func() {
					err = runInitReinitPreflight(true, true, target, "bd", "bd", "")
				})
			})
			requireExitCode(t, err, ExitDestroyTokenMissing)
			if strings.Contains(out, "Type '") {
				t.Errorf("a non-interactive init must not prompt, got:\n%s", out)
			}

			withTestStdin(t, tc.phrase+"\n", func() {
				out = captureStderr(t, func() {
					err = runInitReinitPreflight(true, false, target, "bd", "bd", "")
				})
			})
			if err != nil {
				t.Fatalf("on a terminal the typed phrase must confirm, got %v\n%s", err, out)
			}
			if !strings.Contains(out, "Type '"+tc.phrase+"' to confirm") {
				t.Errorf("want the typed prompt on a terminal, got:\n%s", out)
			}
		})
	}
}

// TestInitReinitLocal_NonInteractiveOnATerminalAsksForTheToken pins RunE's
// wiring: the preflight is handed init's non-interactive mode, so
// --non-interactive or BD_NON_INTERACTIVE=1 on a terminal gets the
// destroy-token refusal (exit 12) rather than the typed prompt, whose declined
// answer would exit 11.
func TestInitReinitLocal_NonInteractiveOnATerminalAsksForTheToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag bool   // --non-interactive
		env  string // BD_NON_INTERACTIVE
	}{
		{name: "--non-interactive", flag: true},
		{name: "BD_NON_INTERACTIVE=1", env: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			gitInitAt(t, projectDir)
			writeReinitTargetMetadata(t, filepath.Join(projectDir, ".beads"), []byte(`{"database":"dolt","backend":"dolt","dolt_database":"kept"}`))

			t.Chdir(projectDir)
			git.ResetCaches()
			beads.ResetCaches()
			t.Cleanup(func() {
				git.ResetCaches()
				beads.ResetCaches()
			})
			stubCallerBeadsDir(t, "")
			for _, name := range []string{"BEADS_DIR", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER"} {
				t.Setenv(name, "")
			}
			t.Setenv("BD_NON_INTERACTIVE", tc.env)
			setInitFlag(t, "reinit-local", "true")
			setInitFlag(t, "skip-hooks", "true")
			setInitFlag(t, "prefix", "kept")
			if tc.flag {
				setInitFlag(t, "non-interactive", "true")
			}
			oldCtx := rootCtx
			rootCtx = context.Background()
			t.Cleanup(func() { rootCtx = oldCtx })

			origTerminal := reinitStdinIsTerminal
			reinitStdinIsTerminal = func() bool { return true }
			t.Cleanup(func() { reinitStdinIsTerminal = origTerminal })
			origCount := countReinitTarget
			countReinitTarget = func(string, string) (int, error) { return 5, nil }
			t.Cleanup(func() { countReinitTarget = origCount })

			var err error
			var out string
			withTestStdin(t, "no\n", func() {
				out = captureStderr(t, func() {
					err = initCmd.RunE(initCmd, nil)
				})
			})
			requireExitCode(t, err, ExitDestroyTokenMissing)
			if strings.Contains(out, "Type '") {
				t.Errorf("a non-interactive init must not prompt, got:\n%s", out)
			}
		})
	}
}
