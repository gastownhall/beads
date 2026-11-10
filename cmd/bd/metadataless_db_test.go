package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEmbeddedDatabase creates the coarse marker a real embedded Dolt
// database directory carries (a non-symlink .dolt directory with at least one
// entry), the probe embeddeddolt.Databases reads. name may be "" to create a
// plain directory that is not a database.
func fakeEmbeddedDatabase(t *testing.T, beadsDir, name string, emptyMarker bool) {
	t.Helper()
	if name == "" {
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "plain"), 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	marker := filepath.Join(beadsDir, "embeddeddolt", name, ".dolt")
	if emptyMarker {
		if err := os.MkdirAll(marker, 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(marker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, "config.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestResolveMetadatalessEmbeddedDatabase pins the selection rules for a
// metadata-less embedded workspace (GH#4135): the sole existing database is
// adopted rather than shadowed by the default, the default is kept only when
// no database exists, BEADS_DOLT_SERVER_DATABASE selects the database
// explicitly (sanitized like the open paths sanitize a fresh name, matched
// against existing directories in both spellings), and a workspace whose
// database cannot be named refuses the open instead of guessing. The
// candidate set is the established coarse marker discovery
// (embeddeddolt.Databases): directories without a populated .dolt marker are
// not databases, so they cannot make a sole adoption ambiguous.
func TestResolveMetadatalessEmbeddedDatabase(t *testing.T) {
	cases := []struct {
		name         string
		databases    []string
		plainDirs    []string // non-database directories beside them
		emptyMarkers []string // database-shaped dirs with an empty .dolt
		override     string
		wantDatabase string
		wantSource   string
		wantErr      string // non-empty: expect a refusal containing this
	}{
		{
			name:         "no databases keeps the default",
			wantDatabase: "beads",
			wantSource:   embeddedSelectionSourceDefault,
		},
		{
			name:         "sole database is adopted",
			databases:    []string{"gh4135"},
			wantDatabase: "gh4135",
			wantSource:   embeddedSelectionSourceSole,
		},
		{
			name:         "legacy hyphenated name is adopted as-is",
			databases:    []string{"my-repo"},
			wantDatabase: "my-repo",
			wantSource:   embeddedSelectionSourceSole,
		},
		{
			name:         "override selects that existing database",
			databases:    []string{"gh4135", "other"},
			override:     "gh4135",
			wantDatabase: "gh4135",
			wantSource:   embeddedSelectionSourceOverride,
		},
		{
			name:         "override matches a sanitized spelling",
			databases:    []string{"my_repo"},
			override:     "my-repo",
			wantDatabase: "my_repo",
			wantSource:   embeddedSelectionSourceOverride,
		},
		{
			name:         "override with no existing database is sanitized",
			override:     "my-repo",
			wantDatabase: "my_repo",
			wantSource:   embeddedSelectionSourceOverride,
		},
		{
			name:      "several databases refuse",
			databases: []string{"gh4135", "other"},
			wantErr:   "several embedded databases",
		},
		{
			name:      "override matching none refuses",
			databases: []string{"gh4135", "other"},
			override:  "missing",
			wantErr:   "names none of its embedded databases",
		},
		{
			name:         "plain directory is not a database candidate",
			databases:    []string{"gh4135"},
			plainDirs:    []string{"plain"},
			wantDatabase: "gh4135",
			wantSource:   embeddedSelectionSourceSole,
		},
		{
			name:         "empty .dolt marker is not a database candidate",
			databases:    []string{"gh4135"},
			emptyMarkers: []string{"empty"},
			wantDatabase: "gh4135",
			wantSource:   embeddedSelectionSourceSole,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			for _, database := range tc.databases {
				fakeEmbeddedDatabase(t, beadsDir, database, false)
			}
			for range tc.plainDirs {
				fakeEmbeddedDatabase(t, beadsDir, "", false)
			}
			for _, database := range tc.emptyMarkers {
				fakeEmbeddedDatabase(t, beadsDir, database, true)
			}
			if tc.override != "" {
				t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.override)
			}
			selection, err := resolveMetadatalessEmbeddedDatabase(beadsDir)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("selection = %+v, want a refusal", selection)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("refusal %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMetadatalessEmbeddedDatabase: %v", err)
			}
			if selection.database != tc.wantDatabase {
				t.Errorf("database = %q, want %q", selection.database, tc.wantDatabase)
			}
			if selection.source != tc.wantSource {
				t.Errorf("source = %q, want %q", selection.source, tc.wantSource)
			}
			if summary := selection.summary(); !strings.Contains(summary, tc.wantDatabase) {
				t.Errorf("summary %q does not name the selected database %q", summary, tc.wantDatabase)
			}
		})
	}
}
