package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
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
