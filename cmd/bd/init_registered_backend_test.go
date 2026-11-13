package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// TestInitRejectsRegisteredBackend verifies bd init fails closed for a
// registered extension backend instead of silently provisioning Dolt and
// persisting backend: dolt. Registered backends can only open an existing
// workspace; provisioning is the downstream registrant's own contract.
func TestInitRejectsRegisteredBackend(t *testing.T) {
	const name = "registry-init"
	registerContractBackend(t, name)

	// bd init runs on the shared cobra command and mutates package globals;
	// snapshot and restore both so the registered backend and its flag value
	// cannot leak into other init tests that share this state.
	origDBPath := dbPath
	origStore := store
	t.Cleanup(func() {
		if store != nil && store != origStore {
			store.Close()
		}
		store = origStore
		dbPath = origDBPath
		_ = initCmd.Flags().Set("backend", "")
		_ = initCmd.Flags().Set("prefix", "")
		_ = initCmd.Flags().Set("quiet", "false")
	})
	dbPath = ""
	store = nil

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	rootCmd.SetArgs([]string{"init", "--backend", name, "--prefix", "test", "--quiet"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("bd init accepted a registered backend; want fail-closed rejection")
	}
	if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "open an existing workspace") {
		t.Fatalf("init reject error = %v, want open-existing-workspace-only guidance naming %q", err, name)
	}
	// Failing closed means no workspace is written for the unprovisionable backend.
	if _, statErr := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected init created a .beads workspace (stat error: %v)", statErr)
	}
}

// createFixtureStore is the store a creatable fixture backend opens: a nil
// DoltStorage for identity, with the config and metadata writes bd init makes
// on a fresh workspace, and the failures a test arms on its backend.
type createFixtureStore struct {
	storage.DoltStorage
	backend  *creatableFixture
	config   map[string]string
	metadata map[string]string
	closed   int
}

func (s *createFixtureStore) SetConfig(_ context.Context, key, value string) error {
	if s.backend.setConfigErr != nil {
		return s.backend.setConfigErr
	}
	s.config[key] = value
	return nil
}

func (s *createFixtureStore) SetMetadata(_ context.Context, key, value string) error {
	if s.backend.setMetadataErr != nil {
		return s.backend.setMetadataErr
	}
	s.metadata[key] = value
	return nil
}

func (s *createFixtureStore) Close() error {
	s.closed++
	return s.backend.closeErr
}

// creatableFixture is a registered backend with a Create hook: it records the
// directories Create was handed and the stores Open returned, and fails
// whichever step a test arms.
type creatableFixture struct {
	name    string
	created []string
	opened  []*createFixtureStore

	createErr      error
	openErr        error
	setConfigErr   error
	setMetadataErr error
	closeErr       error
}

func registerCreatableFixture(t *testing.T, name string) *creatableFixture {
	t.Helper()
	fixture := &creatableFixture{name: name}
	backends.Register(name, backends.Backend{
		Open: func(context.Context, string) (storage.DoltStorage, error) {
			if fixture.openErr != nil {
				return nil, fixture.openErr
			}
			s := &createFixtureStore{backend: fixture, config: map[string]string{}, metadata: map[string]string{}}
			fixture.opened = append(fixture.opened, s)
			return s, nil
		},
		OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errRegistryReadOnly
		},
		Create: func(_ context.Context, beadsDir string) error {
			fixture.created = append(fixture.created, beadsDir)
			if fixture.createErr != nil {
				return fixture.createErr
			}
			return os.MkdirAll(filepath.Join(beadsDir, "fixture-store"), 0o700)
		},
		WorkspaceIsBeadsDir: true,
	})
	t.Cleanup(func() { backends.Deregister(name) })
	return fixture
}

// registeredInitWorkdir prepares an in-process bd init: package globals and
// the command tree's flags are put back afterwards, and the process sits in a
// fresh directory whose .beads the init will create. It returns that
// directory.
func registeredInitWorkdir(t *testing.T) string {
	t.Helper()
	origDBPath := dbPath
	origStore := store
	resetCommandFlags(rootCmd)
	t.Cleanup(func() {
		if store != nil && store != origStore {
			store.Close()
		}
		store = origStore
		dbPath = origDBPath
		resetCommandFlags(rootCmd)
	})
	dbPath = ""
	store = nil
	isolateBeadsDirForTest(t)

	// Resolved, so the directory compares equal to the one bd init derives
	// from the working directory (t.TempDir is behind a symlink on macOS).
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temp dir: %v", err)
	}
	t.Chdir(tmpDir)
	return tmpDir
}

// runRegisteredInit executes bd init with args through the root command. The
// flags are reset first: cobra keeps a parsed flag across Execute calls, and
// a second init in one test must not inherit the first one's.
func runRegisteredInit(args ...string) error {
	resetCommandFlags(rootCmd)
	rootCmd.SetArgs(append([]string{"init"}, args...))
	return rootCmd.Execute()
}

func initPathExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return err == nil
}

// TestInitProvisionsRegisteredBackendWithCreate is the other half of the
// fail-closed rule above: a registered backend that supplies the Create hook
// IS provisioned by bd init, through the hook and nothing else. The test
// registers a backend whose Create records the directory it was handed and
// whose Open returns an in-memory stand-in, runs bd init on it, and checks
// what a later command would open: the standard .beads/.gitignore,
// metadata.json naming the backend with a fresh project id, the issue prefix
// and the identity written through the store, and the store closed. A second
// init is refused, and --init-if-missing returns without creating again.
func TestInitProvisionsRegisteredBackendWithCreate(t *testing.T) {
	const name = "registry-init-create"
	fixture := registerCreatableFixture(t, name)
	tmpDir := registeredInitWorkdir(t)
	beadsDir := filepath.Join(tmpDir, ".beads")

	// The three flags the route honors by construction ride along: it never
	// prompts and installs neither hooks nor agent files.
	if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("bd init --backend=%s: %v", name, err)
	}

	if len(fixture.created) != 1 || fixture.created[0] != beadsDir {
		t.Fatalf("Create hook calls = %q, want exactly [%q]", fixture.created, beadsDir)
	}
	if info, err := os.Stat(filepath.Join(beadsDir, "fixture-store")); err != nil || !info.IsDir() {
		t.Fatalf("the hook's store directory: %v", err)
	}
	if ignore, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore")); err != nil || !strings.Contains(string(ignore), ".beads-credential-key") {
		t.Fatalf("the standard .beads/.gitignore: err=%v", err)
	}
	// Every later command takes the workspace gate, whose file sits beside
	// .beads: the project .gitignore has to cover it.
	if ignore, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore")); err != nil || !strings.Contains(string(ignore), "*.gate.lock*") {
		t.Fatalf("the project .gitignore: %q, err=%v", ignore, err)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json: cfg=%v err=%v", cfg, err)
	}
	if cfg.GetBackend() != name {
		t.Fatalf("metadata.json backend = %q, want %q", cfg.GetBackend(), name)
	}
	if cfg.ProjectID == "" {
		t.Fatal("metadata.json has no project_id")
	}
	if len(fixture.opened) != 1 {
		t.Fatalf("init opened %d stores, want 1", len(fixture.opened))
	}
	s := fixture.opened[0]
	if got := s.config["issue_prefix"]; got != "test" {
		t.Errorf("issue_prefix written to the store = %q, want %q", got, "test")
	}
	if got := s.metadata["_project_id"]; got != cfg.ProjectID {
		t.Errorf("_project_id written to the store = %q, want %q", got, cfg.ProjectID)
	}
	if s.closed != 1 {
		t.Errorf("init closed the store %d times, want 1", s.closed)
	}

	if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet"); err == nil {
		t.Fatal("a second bd init on the initialized workspace succeeded; want a refusal")
	}
	if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet", "--init-if-missing"); err != nil {
		t.Fatalf("bd init --init-if-missing on the initialized workspace: %v", err)
	}
	if len(fixture.created) != 1 {
		t.Fatalf("Create hook calls after the repeat inits = %d, want 1", len(fixture.created))
	}
}

// TestInitRegisteredBackendFailureLeavesNoWorkspace arms each step of the
// init in turn and holds the route to its commit point: metadata.json is
// written last, so an init that failed anywhere before it leaves nothing the
// next init would mistake for a workspace. Each case checks the three things
// a half-built workspace got wrong: the .beads directory this init created is
// gone, --init-if-missing does not report success while the fault stands, and
// a plain init succeeds once it clears.
func TestInitRegisteredBackendFailureLeavesNoWorkspace(t *testing.T) {
	errFault := errors.New("fixture fault")
	cases := []struct {
		name string
		arm  func(*creatableFixture, error)
	}{
		{"create fails", func(f *creatableFixture, err error) { f.createErr = err }},
		{"open fails", func(f *creatableFixture, err error) { f.openErr = err }},
		{"the prefix write fails", func(f *creatableFixture, err error) { f.setConfigErr = err }},
		{"the identity write fails", func(f *creatableFixture, err error) { f.setMetadataErr = err }},
		{"close fails", func(f *creatableFixture, err error) { f.closeErr = err }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "registry-init-fault-" + string(rune('a'+i))
			fixture := registerCreatableFixture(t, name)
			tmpDir := registeredInitWorkdir(t)
			beadsDir := filepath.Join(tmpDir, ".beads")

			tc.arm(fixture, errFault)
			if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet"); !errors.Is(err, errFault) {
				t.Fatalf("bd init with the fault armed: err = %v, want it to carry %v", err, errFault)
			}
			if initPathExists(t, beadsDir) {
				t.Fatalf("the failed init left %s behind", beadsDir)
			}
			if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet", "--init-if-missing"); !errors.Is(err, errFault) {
				t.Fatalf("bd init --init-if-missing after the failed init: err = %v, want it to try again and carry %v", err, errFault)
			}
			if initPathExists(t, beadsDir) {
				t.Fatalf("the failed --init-if-missing left %s behind", beadsDir)
			}

			tc.arm(fixture, nil)
			if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet"); err != nil {
				t.Fatalf("bd init once the fault cleared: %v", err)
			}
			cfg, err := configfile.Load(beadsDir)
			if err != nil || cfg == nil || cfg.GetBackend() != name {
				t.Fatalf("metadata.json after the clean init: cfg=%v err=%v", cfg, err)
			}
			last := fixture.opened[len(fixture.opened)-1]
			if last.config["issue_prefix"] != "test" || last.metadata["_project_id"] != cfg.ProjectID {
				t.Fatalf("the clean init's store holds prefix %q and identity %q, want %q and %q",
					last.config["issue_prefix"], last.metadata["_project_id"], "test", cfg.ProjectID)
			}
		})
	}
}

// TestInitRegisteredBackendFailureKeepsAPreexistingBeadsDir is the arm the
// rollback must not take: a .beads directory that was there before the init
// is not this init's to remove. A failed init leaves it and its contents in
// place, still without a metadata.json, and the retry calls Create on what
// the first call left, as the hook's contract says it may.
func TestInitRegisteredBackendFailureKeepsAPreexistingBeadsDir(t *testing.T) {
	const name = "registry-init-preexisting"
	fixture := registerCreatableFixture(t, name)
	tmpDir := registeredInitWorkdir(t)
	beadsDir := filepath.Join(tmpDir, ".beads")
	keep := filepath.Join(beadsDir, "notes.txt")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	errFault := errors.New("fixture fault")
	fixture.openErr = errFault
	if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet"); !errors.Is(err, errFault) {
		t.Fatalf("bd init with open failing: err = %v, want it to carry %v", err, errFault)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "mine" {
		t.Fatalf("the failed init disturbed a file that was there before it: %q, %v", got, err)
	}
	if initPathExists(t, configfile.ConfigPath(beadsDir)) {
		t.Fatal("the failed init left a metadata.json")
	}

	fixture.openErr = nil
	if err := runRegisteredInit("--backend", name, "--prefix", "test", "--quiet"); err != nil {
		t.Fatalf("bd init once open works: %v", err)
	}
	if len(fixture.created) != 2 || fixture.created[1] != beadsDir {
		t.Fatalf("Create hook calls = %q, want two on %q", fixture.created, beadsDir)
	}
	if cfg, err := configfile.Load(beadsDir); err != nil || cfg == nil || cfg.GetBackend() != name {
		t.Fatalf("metadata.json after the retry: cfg=%v err=%v", cfg, err)
	}
}

// TestInitRegisteredBackendRefusesFlagsItCannotHonor holds the route to
// failing closed on a flag it would otherwise drop. Each of these exited
// non-zero for a registered backend before it could be provisioned, and an
// init that accepted one and ignored it would report success for a workspace
// the caller did not ask for. The refusal names the flag and the backend and
// comes before anything is created.
func TestInitRegisteredBackendRefusesFlagsItCannotHonor(t *testing.T) {
	const name = "registry-init-flags"
	fixture := registerCreatableFixture(t, name)
	tmpDir := registeredInitWorkdir(t)
	beadsDir := filepath.Join(tmpDir, ".beads")

	cases := [][]string{
		{"--server"},
		{"--remote", "https://example.invalid/x"},
		{"--stealth"},
		{"--from-jsonl"},
		{"--role", "maintainer"},
		{"--force"},
		{"--proxied-server"},
		{"--database", "elsewhere"},
		{"--db", filepath.Join(tmpDir, "elsewhere.db")},
		{"--" + removedBackendInitFlags[0].name, "x"},
		{"--server", "--remote", "https://example.invalid/x", "--stealth"},
	}
	for _, extra := range cases {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			args := append([]string{"--backend", name, "--prefix", "test", "--quiet"}, extra...)
			err := runRegisteredInit(args...)
			if err == nil {
				t.Fatalf("bd init %s succeeded; want a refusal", strings.Join(args, " "))
			}
			for _, arg := range extra {
				if strings.HasPrefix(arg, "--") && !strings.Contains(err.Error(), arg) {
					t.Errorf("the refusal does not name %s: %v", arg, err)
				}
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the refusal does not name the backend: %v", err)
			}
			if len(fixture.created) != 0 {
				t.Fatalf("the refused init reached the Create hook: %q", fixture.created)
			}
			if initPathExists(t, beadsDir) {
				t.Fatalf("the refused init created %s", beadsDir)
			}
		})
	}
}
