package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// flipToFlatfile is the exact mutation the forward migration applies.
func flipToFlatfile(meta map[string]interface{}) {
	meta["backend"] = "flatfile"
	delete(meta, "dolt_mode")
	delete(meta, "dolt_database")
	if meta["project_id"] == nil {
		meta["project_id"] = "generated-should-not-happen"
	}
}

// TestMigrateFlatfileMetadataPreserved reproduces TASKS-mm6l: the forward
// migration discarded the metadata.json read error and blank-slated the map
// on invalid JSON, then wrote a fresh file containing only backend + a
// REGENERATED project_id — silently dropping the original project identity
// and every other configfile key while printing success. Read and parse
// failures must abort with the original file untouched; a successful flip
// must carry every pre-existing key through.
func TestMigrateFlatfileMetadataPreserved(t *testing.T) {
	t.Parallel()

	t.Run("valid_metadata_keeps_all_keys", func(t *testing.T) {
		dir := t.TempDir()
		orig := map[string]interface{}{
			"backend":                  "dolt",
			"dolt_mode":                "embedded",
			"dolt_database":            "beads",
			"project_id":               "proj-original-identity",
			"deletions_retention_days": float64(30),
			"global_dolt_database":     "gdb",
			"custom_key":               "custom_value",
		}
		data, err := json.Marshal(orig)
		if err != nil {
			t.Fatal(err)
		}
		metaPath := filepath.Join(dir, "metadata.json")
		if err := os.WriteFile(metaPath, data, 0o644); err != nil {
			t.Fatal(err)
		}

		if err := mutateMetadataFile(dir, flipToFlatfile); err != nil {
			t.Fatalf("mutateMetadataFile: %v", err)
		}

		out, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("rewritten metadata.json is invalid: %v", err)
		}
		if got["backend"] != "flatfile" {
			t.Errorf("backend = %v, want flatfile", got["backend"])
		}
		if got["project_id"] != "proj-original-identity" {
			t.Errorf("project_id = %v — original identity lost", got["project_id"])
		}
		for _, key := range []string{"deletions_retention_days", "global_dolt_database", "custom_key"} {
			if got[key] != orig[key] {
				t.Errorf("key %s = %v, want %v (dropped by rewrite)", key, got[key], orig[key])
			}
		}
		for _, key := range []string{"dolt_mode", "dolt_database"} {
			if _, ok := got[key]; ok {
				t.Errorf("key %s should have been deleted by the flip", key)
			}
		}
	})

	t.Run("invalid_json_aborts_and_leaves_file_untouched", func(t *testing.T) {
		dir := t.TempDir()
		corrupt := []byte(`{"backend": "dolt", "project_id": "proj-x"`) // truncated
		metaPath := filepath.Join(dir, "metadata.json")
		if err := os.WriteFile(metaPath, corrupt, 0o644); err != nil {
			t.Fatal(err)
		}

		if err := mutateMetadataFile(dir, flipToFlatfile); err == nil {
			t.Fatal("mutateMetadataFile blank-slated a corrupt metadata.json instead of failing")
		}
		got, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(corrupt) {
			t.Errorf("metadata.json was rewritten despite the parse failure:\n%s", got)
		}
	})

	t.Run("missing_file_aborts", func(t *testing.T) {
		dir := t.TempDir()
		if err := mutateMetadataFile(dir, flipToFlatfile); err == nil {
			t.Fatal("mutateMetadataFile fabricated a metadata.json from nothing instead of failing")
		}
		if _, err := os.Stat(filepath.Join(dir, "metadata.json")); !os.IsNotExist(err) {
			t.Errorf("metadata.json was created despite the read failure (stat err = %v)", err)
		}
	})
}
