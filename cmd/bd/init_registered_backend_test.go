package main

import (
	"context"
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
// on a fresh workspace, and the reads this test uses to check them.
type createFixtureStore struct {
	storage.DoltStorage
	config   map[string]string
	metadata map[string]string
	closed   int
}

func (s *createFixtureStore) SetConfig(_ context.Context, key, value string) error {
	s.config[key] = value
	return nil
}

func (s *createFixtureStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.config[key], nil
}

func (s *createFixtureStore) SetMetadata(_ context.Context, key, value string) error {
	s.metadata[key] = value
	return nil
}

func (s *createFixtureStore) GetMetadata(_ context.Context, key string) (string, error) {
	return s.metadata[key], nil
}

func (s *createFixtureStore) Close() error {
	s.closed++
	return nil
}

// TestInitProvisionsRegisteredBackendWithCreate is the other half of the
// fail-closed rule above: a registered backend that supplies the Create hook
// IS provisioned by bd init, through the hook and nothing else. The test
// registers a backend whose Create records the directory it was handed and
// whose Open returns an in-memory stand-in, runs bd init on it, and checks
// what a later command would open: the standard .beads/.gitignore,
// metadata.json naming the backend with a fresh project id, the issue prefix and the identity written through the
// store, and the store closed. A second init is refused, and --init-if-missing
// returns without creating again.
func TestInitProvisionsRegisteredBackendWithCreate(t *testing.T) {
	const name = "registry-init-create"
	var (
		created  []string
		opened   []*createFixtureStore
		register = func() {
			backends.Register(name, backends.Backend{
				Open: func(context.Context, string) (storage.DoltStorage, error) {
					s := &createFixtureStore{config: map[string]string{}, metadata: map[string]string{}}
					opened = append(opened, s)
					return s, nil
				},
				OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
					return nil, errRegistryReadOnly
				},
				Create: func(_ context.Context, beadsDir string) error {
					created = append(created, beadsDir)
					return os.MkdirAll(filepath.Join(beadsDir, "fixture-store"), 0o700)
				},
				WorkspaceIsBeadsDir: true,
			})
		}
	)
	register()
	t.Cleanup(func() { backends.Deregister(name) })

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
		_ = initCmd.Flags().Set("init-if-missing", "false")
	})
	dbPath = ""
	store = nil
	isolateBeadsDirForTest(t)

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	beadsDir := filepath.Join(tmpDir, ".beads")

	rootCmd.SetArgs([]string{"init", "--backend", name, "--prefix", "test", "--quiet"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("bd init --backend=%s: %v", name, err)
	}

	if len(created) != 1 || created[0] != beadsDir {
		t.Fatalf("Create hook calls = %q, want exactly [%q]", created, beadsDir)
	}
	if info, err := os.Stat(filepath.Join(beadsDir, "fixture-store")); err != nil || !info.IsDir() {
		t.Fatalf("the hook's store directory: %v", err)
	}
	if ignore, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore")); err != nil || !strings.Contains(string(ignore), ".beads-credential-key") {
		t.Fatalf("the standard .beads/.gitignore: err=%v", err)
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
	if len(opened) != 1 {
		t.Fatalf("init opened %d stores, want 1", len(opened))
	}
	s := opened[0]
	if got := s.config["issue_prefix"]; got != "test" {
		t.Errorf("issue_prefix written to the store = %q, want %q", got, "test")
	}
	if got := s.metadata["_project_id"]; got != cfg.ProjectID {
		t.Errorf("_project_id written to the store = %q, want %q", got, cfg.ProjectID)
	}
	if s.closed != 1 {
		t.Errorf("init closed the store %d times, want 1", s.closed)
	}

	rootCmd.SetArgs([]string{"init", "--backend", name, "--prefix", "test", "--quiet"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("a second bd init on the initialized workspace succeeded; want a refusal")
	}
	rootCmd.SetArgs([]string{"init", "--backend", name, "--prefix", "test", "--quiet", "--init-if-missing"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("bd init --init-if-missing on the initialized workspace: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("Create hook calls after the repeat inits = %d, want 1", len(created))
	}
}
