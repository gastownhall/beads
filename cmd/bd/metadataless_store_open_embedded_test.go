//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// metadatalessOpener is one store-construction path that falls back to a
// default database when the workspace has no metadata (GH#4381).
type metadatalessOpener struct {
	name string
	open func(ctx context.Context, beadsDir string) (interface{ Close() error }, error)
}

// metadatalessOpeners are the store factories and the SDK opens that reach an
// embedded open of a metadata-less workspace: the writable factory, its
// require-existing (routed write) variant, the nonmutating read and preview
// factories, and the SDK's OpenBestAvailable and OpenBestAvailableWith.
func metadatalessOpeners() []metadatalessOpener {
	return []metadatalessOpener{
		{"newDoltStoreFromConfig", func(ctx context.Context, d string) (interface{ Close() error }, error) {
			return newDoltStoreFromConfig(ctx, d)
		}},
		{"newExistingDoltStoreFromConfig", func(ctx context.Context, d string) (interface{ Close() error }, error) {
			return newExistingDoltStoreFromConfig(ctx, d)
		}},
		{"newReadOnlyStoreFromConfig", func(ctx context.Context, d string) (interface{ Close() error }, error) {
			return newReadOnlyStoreFromConfig(ctx, d)
		}},
		{"newPreviewStoreFromConfig", func(ctx context.Context, d string) (interface{ Close() error }, error) {
			return newPreviewStoreFromConfig(ctx, d)
		}},
		{"beads.OpenBestAvailable", func(ctx context.Context, d string) (interface{ Close() error }, error) {
			return beads.OpenBestAvailable(ctx, d)
		}},
		{"beads.OpenBestAvailableWith", func(ctx context.Context, d string) (interface{ Close() error }, error) {
			return beads.OpenBestAvailableWith(ctx, d, beads.OpenOptions{})
		}},
	}
}

// skipUnlessEmbeddedDolt skips a test that opens embedded Dolt outside the
// embedded tier, and clears the env layers that would otherwise pick a
// server or a database for a metadata-less workspace.
func skipUnlessEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	for _, name := range []string{
		"BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
	} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

// seedEmbeddedDatabases creates real embedded databases named names under
// beadsDir's store, the way a configured open creates its database.
func seedEmbeddedDatabases(t *testing.T, beadsDir string, names ...string) {
	t.Helper()
	for _, name := range names {
		store, err := embeddeddolt.Open(t.Context(), beadsDir, name, "main")
		if err != nil {
			t.Fatalf("seed embedded database %q: %v", name, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close seeded database %q: %v", name, err)
		}
	}
	requireEmbeddedDatabases(t, beadsDir, names...)
}

// requireEmbeddedDatabases fails unless beadsDir's store holds exactly want
// (in any order).
func requireEmbeddedDatabases(t *testing.T, beadsDir string, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, name := range configfile.EmbeddedDoltDatabases(beadsDir) {
		got[name] = true
	}
	wantSet := map[string]bool{}
	for _, name := range want {
		wantSet[name] = true
	}
	if !reflect.DeepEqual(got, wantSet) {
		t.Fatalf("embedded databases = %v, want %v", configfile.EmbeddedDoltDatabases(beadsDir), want)
	}
}

// sortedNames returns a sorted copy of names, the order refusals name them in.
func sortedNames(names []string) []string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	return sorted
}

// requireMetadatalessRefusal fails unless err is the shared metadata-less
// refusal for target naming exactly found, and the open returned no store.
func requireMetadatalessRefusal(t *testing.T, store interface{ Close() error }, err error, target string, found ...string) {
	t.Helper()
	if err == nil {
		_ = store.Close()
		t.Fatalf("open succeeded without metadata against a store holding %v; want the metadata-less refusal for %q", found, target)
	}
	var refusal *configfile.MetadatalessDatabaseError
	if !errors.As(err, &refusal) {
		t.Fatalf("open error = %v, want *configfile.MetadatalessDatabaseError", err)
	}
	if refusal.Database != target || !reflect.DeepEqual(refusal.Found, found) {
		t.Fatalf("refusal = %+v, want Database %q, Found %v", refusal, target, found)
	}
}

// TestEmbeddedMetadatalessStoreOpensRefuseSeveralDatabases covers the
// aftermath of GH#4381 at every store factory and SDK open: a metadata-less
// store already holding a stray sibling beside the real database refuses,
// naming both, even though the fallback database exists, and opens nothing.
func TestEmbeddedMetadatalessStoreOpensRefuseSeveralDatabases(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, tc := range []struct {
		name, env, target string
		seed              []string
	}{
		{name: "default fallback exists", target: "beads", seed: []string{"beads", "myproj"}},
		{name: "default fallback absent", target: "beads", seed: []string{"myproj", "other"}},
		{name: "env-selected target exists", env: "foo", target: "foo", seed: []string{"foo", "myproj"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.env)
			}
			beadsDir := t.TempDir()
			seedEmbeddedDatabases(t, beadsDir, tc.seed...)
			for _, opener := range metadatalessOpeners() {
				t.Run(opener.name, func(t *testing.T) {
					store, err := opener.open(t.Context(), beadsDir)
					requireMetadatalessRefusal(t, store, err, tc.target, sortedNames(tc.seed)...)
					requireEmbeddedDatabases(t, beadsDir, tc.seed...)
					if _, statErr := os.Stat(configfile.ConfigPath(beadsDir)); !os.IsNotExist(statErr) {
						t.Fatalf("refused open wrote %s (stat err %v)", configfile.ConfigPath(beadsDir), statErr)
					}
				})
			}
		})
	}
}
