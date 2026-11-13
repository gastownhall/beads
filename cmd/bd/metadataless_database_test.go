package main

import (
	"os"
	"path/filepath"
	"strings"
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
		wantErr    []string
	}{
		{name: "no database keeps the default", want: "beads", wantSource: metadatalessDatabaseFromDefault},
		{name: "no database with override", override: "chosen", want: "chosen", wantSource: metadatalessDatabaseFromEnv},
		{name: "sole database adopted", databases: []string{"gh4135"}, want: "gh4135", wantSource: metadatalessDatabaseFromSole},
		{name: "sole legacy name adopted as it is on disk", databases: []string{"my-proj"}, want: "my-proj", wantSource: metadatalessDatabaseFromSole},
		{name: "sole database named by override", databases: []string{"gh4135"}, override: "gh4135", want: "gh4135", wantSource: metadatalessDatabaseFromEnv},
		{name: "several databases refuse", databases: []string{"gh4135", "second"}, wantErr: []string{"gh4135, second", "BEADS_DOLT_SERVER_DATABASE", "metadata.json"}},
		{name: "several databases refuse even with beads among them", databases: []string{"beads", "gh4135"}, wantErr: []string{"beads, gh4135", "BEADS_DOLT_SERVER_DATABASE"}},
		{name: "override names one of several", databases: []string{"gh4135", "second"}, override: "second", want: "second", wantSource: metadatalessDatabaseFromEnv},
		{name: "override names none of several", databases: []string{"gh4135", "second"}, override: "beads", wantErr: []string{"BEADS_DOLT_SERVER_DATABASE=beads", "gh4135, second", "refusing to create another"}},
		{name: "override names none beside the sole database", databases: []string{"gh4135"}, override: "missing", wantErr: []string{"BEADS_DOLT_SERVER_DATABASE=missing", "gh4135"}},
		{name: "override matched as sanitized", databases: []string{"my_proj"}, override: "my-proj", want: "my_proj", wantSource: metadatalessDatabaseFromEnv},
		{name: "override names a legacy database as it is on disk", databases: []string{"my-proj", "second"}, override: "my-proj", want: "my-proj", wantSource: metadatalessDatabaseFromEnv},
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
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("selected %q (%s), want a refusal", got.Database, got.Source)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal does not mention %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMetadatalessEmbeddedDatabase: %v", err)
			}
			if got.Database != tc.want || got.Source != tc.wantSource {
				t.Fatalf("selected %q (%s), want %q (%s)", got.Database, got.Source, tc.want, tc.wantSource)
			}
		})
	}
}
