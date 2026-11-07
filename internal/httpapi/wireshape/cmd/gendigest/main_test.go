package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/wireshape"
)

func sampleDigest(revision int) wireshape.Digest {
	return wireshape.Digest{
		WireRevision: revision,
		Entries: []wireshape.Entry{
			{Schema: "Widget", Member: "name", Type: "string", Required: true},
		},
	}
}

// TestWriteGoldenRequiresInitWhenMissing is LOW-2 from the re-review: a
// missing golden.json must not be filled in silently. Without -init, the one
// failure mode that most needs a loud error — pointing this command at a path
// that lost its file by accident — would otherwise look exactly like a
// successful first run.
func TestWriteGoldenRequiresInitWhenMissing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "golden.json")

	if err := writeGolden(out, sampleDigest(2), false); err == nil {
		t.Fatal("writeGolden with no existing file and initFlag=false: want an error naming -init, got nil")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("writeGolden refused but a file exists at %s anyway", out)
	}
}

// TestWriteGoldenInitCreatesWhenMissing is the other half: -init is how a
// real first run (or a deliberate reset) still works.
func TestWriteGoldenInitCreatesWhenMissing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "golden.json")

	if err := writeGolden(out, sampleDigest(2), true); err != nil {
		t.Fatalf("writeGolden with initFlag=true: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("writeGolden -init did not create %s: %v", out, err)
	}
}

// TestWriteGoldenIgnoresInitWhenGoldenExists: -init only matters for the
// missing-file case. An existing golden is still governed exclusively by
// SafeToWrite, whether or not -init was passed.
func TestWriteGoldenIgnoresInitWhenGoldenExists(t *testing.T) {
	out := filepath.Join(t.TempDir(), "golden.json")
	if err := writeGolden(out, sampleDigest(2), true); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// A same-revision changed entry is unsafe per SafeToWrite, -init or not.
	changed := sampleDigest(2)
	changed.Entries[0].Type = "integer"
	if err := writeGolden(out, changed, true); err == nil {
		t.Error("writeGolden over an existing golden with an unsafe diff and initFlag=true: want an error, got nil")
	}

	// A higher-revision changed entry is safe, with no -init needed.
	bumped := sampleDigest(3)
	bumped.Entries[0].Type = "integer"
	if err := writeGolden(out, bumped, false); err != nil {
		t.Errorf("writeGolden over an existing golden with a revision-bumped diff and initFlag=false: %v", err)
	}
}
