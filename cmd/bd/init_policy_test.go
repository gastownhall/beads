package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

func TestRunInitRemoteClone(t *testing.T) {
	t.Run("passes explicit HTTP URL through unchanged", func(t *testing.T) {
		const remoteURL = "http://127.0.0.1:1/no-such-db"
		var gotURL string
		resolvedURL, source := resolveInitConfiguredSyncRemote(remoteURL, true, func() string {
			t.Fatal("explicit remote must not resolve a configured fallback")
			return ""
		})
		if resolvedURL != remoteURL {
			t.Fatalf("resolved URL = %q, want %q", resolvedURL, remoteURL)
		}
		if source != initSyncRemoteExplicit {
			t.Fatalf("source = %v, want explicit", source)
		}

		disposition, err := runInitRemoteClone(resolvedURL, func(url string) error {
			gotURL = url
			return nil
		})
		if err != nil {
			t.Fatalf("runInitRemoteClone: %v", err)
		}
		if gotURL != remoteURL {
			t.Fatalf("clone URL = %q, want %q", gotURL, remoteURL)
		}
		if disposition != initRemoteCloneBootstrapped {
			t.Fatalf("disposition = %v, want bootstrapped", disposition)
		}
	})

	t.Run("empty remote selects fresh initialization", func(t *testing.T) {
		const remoteURL = "file:///empty"
		resolvedURL, source := resolveInitConfiguredSyncRemote(remoteURL, true, func() string {
			t.Fatal("explicit remote must not resolve a configured fallback")
			return ""
		})
		if resolvedURL != remoteURL || source != initSyncRemoteExplicit {
			t.Fatalf("resolved remote = (%q, %v), want (%q, explicit)", resolvedURL, source, remoteURL)
		}
		syncURLFromConfig := resolvedURL != "" && source != initSyncRemoteNone
		called := false
		disposition, err := runInitRemoteClone(resolvedURL, func(string) error {
			called = true
			return errors.New("remote contains no Dolt data")
		})
		if err != nil {
			t.Fatalf("runInitRemoteClone: %v", err)
		}
		if !called {
			t.Fatal("clone callback was not called")
		}
		if disposition != initRemoteCloneFresh {
			t.Fatalf("disposition = %v, want fresh", disposition)
		}

		// The production call site clears syncFromRemote after this fresh
		// fallback, but the explicit source remains a durable remote-wiring
		// intent for both the Dolt remote and config.yaml.
		if !shouldWireInitRemote(resolvedURL, false, syncURLFromConfig, false) {
			t.Fatal("fresh fallback lost explicit remote wiring")
		}
		if !shouldWriteInitDoltRemote(false, resolvedURL, false, syncURLFromConfig, false, false) {
			t.Fatal("fresh fallback lost explicit Dolt remote wiring")
		}

		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir beads dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
		if err := persistInitSyncRemote(beadsDir, remoteURL, resolvedURL, false, syncURLFromConfig, false); err != nil {
			t.Fatalf("persistInitSyncRemote: %v", err)
		}
		configBytes, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		if !strings.Contains(string(configBytes), remoteURL) {
			t.Fatalf("config.yaml does not contain explicit remote %q:\n%s", remoteURL, configBytes)
		}
	})

	t.Run("preserves non-empty clone failure", func(t *testing.T) {
		want := errors.New("permission denied")
		disposition, err := runInitRemoteClone("https://example.invalid/beads", func(string) error {
			return want
		})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
		if disposition != initRemoteCloneFailed {
			t.Fatalf("disposition = %v, want failed", disposition)
		}
	})
}

func TestDetectInitRemoteHostConflict(t *testing.T) {
	for _, tt := range []struct {
		name                string
		configHost, envHost string
		configPort, envPort string
		want                *initRemoteHostConflict
	}{
		{name: "port only", configPort: "3306"},
		{
			name:       "remote config host",
			configHost: "100.111.197.110",
			want:       &initRemoteHostConflict{host: "100.111.197.110", source: "config.yaml"},
		},
		{name: "localhost environment overrides remote config", configHost: "100.111.197.110", envHost: "localhost"},
		{name: "loopback environment overrides remote config", configHost: "100.111.197.110", envHost: "127.0.0.1"},
		{name: "IPv6 loopback environment, bare", configHost: "100.111.197.110", envHost: "::1"},
		{name: "IPv6 loopback environment, bracketed", configHost: "100.111.197.110", envHost: "[::1]"},
		{
			name:       "remote environment takes precedence",
			configHost: "127.0.0.1",
			envHost:    "100.111.197.110",
			envPort:    "3307",
			want:       &initRemoteHostConflict{host: "100.111.197.110", source: "environment", includesPort: true},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := detectInitRemoteHostConflict(tt.configHost, tt.envHost, tt.configPort, tt.envPort)
			if got == nil || tt.want == nil {
				if got != tt.want {
					t.Fatalf("detectInitRemoteHostConflict() = %#v, want %#v", got, tt.want)
				}
				return
			}
			if *got != *tt.want {
				t.Fatalf("detectInitRemoteHostConflict() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestConfiguredImportJSONLPath(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	const beadsDir = "/workspace/.beads"
	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{name: "default", want: filepath.Join(beadsDir, "issues.jsonl")},
		{name: "configured", path: "beads.jsonl", want: filepath.Join(beadsDir, "beads.jsonl")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config.Set("import.path", tt.path)
			if got := configuredImportJSONLPath(beadsDir); got != tt.want {
				t.Errorf("configuredImportJSONLPath(%q) = %q, want %q", beadsDir, got, tt.want)
			}
		})
	}
}

func TestNormalizeIssuePrefixAndDatabaseName(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "dotted directory", raw: "MyPkg.jl", want: "MyPkg_jl"},
		{name: "leading dot", raw: ".claude", want: "claude"},
		{name: "trailing hyphen", raw: "project-", want: "project"},
		{name: "numeric start", raw: "001", want: "bd_001"},
		{name: "database hyphen", raw: "my-prefix", want: "my_prefix"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := dbNameFromPrefix(normalizeIssuePrefix(tt.raw))
			if got != tt.want {
				t.Errorf("dbNameFromPrefix(normalizeIssuePrefix(%q)) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestDoltRemoteURL pins which URLs bd rewrites before handing them to Dolt —
// for DOLT_CLONE, for DOLT_REMOTE('add', ...) and therefore for DOLT_PUSH.
// Forge URLs must arrive in git+ form so Dolt uses the git remote factory
// rather than the remotesapi retry storm (#4421); a user-configured remotesapi
// endpoint must arrive byte-identical (GH#3339). bd init and bd bootstrap
// share this one helper so they can never disagree about the URL derived from
// a single committed sync.remote.
func TestDoltRemoteURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"raw https forge", "https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"forge without .git", "https://github.com/org/repo", "git+https://github.com/org/repo"},
		{"scp style forge", "git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"ssh forge", "ssh://git@gitlab.com/org/repo.git", "git+ssh://git@gitlab.com/org/repo.git"},
		{"already normalized", "git+https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},

		{"remotesapi endpoint", "http://myserver:7007/mydb", "http://myserver:7007/mydb"},
		{"remotesapi https endpoint", "https://doltremoteapi.dolthub.com/org/db", "https://doltremoteapi.dolthub.com/org/db"},
		{"dolthub native", "dolthub://myorg/mydb", "dolthub://myorg/mydb"},
		{"s3 native", "s3://bucket/path", "s3://bucket/path"},
		{"self-hosted dolt over ssh", "git+ssh://my-dolt.example.com/org/db", "git+ssh://my-dolt.example.com/org/db"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doltRemoteURL(tt.in); got != tt.want {
				t.Errorf("doltRemoteURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRunInitRemoteCloneReceivesRoutedURL proves the routing survives the call
// into runInitRemoteClone — the seam the init RunE body actually uses.
func TestRunInitRemoteCloneReceivesRoutedURL(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"http://myserver:7007/mydb", "http://myserver:7007/mydb"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			var gotURL string
			if _, err := runInitRemoteClone(doltRemoteURL(tt.in), func(url string) error {
				gotURL = url
				return nil
			}); err != nil {
				t.Fatalf("runInitRemoteClone: %v", err)
			}
			if gotURL != tt.want {
				t.Errorf("clone received %q, want %q", gotURL, tt.want)
			}
		})
	}
}

// initIdentityStore is a store whose Bootstrapper is a spy, for the identity
// publication tests: nothing else on it is reachable.
type initIdentityStore struct {
	storage.DoltStorage
	boot *spyInitBootstrapper
}

func (s *initIdentityStore) Bootstrapper() (issueops.Bootstrapper, error) { return s.boot, nil }

type spyInitBootstrapper struct {
	err   error
	calls int
	got   issueops.BootstrapRequest
	// metadataAtBootstrap records whether metadata.json existed when
	// Bootstrap ran, proving publication follows the seed.
	beadsDir            string
	metadataAtBootstrap bool
}

func (b *spyInitBootstrapper) Bootstrap(_ context.Context, req issueops.BootstrapRequest) (issueops.BootstrapResult, error) {
	b.calls++
	b.got = req
	if _, err := os.Stat(filepath.Join(b.beadsDir, "metadata.json")); err == nil {
		b.metadataAtBootstrap = true
	}
	if b.err != nil {
		return issueops.BootstrapResult{}, b.err
	}
	return issueops.BootstrapResult{Prefix: req.Prefix, ProjectID: req.ProjectID}, nil
}

// initIdentityFixture is a .beads with an optional existing metadata.json and
// the metadata init composed in memory with a freshly chosen project id.
func initIdentityFixture(t *testing.T, existing []byte) (beadsDir string, cfg *configfile.Config) {
	t.Helper()
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if existing != nil {
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), existing, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg = configfile.DefaultConfig()
	cfg.ProjectID = configfile.GenerateProjectID()
	cfg.DoltDatabase = "newproj"
	return beadsDir, cfg
}

// AC-6: two initializers both read an unidentified database; the loser's
// bootstrap is refused (or fails). It must return the failure without
// publishing the identity it chose: new metadata stays absent, existing
// metadata keeps its bytes.
func TestInitIdentityPublicationLoserPublishesNothing(t *testing.T) {
	winner := &issueops.AlreadyIdentifiedError{Prefix: "winner", ProjectID: "proj-winner"}
	other := errors.New("connection reset")
	existingBytes := []byte(`{"backend":"dolt","database":"dolt","dolt_database":"keep"}` + "\n")
	for _, c := range []struct {
		name     string
		bootErr  error
		existing []byte
		wantIs   error
	}{
		{"refused-new-metadata", winner, nil, issueops.ErrAlreadyIdentified},
		{"refused-existing-metadata", winner, existingBytes, issueops.ErrAlreadyIdentified},
		{"failed-new-metadata", other, nil, other},
		{"failed-existing-metadata", other, existingBytes, other},
	} {
		t.Run(c.name, func(t *testing.T) {
			beadsDir, cfg := initIdentityFixture(t, c.existing)
			boot := &spyInitBootstrapper{err: c.bootErr, beadsDir: beadsDir}
			store := &initIdentityStore{boot: boot}

			err := publishInitWorkspaceIdentity(context.Background(), store, issueops.VerifyIdentityResult{}, "newproj", cfg.ProjectID, beadsDir, cfg, true)
			if !errors.Is(err, c.wantIs) {
				t.Fatalf("err = %v, want it to wrap %v", err, c.wantIs)
			}
			if boot.calls != 1 {
				t.Fatalf("Bootstrap calls = %d, want 1", boot.calls)
			}
			got, readErr := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
			if c.existing == nil {
				if !os.IsNotExist(readErr) {
					t.Fatalf("losing init published metadata.json (read err %v): %s", readErr, got)
				}
				return
			}
			if readErr != nil || !bytes.Equal(got, c.existing) {
				t.Fatalf("losing init changed metadata.json:\n got %s\nwant %s (err %v)", got, c.existing, readErr)
			}
		})
	}
}

// AC-6: on the paths that succeed, metadata.json is published after the
// seed (never before), the seed runs at most once, and the adoption, gateway
// and no-local-metadata paths keep their behavior.
func TestInitIdentityPublicationOrder(t *testing.T) {
	t.Run("bootstrap-then-save", func(t *testing.T) {
		beadsDir, cfg := initIdentityFixture(t, nil)
		boot := &spyInitBootstrapper{beadsDir: beadsDir}
		if err := publishInitWorkspaceIdentity(context.Background(), &initIdentityStore{boot: boot}, issueops.VerifyIdentityResult{}, "newproj", cfg.ProjectID, beadsDir, cfg, true); err != nil {
			t.Fatal(err)
		}
		if boot.calls != 1 || boot.metadataAtBootstrap {
			t.Fatalf("Bootstrap calls=%d, metadata existed at bootstrap=%v; want 1 call before publication", boot.calls, boot.metadataAtBootstrap)
		}
		if boot.got.ProjectID != cfg.ProjectID || boot.got.Prefix != "newproj" {
			t.Fatalf("bootstrapped %+v, want prefix newproj and the composed project id %s", boot.got, cfg.ProjectID)
		}
		saved, err := configfile.Load(beadsDir)
		if err != nil || saved == nil || saved.ProjectID != cfg.ProjectID {
			t.Fatalf("published metadata = %+v (err %v), want project id %s", saved, err, cfg.ProjectID)
		}
	})
	t.Run("adopt-existing-identity", func(t *testing.T) {
		beadsDir, cfg := initIdentityFixture(t, nil)
		cfg.ProjectID = "proj-adopted"
		boot := &spyInitBootstrapper{beadsDir: beadsDir}
		found := issueops.VerifyIdentityResult{Prefix: "winner", ProjectID: "proj-adopted"}
		if err := publishInitWorkspaceIdentity(context.Background(), &initIdentityStore{boot: boot}, found, "winner", cfg.ProjectID, beadsDir, cfg, true); err != nil {
			t.Fatal(err)
		}
		if boot.calls != 0 {
			t.Fatalf("Bootstrap ran %d times over an identified database, want adoption", boot.calls)
		}
		if saved, err := configfile.Load(beadsDir); err != nil || saved == nil || saved.ProjectID != "proj-adopted" {
			t.Fatalf("adoption did not publish metadata: %+v (err %v)", saved, err)
		}
	})
	t.Run("gateway-skips-seed", func(t *testing.T) {
		beadsDir, cfg := initIdentityFixture(t, nil)
		boot := &spyInitBootstrapper{beadsDir: beadsDir}
		if err := publishInitWorkspaceIdentity(context.Background(), &initIdentityStore{boot: boot}, issueops.VerifyIdentityResult{}, "p", cfg.ProjectID, beadsDir, cfg, false); err != nil {
			t.Fatal(err)
		}
		if boot.calls != 0 {
			t.Fatalf("gateway path seeded %d times, want 0", boot.calls)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); err != nil {
			t.Fatalf("gateway path did not publish metadata: %v", err)
		}
	})
	t.Run("no-local-metadata", func(t *testing.T) {
		existing := []byte(`{"backend":"dolt","database":"dolt","project_id":"proj-file"}` + "\n")
		beadsDir, _ := initIdentityFixture(t, existing)
		boot := &spyInitBootstrapper{beadsDir: beadsDir}
		if err := publishInitWorkspaceIdentity(context.Background(), &initIdentityStore{boot: boot}, issueops.VerifyIdentityResult{}, "p", "", beadsDir, nil, true); err != nil {
			t.Fatal(err)
		}
		if boot.calls != 1 || boot.got.ProjectID != "proj-file" {
			t.Fatalf("Bootstrap calls=%d request=%+v, want one seed with the file's project id", boot.calls, boot.got)
		}
		if got, _ := os.ReadFile(filepath.Join(beadsDir, "metadata.json")); !bytes.Equal(got, existing) {
			t.Fatalf("no-local-metadata path rewrote metadata.json: %s", got)
		}
	})
}
