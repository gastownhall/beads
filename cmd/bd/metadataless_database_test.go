package main

import (
	"os"
	"path/filepath"
	"testing"
)

// makeEmbeddedDatabaseMarker lays out the coarse .dolt marker
// embeddeddolt.Databases recognizes for database under beadsDir.
func makeEmbeddedDatabaseMarker(t *testing.T, beadsDir, database string) {
	t.Helper()
	marker := filepath.Join(beadsDir, "embeddeddolt", database, ".dolt")
	if err := os.MkdirAll(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, "repo_state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveMetadatalessEmbeddedDatabase(t *testing.T) {
	cases := []struct {
		name       string
		databases  []string
		override   string
		layout     func(t *testing.T, beadsDir string)
		want       string
		wantSource string
	}{
		{name: "no database keeps the default", want: "beads", wantSource: metadatalessDatabaseFromDefault},
		{name: "no database with override", override: "chosen", want: "chosen", wantSource: metadatalessDatabaseFromEnv},
		{name: "sole database adopted", databases: []string{"gh4135"}, want: "gh4135", wantSource: metadatalessDatabaseFromSole},
		{name: "sole legacy name adopted as it is on disk", databases: []string{"my-proj"}, want: "my-proj", wantSource: metadatalessDatabaseFromSole},
		{name: "sole database named by override", databases: []string{"gh4135"}, override: "gh4135", want: "gh4135", wantSource: metadatalessDatabaseFromEnv},
		{
			name: "empty marker is not a database",
			layout: func(t *testing.T, beadsDir string) {
				makeEmbeddedDatabaseMarker(t, beadsDir, "gh4135")
				if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "empty", ".dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "gh4135", wantSource: metadatalessDatabaseFromSole,
		},
		{
			name: "symlinked database is not a database",
			layout: func(t *testing.T, beadsDir string) {
				makeEmbeddedDatabaseMarker(t, beadsDir, "gh4135")
				outside := t.TempDir()
				makeEmbeddedDatabaseMarker(t, outside, "elsewhere")
				if err := os.Symlink(filepath.Join(outside, "embeddeddolt", "elsewhere"), filepath.Join(beadsDir, "embeddeddolt", "linked")); err != nil {
					t.Skipf("symlink: %v", err)
				}
			},
			want: "gh4135", wantSource: metadatalessDatabaseFromSole,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			for _, database := range tc.databases {
				makeEmbeddedDatabaseMarker(t, beadsDir, database)
			}
			if tc.layout != nil {
				tc.layout(t, beadsDir)
			}
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.override)
			got, err := resolveMetadatalessEmbeddedDatabase(beadsDir)
			if err != nil {
				t.Fatalf("resolveMetadatalessEmbeddedDatabase: %v", err)
			}
			if got.Database != tc.want || got.Source != tc.wantSource {
				t.Fatalf("selected %q (%s), want %q (%s)", got.Database, got.Source, tc.want, tc.wantSource)
			}
		})
	}
}
