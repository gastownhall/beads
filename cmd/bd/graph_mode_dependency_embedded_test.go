//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readGraphKeys returns the graph_* keys of a metadata.json and their values.
func readGraphKeys(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	keys := map[string]any{}
	for key, value := range doc {
		if strings.HasPrefix(key, "graph_") {
			keys[key] = value
		}
	}
	return keys
}

// The graph-mode fence must not change ordinary workspaces: one without the
// marker, and one whose metadata names the default "dependency" model
// explicitly, open and write as before, and a command that rewrites
// metadata.json keeps the marker exactly as it found it.
func TestEmbeddedGraphModeDependencyWorkspaceUnaffected(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)

	for _, marker := range []string{"absent", "dependency"} {
		t.Run(marker, func(t *testing.T) {
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gm")
			metadataPath := filepath.Join(beadsDir, "metadata.json")
			if marker != "absent" {
				data, err := os.ReadFile(metadataPath)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(data, &doc); err != nil {
					t.Fatal(err)
				}
				doc["graph_mode"] = marker
				data, err = json.MarshalIndent(doc, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			wantKeys := readGraphKeys(t, metadataPath)
			before, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}

			issue := bdCreate(t, bd, dir, "ordinary workspace issue", "--type", "task")
			if out := bdList(t, bd, dir, "--json"); !strings.Contains(out, issue.ID) {
				t.Fatalf("bd list --json does not show %s:\n%s", issue.ID, out)
			}
			if after, err := os.ReadFile(metadataPath); err != nil || string(after) != string(before) {
				t.Fatalf("create/list changed metadata.json:\nbefore:\n%s\nafter:\n%s\n%v", before, after, err)
			}

			// upgrade ack rewrites metadata.json through configfile.Save.
			if out, err := bdRunWithFlockRetry(t, bd, dir, "upgrade", "ack"); err != nil {
				t.Fatalf("bd upgrade ack: %v\n%s", err, out)
			}
			if got := readGraphKeys(t, metadataPath); len(got) != len(wantKeys) || got["graph_mode"] != wantKeys["graph_mode"] {
				data, _ := os.ReadFile(metadataPath)
				t.Fatalf("metadata rewrite changed graph keys: got %v, want %v\n%s", got, wantKeys, data)
			}
		})
	}
}
