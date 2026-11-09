//go:build cgo

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"path/filepath"
)

// TestEmbeddedImportDryRunMatchesScreenSemantics pins GH#4492 AC-7: dry-run
// classifies with the same strict/skip semantics as a real import while
// writing nothing — the database stays empty and no rejects file appears.
func TestEmbeddedImportDryRunMatchesScreenSemantics(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)

	t.Run("strict dry-run refuses", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ce")
		jsonlPath := embeddedScreenFixture(t)
		out, code := bdRunFailCode(t, bd, dir, "import", "--dry-run", jsonlPath)
		if code == 0 {
			t.Fatalf("strict dry-run of a mixed plan must exit nonzero; output:\n%s", out)
		}
		if !strings.Contains(out, "nothing was imported by this import phase") {
			t.Errorf("strict dry-run output = %q, want the refusal", out)
		}
		store := openStore(t, beadsDir, "ce")
		defer store.Close()
		ctx := context.Background()
		if _, err := store.GetIssue(ctx, "ce-1"); err == nil {
			t.Error("strict dry-run wrote ce-1 to the database")
		}
	})

	t.Run("skip dry-run classifies without writes", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ce")
		jsonlPath := embeddedScreenFixture(t)
		rejectsPath := filepath.Join(filepath.Dir(jsonlPath), "rejects.jsonl")

		out := bdRunOK(t, bd, dir, "import", "--dry-run", "--skip-invalid", "--rejects", rejectsPath, jsonlPath)
		for _, want := range []string{"Would import 1 issues", "(1 invalid records skipped)"} {
			if !strings.Contains(out, want) {
				t.Errorf("skip dry-run output = %q, want it to contain %q", out, want)
			}
		}
		store := openStore(t, beadsDir, "ce")
		defer store.Close()
		ctx := context.Background()
		if _, err := store.GetIssue(ctx, "ce-1"); err == nil {
			t.Error("skip dry-run wrote ce-1 to the database")
		}
		if _, statErr := os.Stat(rejectsPath); !os.IsNotExist(statErr) {
			t.Errorf("skip dry-run wrote a rejects file (%v), want no writes", statErr)
		}
	})
}

// TestEmbeddedImportUndecodableLineReportsLocation pins GH#4492 AC-7's
// undecodable-line rule on the explicit path: a line that cannot decode is
// fatal with its physical location, reported before any import-phase write.
func TestEmbeddedImportUndecodableLineReportsLocation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ce")
	jsonlPath := writeScreenJSONL(t, t.TempDir(), "mixed.jsonl", []string{
		`{"_type":"issue","id":"ce-1","title":"valid row","status":"open","priority":2,"issue_type":"task"}`,
		`{"_type":"issue","id":"ce-2","title":"also valid","status":"open","priority":2,"issue_type":"task"}`,
		`{"_type":"issue","id": not json`,
	})

	out, _ := bdRunFailCode(t, bd, dir, "import", jsonlPath)
	if !strings.Contains(out, "line 3") {
		t.Errorf("undecodable-line import output = %q, want the physical location \"line 3\"", out)
	}
	store := openStore(t, beadsDir, "ce")
	defer store.Close()
	ctx := context.Background()
	if _, err := store.GetIssue(ctx, "ce-1"); err == nil {
		t.Error("undecodable line at line 3 still imported ce-1; nothing may be written")
	}
}
