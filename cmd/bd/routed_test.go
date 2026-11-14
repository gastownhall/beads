package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMatchPrefixRoute pins longest-prefix route selection: multi-hyphen
// route prefixes must match their IDs instead of being shadowed by a
// first-dash cut (GH#5048).
func TestMatchPrefixRoute(t *testing.T) {
	routes := []prefixRoute{
		{Prefix: "claude-os-", Path: "rigA"},
		{Prefix: "solo-", Path: "rigB"},
		{Prefix: "hq-", Path: "hq"},
		{Prefix: "hq-cv-", Path: "convoys"},
		{Prefix: "nodash", Path: "bogus"},
	}
	tests := []struct {
		id   string
		want string // matched route path; "" means no match
	}{
		{"claude-os-sar", "rigA"},
		{"claude-os-76l.1", "rigA"},
		{"solo-x", "rigB"},
		{"hq-abc", "hq"},
		{"hq-cv-abc", "convoys"},
		{"claude-abc", ""},
		{"other-abc", ""},
		{"nodash-abc", ""},
		{"solo", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := matchPrefixRoute(routes, tt.id)
			gotPath := ""
			if got != nil {
				gotPath = got.Path
			}
			if gotPath != tt.want {
				t.Errorf("matchPrefixRoute(%q) = %q, want %q", tt.id, gotPath, tt.want)
			}
		})
	}
}

// TestAutoRouteTargetDatabase pins the dolt_database an auto-routed open takes
// from its target's metadata.json: a write refuses a file it cannot parse or
// that names no database, which a read tolerates. A target with no
// metadata.json, the planning repository contributor setup leaves, opens the
// only embedded database it holds either way, and its default database when it
// holds none or several.
func TestAutoRouteTargetDatabase(t *testing.T) {
	tests := []struct {
		name      string
		metadata  string   // metadata.json content; "" writes no file
		databases []string // embedded databases the target holds
		want      string
		wantErr   bool // for a writable open; a read never errs
	}{
		{"names_database", `{"dolt_database":"planning"}`, []string{"proj"}, "planning", false},
		{"no_metadata_json", "", nil, "beads", false},
		{"no_metadata_json_one_database", "", []string{"proj"}, "proj", false},
		{"no_metadata_json_several_databases", "", []string{"alpha", "proj"}, "beads", false},
		{"no_database_field", `{"backend":"dolt"}`, []string{"proj"}, "", true},
		{"unparseable", "{not json", nil, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			for _, database := range tt.databases {
				marker := filepath.Join(beadsDir, "embeddeddolt", database, ".dolt")
				if err := os.MkdirAll(marker, 0o700); err != nil {
					t.Fatalf("create database %s: %v", database, err)
				}
				if err := os.WriteFile(filepath.Join(marker, "opaque-entry"), nil, 0o600); err != nil {
					t.Fatalf("fill database %s: %v", database, err)
				}
			}
			if tt.metadata != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(tt.metadata), 0o600); err != nil {
					t.Fatalf("write metadata.json: %v", err)
				}
			}
			got, err := autoRouteTargetDatabase(beadsDir, true)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("writable: got (%q, %v), want (%q, error %v)", got, err, tt.want, tt.wantErr)
			}
			got, err = autoRouteTargetDatabase(beadsDir, false)
			if got != tt.want || err != nil {
				t.Errorf("read-only: got (%q, %v), want (%q, nil)", got, err, tt.want)
			}
		})
	}
}
