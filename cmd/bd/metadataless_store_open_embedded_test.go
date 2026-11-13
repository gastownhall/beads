//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
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

// TestEmbeddedMetadatalessStoreOpensKeepUnambiguousBehavior pins that the
// GH#4381 check changes nothing for a metadata-less store that is empty or
// holds only the effective target: the factories and the SDK open it as
// before, the strict paths keep their own rules (a routed write still needs
// an existing database, reads and previews still create none), the
// environment still selects the target where it did, and the writable
// factory still migrates a legacy unsanitized directory.
func TestEmbeddedMetadatalessStoreOpensKeepUnambiguousBehavior(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, tc := range []struct{ name, env, target string }{
		{name: "sole default database", target: "beads"},
		{name: "sole env-selected database", env: "foo", target: "foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.env)
			}
			beadsDir := t.TempDir()
			seedEmbeddedDatabases(t, beadsDir, tc.target)
			for _, opener := range metadatalessOpeners() {
				t.Run(opener.name, func(t *testing.T) {
					store, err := opener.open(t.Context(), beadsDir)
					if err != nil {
						t.Fatalf("open of the sole target %q failed: %v", tc.target, err)
					}
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					requireEmbeddedDatabases(t, beadsDir, tc.target)
				})
			}
		})
	}

	t.Run("empty store", func(t *testing.T) {
		for _, opener := range metadatalessOpeners() {
			t.Run(opener.name, func(t *testing.T) {
				beadsDir := t.TempDir()
				store, err := opener.open(t.Context(), beadsDir)
				var refusal *configfile.MetadatalessDatabaseError
				if errors.As(err, &refusal) {
					t.Fatalf("an empty store got the metadata-less refusal: %v", err)
				}
				switch opener.name {
				case "newDoltStoreFromConfig", "beads.OpenBestAvailable", "beads.OpenBestAvailableWith":
					// A fresh workspace still gets the default database.
					if err != nil {
						t.Fatalf("open of an empty store failed: %v", err)
					}
					_ = store.Close()
					requireEmbeddedDatabases(t, beadsDir, "beads")
				case "newExistingDoltStoreFromConfig":
					// A routed write still does not create a database.
					if err == nil {
						_ = store.Close()
						t.Fatal("routed write opened an empty store; want its existing no-database refusal")
					}
					requireEmbeddedDatabases(t, beadsDir)
				default:
					// Reads and previews still create nothing.
					if err == nil {
						_ = store.Close()
					}
					requireEmbeddedDatabases(t, beadsDir)
				}
			})
		}
	})

	t.Run("writable factory sanitation", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "my-db")
		for _, legacy := range []bool{false, true} {
			beadsDir := t.TempDir()
			seedEmbeddedDatabases(t, beadsDir, "my_db")
			if legacy {
				// The sole directory is the unsanitized legacy name, which
				// the factory's existing migration renames to my_db.
				root := filepath.Join(beadsDir, "embeddeddolt")
				if err := os.Rename(filepath.Join(root, "my_db"), filepath.Join(root, "my-db")); err != nil {
					t.Fatal(err)
				}
			}
			store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
			if err != nil {
				t.Fatalf("writable open (legacy dir %v) failed: %v", legacy, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			requireEmbeddedDatabases(t, beadsDir, "my_db")
		}
	})

	t.Run("nonmutating sanitation", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "my-db")
		beadsDir := t.TempDir()
		seedEmbeddedDatabases(t, beadsDir, "my_db")
		for _, open := range []func(context.Context, string) (storage.DoltStorage, error){newReadOnlyStoreFromConfig, newPreviewStoreFromConfig} {
			store, err := open(t.Context(), beadsDir)
			if err != nil {
				t.Fatalf("nonmutating open of the sanitized my_db failed: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			requireEmbeddedDatabases(t, beadsDir, "my_db")
		}
	})
}

// TestEmbeddedMetadatalessStoreOpensRefuseSoleOtherDatabase is GH#4381 at the
// store factories and the SDK: a metadata-less store whose only database is
// not the effective target refuses with the shared metadata-less error
// instead of creating the target beside it, for the default fallback, an
// env-selected target, and the factories' sanitized names alike.
func TestEmbeddedMetadatalessStoreOpensRefuseSoleOtherDatabase(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, tc := range []struct{ name, env, target string }{
		{name: "default target absent", target: "beads"},
		{name: "env-selected target absent", env: "foo", target: "foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.env)
			}
			beadsDir := t.TempDir()
			seedEmbeddedDatabases(t, beadsDir, "myproj")
			for _, opener := range metadatalessOpeners() {
				t.Run(opener.name, func(t *testing.T) {
					store, err := opener.open(t.Context(), beadsDir)
					requireMetadatalessRefusal(t, store, err, tc.target, "myproj")
					requireEmbeddedDatabases(t, beadsDir, "myproj")
					if _, statErr := os.Stat(configfile.ConfigPath(beadsDir)); !os.IsNotExist(statErr) {
						t.Fatalf("refused open wrote %s (stat err %v)", configfile.ConfigPath(beadsDir), statErr)
					}
				})
			}
		})
	}

	// renameDatabase renames a seeded database directory, giving the store a
	// legacy unsanitized name that embeddeddolt.Open would not create.
	renameDatabase := func(t *testing.T, beadsDir, from, to string) {
		t.Helper()
		root := filepath.Join(beadsDir, "embeddeddolt")
		if err := os.Rename(filepath.Join(root, from), filepath.Join(root, to)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("writable factory sanitized collision", func(t *testing.T) {
		// Both the legacy my-db and the sanitized my_db exist: neither is
		// the sole target, so the factory refuses before its rename.
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "my-db")
		beadsDir := t.TempDir()
		seedEmbeddedDatabases(t, beadsDir, "my_db", "legacy")
		renameDatabase(t, beadsDir, "legacy", "my-db")
		store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
		requireMetadatalessRefusal(t, store, err, "my_db", "my-db", "my_db")
		requireEmbeddedDatabases(t, beadsDir, "my-db", "my_db")
	})

	t.Run("nonmutating factories legacy unsanitized only", func(t *testing.T) {
		// Reads and previews open only the sanitized my_db; a store holding
		// just the legacy my-db does not hold their target.
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "my-db")
		beadsDir := t.TempDir()
		seedEmbeddedDatabases(t, beadsDir, "legacy")
		renameDatabase(t, beadsDir, "legacy", "my-db")
		for _, open := range []func(context.Context, string) (storage.DoltStorage, error){newReadOnlyStoreFromConfig, newPreviewStoreFromConfig} {
			store, err := open(t.Context(), beadsDir)
			var closer interface{ Close() error }
			if store != nil {
				closer = store
			}
			requireMetadatalessRefusal(t, closer, err, "my_db", "my-db")
			requireEmbeddedDatabases(t, beadsDir, "my-db")
		}
	})
}
