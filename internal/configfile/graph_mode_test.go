package configfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGraphModeDiscoveryAndRoundTrip(t *testing.T) {
	for _, filename := range []string{ConfigFileName, "config.json"} {
		for _, mode := range []string{"", GraphModeDependency, GraphModeLink, "future-format"} {
			t.Run(filename+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				input := `{"backend":"dolt","graph_mode":"` + mode + `"}`
				path := filepath.Join(dir, filename)
				if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg, err := LoadForDiscovery(dir)
				if err != nil {
					t.Fatal(err)
				}
				got, err := cfg.GetGraphMode()
				if mode == "future-format" {
					if err == nil || got != "" {
						t.Fatalf("unknown marker resolved to %q, %v", got, err)
					}
				} else {
					want := mode
					if want == "" {
						want = GraphModeDependency
					}
					if err != nil || got != want {
						t.Fatalf("mode = %q, %v; want %q", got, err, want)
					}
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 {
					t.Fatalf("discovery changed directory: %v, %v", entries, err)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != input {
					t.Fatalf("discovery changed metadata: %q, %v", after, err)
				}
				// Saving otherwise unrelated config must retain the marker,
				// including one this client does not understand.
				if err := cfg.Save(dir); err != nil {
					t.Fatal(err)
				}
				loaded, err := LoadForDiscovery(dir)
				if err != nil || loaded == nil || loaded.GraphMode != mode {
					t.Fatalf("marker lost during save: %#v, %v", loaded, err)
				}
			})
		}
	}
}

func TestGraphModeAbsentConfigPreservesLegacyDefault(t *testing.T) {
	cfg, err := LoadForDiscovery(t.TempDir())
	if err != nil || cfg != nil {
		t.Fatalf("absent config = %#v, %v", cfg, err)
	}
	mode, err := cfg.GetGraphMode()
	if err != nil || mode != GraphModeDependency {
		t.Fatalf("absent config mode = %q, %v", mode, err)
	}
}

// graphWorkspaceMetadata is the metadata.json shape a graph_mode link
// workspace carries: the marker, its binding, and ordinary Dolt settings.
const graphWorkspaceMetadata = `{
  "database": "",
  "backend": "dolt",
  "graph_mode": "link",
  "graph_scope_url": "https://example.test/scope",
  "graph_authority_id": "0123456789abcdef0123456789abcdef",
  "graph_workspace": "/work/repo/.beads",
  "graph_schema_version": 7,
  "graph_ready": true,
  "dolt_mode": "embedded",
  "dolt_database": "beads_graph_0123456789abcdef0123456789abcdef",
  "project_id": "0123456789abcdef0123456789abcdef"
}`

// graphKeys returns every graph_* key of a metadata document with its value.
func graphKeys(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse metadata %q: %v", data, err)
	}
	keys := map[string]any{}
	for key, value := range doc {
		if strings.HasPrefix(key, "graph_") {
			keys[key] = value
		}
	}
	return keys
}

// A bd that does not open graph workspaces still rewrites metadata on paths
// that change unrelated settings, and Load itself rewrites a legacy
// config.json as metadata.json. Neither may strip the graph marker or its
// binding: a workspace that lost them would read as an ordinary Issue store.
func TestGraphWorkspaceBindingSurvivesSave(t *testing.T) {
	want := graphKeys(t, []byte(graphWorkspaceMetadata))
	if len(want) != 6 {
		t.Fatalf("fixture carries %d graph_* keys, want 6: %v", len(want), want)
	}

	t.Run("metadata.json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(ConfigPath(dir), []byte(graphWorkspaceMetadata), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(dir)
		if err != nil || cfg == nil {
			t.Fatalf("Load = %#v, %v", cfg, err)
		}
		cfg.StaleClosedIssuesDays = 30
		if err := cfg.Save(dir); err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(ConfigPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if got := graphKeys(t, saved); !reflect.DeepEqual(got, want) {
			t.Fatalf("Save changed the graph binding:\n got %v\nwant %v\nsaved:\n%s", got, want, saved)
		}
	})

	t.Run("legacy config.json migration", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, "config.json")
		if err := os.WriteFile(legacy, []byte(graphWorkspaceMetadata), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err != nil {
			t.Fatal(err)
		}
		migrated, err := os.ReadFile(ConfigPath(dir))
		if err != nil {
			t.Fatalf("Load did not migrate config.json: %v", err)
		}
		if got := graphKeys(t, migrated); !reflect.DeepEqual(got, want) {
			t.Fatalf("config.json migration changed the graph binding:\n got %v\nwant %v\nmigrated:\n%s", got, want, migrated)
		}
	})
}

// The marker and binding are omitempty, so metadata.json written for an
// ordinary workspace is byte-for-byte what it was before they existed.
func TestGraphFieldsOmittedFromOrdinaryMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := DefaultConfig().Save(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(ConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := graphKeys(t, data); len(got) != 0 {
		t.Fatalf("ordinary metadata.json gained graph keys %v:\n%s", got, data)
	}
}
