//go:build cgo

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/kvkeys"
)

// embeddedScreenFixture writes the standard mixed plan (valid ce-1, invalid
// status ce-2, one memory) into a scratch directory and returns the JSONL
// path. Every embedded screen test builds on it (GH#4492).
func embeddedScreenFixture(t *testing.T) (jsonlPath string) {
	t.Helper()
	return writeScreenJSONL(t, t.TempDir(), "mixed.jsonl", screenFixtureLines)
}

// TestEmbeddedImportStrictRefusalNamesInvalidRecords pins GH#4492 AC-1 on
// the classic explicit import: a mixed plan is refused BEFORE any
// import-phase write — no issue, no memory, no prefix seed — with every
// rejected row's source line, id and reason, and the remedy.
func TestEmbeddedImportStrictRefusalNamesInvalidRecords(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ce")
	jsonlPath := embeddedScreenFixture(t)

	out, code := bdRunFailCode(t, bd, dir, "import", jsonlPath)
	if code == 0 {
		t.Fatalf("strict import of a mixed plan must exit nonzero; output:\n%s", out)
	}
	for _, want := range []string{
		"line 2",
		"ce-2",
		"invalid status: verify",
		"nothing was imported by this import phase",
		"--skip-invalid",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("strict refusal output = %q, want it to contain %q", out, want)
		}
	}

	store := openStore(t, beadsDir, "ce")
	defer store.Close()
	ctx := context.Background()
	if _, err := store.GetIssue(ctx, "ce-1"); err == nil {
		t.Error("strict refusal imported ce-1; nothing may be written by a refused import")
	}
	if mem, err := store.GetConfig(ctx, kvkeys.MemoryConfigKeyPrefix+"hint"); err != nil || mem != "" {
		t.Errorf("strict refusal wrote the memory record (got %q, err %v); nothing may be written", mem, err)
	}
}
