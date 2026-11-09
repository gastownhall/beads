//go:build cgo

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestCloseIssueSetsClosedAt verifies the dedicated close operation owns its lifecycle metadata.
func TestCloseIssueSetsClosedAt(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := context.Background()

	// Step 1: Create an open issue in the database
	openIssue := &types.Issue{
		ID:          "bd-transition-1",
		Title:       "Test transition",
		Description: "This will be closed",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		ClosedAt:    nil,
	}

	if err := testStore.CreateIssue(ctx, openIssue, "test"); err != nil {
		t.Fatalf("Failed to create open issue: %v", err)
	}

	// Step 2: Lifecycle transitions use the dedicated close operation.
	if err := testStore.CloseIssue(ctx, "bd-transition-1", "", "test", ""); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	// Step 3: Verify the issue is now closed with correct closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-1")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusClosed {
		t.Errorf("Expected status to be closed, got %s", updated.Status)
	}

	if updated.ClosedAt == nil {
		t.Fatal("Expected closed_at to be set after transition to closed")
	}
}

// TestReopenIssueClearsClosedAt verifies the dedicated reopen operation clears lifecycle metadata.
func TestReopenIssueClearsClosedAt(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := context.Background()

	// Step 1: Create a closed issue in the database
	closedTime := time.Now()
	closedIssue := &types.Issue{
		ID:          "bd-transition-2",
		Title:       "Test reopening",
		Description: "This will be reopened",
		Status:      types.StatusClosed,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   closedTime,
		ClosedAt:    &closedTime,
	}

	if err := testStore.CreateIssue(ctx, closedIssue, "test"); err != nil {
		t.Fatalf("Failed to create closed issue: %v", err)
	}

	// Step 2: Lifecycle transitions use the dedicated reopen operation.
	if err := testStore.ReopenIssue(ctx, "bd-transition-2", "", "test"); err != nil {
		t.Fatalf("ReopenIssue failed: %v", err)
	}

	// Step 3: Verify the issue is now open with null closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-2")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusOpen {
		t.Errorf("Expected status to be open, got %s", updated.Status)
	}

	if updated.ClosedAt != nil {
		t.Errorf("Expected closed_at to be nil after reopening, got %v", updated.ClosedAt)
	}
}

// TestBlockedEnvVars tests that BD_BACKEND and BD_DATABASE_BACKEND are blocked (bd-hevyw).
func TestBlockedEnvVars(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		value  string
	}{
		{"BD_BACKEND blocked", "BD_BACKEND", "sqlite"},
		{"BD_DATABASE_BACKEND blocked", "BD_DATABASE_BACKEND", "sqlite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.value)
			err := checkBlockedEnvVars()
			if err == nil {
				t.Errorf("expected error when %s is set, got nil", tt.envVar)
			}
			if err != nil && !strings.Contains(err.Error(), tt.envVar) {
				t.Errorf("expected error to mention %s, got: %v", tt.envVar, err)
			}
			if err != nil && !strings.Contains(err.Error(), "bd help init-safety") {
				t.Errorf("expected error to point to safe reinitialization guidance, got: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "bd migrate dolt") {
				t.Errorf("error must not recommend the removed backend-conversion command: %v", err)
			}
		})
	}

	// Verify no error when env vars are unset
	t.Run("no env vars set", func(t *testing.T) {
		t.Setenv("BD_BACKEND", "")
		t.Setenv("BD_DATABASE_BACKEND", "")
		// Unset them (t.Setenv("", "") sets to empty which Getenv returns as "")
		os.Unsetenv("BD_BACKEND")
		os.Unsetenv("BD_DATABASE_BACKEND")
		err := checkBlockedEnvVars()
		if err != nil {
			t.Errorf("expected no error when env vars are unset, got: %v", err)
		}
	})
}

func TestListUsesRepoBeadsDirWhenDoltDataDirEscapesDotBeads(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	initConfigForTest(t)
	ensureCleanGlobalState(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	relativeDoltDir := "../external-dolt"
	externalDoltDir := filepath.Join(beadsDir, relativeDoltDir)
	if err := os.MkdirAll(filepath.Dir(externalDoltDir), 0o755); err != nil {
		t.Fatalf("mkdir external dolt parent: %v", err)
	}

	database := uniqueTestDBName(t)
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
		DoltDataDir:    relativeDoltDir,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(testDoltServerPort)), 0o600); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	ctx := context.Background()
	testStore, err := dolt.New(ctx, &dolt.Config{
		Path:            externalDoltDir,
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	defer func() {
		_ = testStore.Close()
		dropTestDatabase(database, testDoltServerPort)
	}()

	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := testStore.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		t.Fatalf("set types.custom: %v", err)
	}

	now := time.Now()
	issue := &types.Issue{
		ID:          "test-port-proof-1",
		Title:       "Port-proof issue",
		Description: "Verifies bd list uses the repo's .beads config even with external dolt data",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DB", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	// Shared once-per-process binary (honors BEADS_TEST_BD_BINARY) instead of
	// a per-test go build — the in-test link steps dominated cmd/bd's wall
	// clock (wy-4mtr0).
	binPath := buildBDForInitTests(t)

	listCmd := exec.Command(binPath, "list", "--json")
	listCmd.Dir = repoDir
	listCmd.Env = append(os.Environ(),
		"BEADS_TEST_MODE=1",
		"BEADS_DIR="+beadsDir,
		"BEADS_DB=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
	)
	output, err := listCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd list failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Port-proof issue") {
		t.Fatalf("expected list output to include created issue\n%s", output)
	}
}

// bd-6dnrw.5: shared-server mode overriding a pinned dolt_mode=embedded must
// warn and win for the session, but must never rewrite the committed
// metadata.json (per-machine env must not leak into shared config).
func TestSharedServerEmbeddedMismatchDoesNotRewriteMetadata(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeEmbedded
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	before, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")

	oldServerMode, oldWarned := serverMode, sharedServerEmbeddedMismatchWarned
	defer func() {
		serverMode = oldServerMode
		sharedServerEmbeddedMismatchWarned = oldWarned
	}()
	sharedServerEmbeddedMismatchWarned = false

	captureStderr := func(fn func()) string {
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("pipe: %v", pipeErr)
		}
		oldStderr := os.Stderr
		os.Stderr = w
		defer func() { os.Stderr = oldStderr }()
		fn()
		_ = w.Close()
		out, readErr := io.ReadAll(r)
		if readErr != nil {
			t.Fatalf("read stderr: %v", readErr)
		}
		return string(out)
	}

	stderr := captureStderr(func() { loadServerModeFromBeadsDir(beadsDir) })

	if !serverMode {
		t.Error("expected shared-server env to win for the session (serverMode=true)")
	}
	if !strings.Contains(stderr, "dolt_mode=\"embedded\"") {
		t.Errorf("expected mismatch notice on stderr, got: %q", stderr)
	}
	after, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	if err != nil {
		t.Fatalf("re-read metadata.json: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("metadata.json was rewritten on disk:\nbefore: %s\nafter: %s", before, after)
	}

	// The notice is once-per-process: a second load must stay quiet.
	stderr = captureStderr(func() { loadServerModeFromBeadsDir(beadsDir) })
	if strings.Contains(stderr, "dolt_mode") {
		t.Errorf("expected no repeat notice, got: %q", stderr)
	}
}

// modeMismatchWorkspace builds an isolated .beads directory for the
// embedded/server mismatch notice tests: metadata.json holds metadataJSON
// verbatim, configYAML (when non-empty) becomes .beads/config.yaml, and
// withEmbeddedDir creates .beads/embeddeddolt. Every env var the mode
// resolvers read is cleared, HOME/XDG point at an empty temp dir, and config
// is re-initialized from the workspace so no host or user setting leaks in.
func modeMismatchWorkspace(t *testing.T, metadataJSON, configYAML string, withEmbeddedDir bool) string {
	t.Helper()
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(configfile.ConfigPath(beadsDir), []byte(metadataJSON), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	if configYAML != "" {
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
	}
	if withEmbeddedDir {
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
			t.Fatalf("mkdir embeddeddolt: %v", err)
		}
	}

	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DIR", beadsDir)
	for _, key := range []string{
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT",
		"BEADS_DOLT_PORT",
	} {
		t.Setenv(key, "")
	}
	t.Chdir(root)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	oldServerMode, oldProxied, oldWarned := serverMode, proxiedServerMode, sharedServerEmbeddedMismatchWarned
	t.Cleanup(func() {
		serverMode = oldServerMode
		proxiedServerMode = oldProxied
		sharedServerEmbeddedMismatchWarned = oldWarned
	})
	sharedServerEmbeddedMismatchWarned = false
	return beadsDir
}

// loadModeCapturingOutput runs loadServerModeFromBeadsDir with stdout and
// stderr captured separately.
func loadModeCapturingOutput(t *testing.T, beadsDir string) (stdout, stderr string, err error) {
	t.Helper()
	outR, outW, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	errR, errW, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	err = loadServerModeFromBeadsDir(beadsDir)
	os.Stdout, os.Stderr = oldStdout, oldStderr
	_ = outW.Close()
	_ = errW.Close()
	outBytes, readErr := io.ReadAll(outR)
	if readErr != nil {
		t.Fatalf("read stdout: %v", readErr)
	}
	errBytes, readErr := io.ReadAll(errR)
	if readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	return string(outBytes), string(errBytes), err
}

// GH#4746: a runtime server override (BEADS_DOLT_SERVER_MODE=1 or a non-local
// BEADS_DOLT_SERVER_HOST) silently bypassed a workspace whose metadata.json
// pins dolt_mode=embedded, so concurrent sessions with different env could
// read different stores with no hint. The override must keep winning, but
// say so once per process on stderr, naming the variable, the embedded
// setting and the bypassed embedded directory, without touching metadata.
func TestServerModeOverrideEmbeddedMismatchNotice(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		env      map[string]string
		wantVar  string
		otherVar string
	}{
		{
			name:     "server mode env",
			metadata: `{"backend":"dolt","dolt_mode":"embedded"}`,
			env:      map[string]string{"BEADS_DOLT_SERVER_MODE": "1"},
			wantVar:  "BEADS_DOLT_SERVER_MODE",
			otherVar: "BEADS_DOLT_SERVER_HOST",
		},
		{
			name:     "non-local host env",
			metadata: `{"backend":"dolt","dolt_mode":"embedded"}`,
			env:      map[string]string{"BEADS_DOLT_SERVER_HOST": "db.example.com"},
			wantVar:  "BEADS_DOLT_SERVER_HOST",
			otherVar: "BEADS_DOLT_SERVER_MODE",
		},
		{
			name:     "non-local loopback host env",
			metadata: `{"backend":"dolt","dolt_mode":"embedded"}`,
			env:      map[string]string{"BEADS_DOLT_SERVER_HOST": "127.0.0.2"},
			wantVar:  "BEADS_DOLT_SERVER_HOST",
			otherVar: "BEADS_DOLT_SERVER_MODE",
		},
		{
			name:     "embedded spelled with case and whitespace",
			metadata: `{"backend":"dolt","dolt_mode":" Embedded "}`,
			env:      map[string]string{"BEADS_DOLT_SERVER_MODE": "1"},
			wantVar:  "BEADS_DOLT_SERVER_MODE",
			otherVar: "BEADS_DOLT_SERVER_HOST",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := modeMismatchWorkspace(t, tt.metadata, "", true)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			before, err := os.ReadFile(configfile.ConfigPath(beadsDir))
			if err != nil {
				t.Fatalf("read metadata.json: %v", err)
			}

			stdout, stderr, err := loadModeCapturingOutput(t, beadsDir)
			if err != nil {
				t.Fatalf("loadServerModeFromBeadsDir: %v", err)
			}
			if !serverMode {
				t.Error("expected the runtime override to keep selecting server mode (serverMode=true)")
			}
			if stdout != "" {
				t.Errorf("stdout must stay untouched, got: %q", stdout)
			}
			embeddedDir := filepath.Join(beadsDir, "embeddeddolt")
			for _, want := range []string{tt.wantVar, `dolt_mode="embedded"`, embeddedDir} {
				if !strings.Contains(stderr, want) {
					t.Errorf("notice must name %q, got stderr: %q", want, stderr)
				}
			}
			if strings.Contains(stderr, tt.otherVar) {
				t.Errorf("notice must name only the deciding variable, found %q in: %q", tt.otherVar, stderr)
			}
			if strings.Contains(stderr, "shared-server mode is enabled") {
				t.Errorf("shared-server notice must not fire without shared-server, got: %q", stderr)
			}

			after, err := os.ReadFile(configfile.ConfigPath(beadsDir))
			if err != nil {
				t.Fatalf("re-read metadata.json: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("metadata.json was rewritten:\nbefore: %s\nafter: %s", before, after)
			}

			// Once per process: a second load stays quiet and keeps server mode.
			stdout, stderr, err = loadModeCapturingOutput(t, beadsDir)
			if err != nil {
				t.Fatalf("second loadServerModeFromBeadsDir: %v", err)
			}
			if stderr != "" || stdout != "" {
				t.Errorf("expected a silent second load, got stdout %q stderr %q", stdout, stderr)
			}
			if !serverMode {
				t.Error("second load must still select server mode")
			}
		})
	}
}

// GH#4746: the runtime-override notice must stay inside its stated
// conditions. The shared-server notice keeps priority and its remedies,
// configurations that do not pit a runtime override against an explicit
// embedded metadata setting stay silent, and every case keeps the resolver's
// existing mode selection, an empty stdout and a nil error.
func TestServerModeOverrideEmbeddedMismatchNoticeScope(t *testing.T) {
	const (
		embedded      = `{"backend":"dolt","dolt_mode":"embedded"}`
		sharedNotice  = "shared-server mode is enabled"
		modeEnvNotice = "BEADS_DOLT_SERVER_MODE=1 selects server mode"
		hostNotice    = "BEADS_DOLT_SERVER_HOST names a non-local host"
	)
	tests := []struct {
		name         string
		metadata     string
		configYAML   string
		embeddedDir  bool
		env          map[string]string
		wantServer   bool
		wantProxied  bool
		wantNotice   string // "" means stderr must be empty
		wantRemedies []string
	}{
		{
			name:         "shared-server env outranks both overrides",
			metadata:     embedded,
			embeddedDir:  true,
			env:          map[string]string{"BEADS_DOLT_SHARED_SERVER": "1", "BEADS_DOLT_SERVER_MODE": "1", "BEADS_DOLT_SERVER_HOST": "db.example.com"},
			wantServer:   true,
			wantNotice:   sharedNotice,
			wantRemedies: []string{"To persist server mode", "unset BEADS_DOLT_SHARED_SERVER (or remove dolt.shared-server from config.yaml)"},
		},
		{
			name:         "shared-server from config.yaml without an embedded dir",
			metadata:     embedded,
			configYAML:   "dolt.shared-server: true\n",
			env:          map[string]string{"BEADS_DOLT_SERVER_MODE": "1"},
			wantServer:   true,
			wantNotice:   sharedNotice,
			wantRemedies: []string{"To persist server mode", "unset BEADS_DOLT_SHARED_SERVER (or remove dolt.shared-server from config.yaml)"},
		},
		{
			name:         "shared-server from nested config.yaml without an embedded dir",
			metadata:     embedded,
			configYAML:   "dolt:\n  shared-server: true\n",
			wantServer:   true,
			wantNotice:   sharedNotice,
			wantRemedies: []string{"To persist server mode", "unset BEADS_DOLT_SHARED_SERVER (or remove dolt.shared-server from config.yaml)"},
		},
		{
			name:         "server mode env outranks host env",
			metadata:     embedded,
			embeddedDir:  true,
			env:          map[string]string{"BEADS_DOLT_SERVER_MODE": "1", "BEADS_DOLT_SERVER_HOST": "db.example.com"},
			wantServer:   true,
			wantNotice:   modeEnvNotice,
			wantRemedies: []string{"To persist server mode", "unset BEADS_DOLT_SERVER_MODE."},
		},
		{
			name:       "explicit server metadata",
			metadata:   `{"backend":"dolt","dolt_mode":"server"}`,
			env:        map[string]string{"BEADS_DOLT_SERVER_MODE": "1"},
			wantServer: true,
		},
		{
			name:        "proxied-server metadata",
			metadata:    `{"backend":"dolt","dolt_mode":"proxied-server"}`,
			env:         map[string]string{"BEADS_DOLT_SERVER_MODE": "1", "BEADS_DOLT_SERVER_HOST": "db.example.com"},
			wantServer:  true,
			wantProxied: true,
		},
		{
			name:        "proxied-server metadata with only host env",
			metadata:    `{"backend":"dolt","dolt_mode":"proxied-server"}`,
			env:         map[string]string{"BEADS_DOLT_SERVER_HOST": "db.example.com"},
			wantProxied: true,
		},
		{
			name:        "non-Dolt backend",
			metadata:    `{"backend":"sqlite","dolt_mode":"embedded"}`,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_MODE": "1", "BEADS_DOLT_SERVER_HOST": "db.example.com"},
		},
		{
			name:        "implicit embedded mode with server mode env",
			metadata:    `{"backend":"dolt"}`,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_MODE": "1"},
			wantServer:  true,
		},
		{
			name:        "implicit embedded mode with host env",
			metadata:    `{"backend":"dolt"}`,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_HOST": "db.example.com"},
			wantServer:  true,
		},
		{
			name:        "localhost host env",
			metadata:    embedded,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_HOST": "localhost"},
		},
		{
			name:        "loopback host env",
			metadata:    embedded,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_HOST": "127.0.0.1"},
		},
		{
			name:        "empty host env",
			metadata:    embedded,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_HOST": ""},
		},
		{
			name:        "bare server port env",
			metadata:    embedded,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_PORT": "3307"},
		},
		{
			name:        "server mode env true is not 1",
			metadata:    embedded,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_MODE": "true"},
		},
		{
			name:        "server mode env 0",
			metadata:    embedded,
			embeddedDir: true,
			env:         map[string]string{"BEADS_DOLT_SERVER_MODE": "0"},
		},
		{
			name:        "persisted non-local host under explicit embedded",
			metadata:    `{"backend":"dolt","dolt_mode":"embedded","dolt_server_host":"db.example.com"}`,
			embeddedDir: true,
		},
		{
			name:       "persisted non-local host without a mode",
			metadata:   `{"backend":"dolt","dolt_server_host":"db.example.com"}`,
			wantServer: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := modeMismatchWorkspace(t, tt.metadata, tt.configYAML, tt.embeddedDir)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			before, err := os.ReadFile(configfile.ConfigPath(beadsDir))
			if err != nil {
				t.Fatalf("read metadata.json: %v", err)
			}

			stdout, stderr, err := loadModeCapturingOutput(t, beadsDir)
			if err != nil {
				t.Fatalf("loadServerModeFromBeadsDir: %v", err)
			}
			if serverMode != tt.wantServer || proxiedServerMode != tt.wantProxied {
				t.Errorf("mode selection changed: serverMode=%v proxiedServerMode=%v, want %v/%v",
					serverMode, proxiedServerMode, tt.wantServer, tt.wantProxied)
			}
			if stdout != "" {
				t.Errorf("stdout must stay untouched, got: %q", stdout)
			}
			if tt.wantNotice == "" {
				if stderr != "" {
					t.Errorf("expected no notice, got stderr: %q", stderr)
				}
			} else {
				if strings.Count(stderr, "Notice:") != 1 || !strings.Contains(stderr, tt.wantNotice) {
					t.Errorf("expected exactly one %q notice, got stderr: %q", tt.wantNotice, stderr)
				}
				for _, remedy := range tt.wantRemedies {
					if !strings.Contains(stderr, remedy) {
						t.Errorf("expected remedy %q, got stderr: %q", remedy, stderr)
					}
				}
			}
			after, err := os.ReadFile(configfile.ConfigPath(beadsDir))
			if err != nil {
				t.Fatalf("re-read metadata.json: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("metadata.json was rewritten:\nbefore: %s\nafter: %s", before, after)
			}

			// Once per process: a repeated load of a warning case stays
			// quiet and keeps the same mode selection.
			if tt.wantNotice != "" {
				stdout, stderr, err = loadModeCapturingOutput(t, beadsDir)
				if err != nil {
					t.Fatalf("second loadServerModeFromBeadsDir: %v", err)
				}
				if stdout != "" || stderr != "" {
					t.Errorf("expected a silent second load, got stdout %q stderr %q", stdout, stderr)
				}
				if serverMode != tt.wantServer || proxiedServerMode != tt.wantProxied {
					t.Errorf("second load changed mode selection: serverMode=%v proxiedServerMode=%v, want %v/%v",
						serverMode, proxiedServerMode, tt.wantServer, tt.wantProxied)
				}
			}
		})
	}

	// The once-per-process guard is set only by a printed notice: a quiet
	// load must not consume it, so a later conflicting load still warns.
	t.Run("quiet load then conflicting load", func(t *testing.T) {
		beadsDir := modeMismatchWorkspace(t, embedded, "", true)
		t.Setenv("BEADS_DOLT_SERVER_MODE", "true")
		_, stderr, err := loadModeCapturingOutput(t, beadsDir)
		if err != nil {
			t.Fatalf("quiet load: %v", err)
		}
		if stderr != "" || serverMode {
			t.Fatalf("expected a quiet embedded load, got serverMode=%v stderr %q", serverMode, stderr)
		}
		t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		_, stderr, err = loadModeCapturingOutput(t, beadsDir)
		if err != nil {
			t.Fatalf("conflicting load: %v", err)
		}
		if !serverMode || !strings.Contains(stderr, modeEnvNotice) {
			t.Errorf("expected server mode and the override notice, got serverMode=%v stderr %q", serverMode, stderr)
		}
		_, stderr, err = loadModeCapturingOutput(t, beadsDir)
		if err != nil {
			t.Fatalf("repeated conflicting load: %v", err)
		}
		if stderr != "" {
			t.Errorf("expected no repeat notice after the guard is set, got: %q", stderr)
		}
	})
}
