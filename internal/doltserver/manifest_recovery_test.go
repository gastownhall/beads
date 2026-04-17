package doltserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogHasCorruptManifestError(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "dolt-server.log")

	// Missing log file is not an error.
	got, err := logHasCorruptManifestError(logPath)
	if err != nil || got {
		t.Fatalf("missing log: got (%v, %v), want (false, nil)", got, err)
	}

	// Log without the signature.
	if err := os.WriteFile(logPath, []byte("starting server\nlistening on :3306\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = logHasCorruptManifestError(logPath)
	if err != nil || got {
		t.Fatalf("clean log: got (%v, %v), want (false, nil)", got, err)
	}

	// Log with the signature.
	content := "starting\n" + strings.Repeat("noise\n", 100) +
		"error: root hash doesn't exist: abc123\nexit\n"
	if err := os.WriteFile(logPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = logHasCorruptManifestError(logPath)
	if err != nil || !got {
		t.Fatalf("corrupt log: got (%v, %v), want (true, nil)", got, err)
	}
}

// writeNomsDir creates a .dolt/noms/ shape under root and returns its path.
// If journalSize >= 0, a 32-char journal file is written with that size.
// If idxSize >= 0, a journal.idx file is written with that size.
// If oldgenChunkSize > 0, a chunk file is created in oldgen/.
func writeNomsDir(t *testing.T, root string, journalSize, idxSize, oldgenChunkSize int64) string {
	t.Helper()
	nomsDir := filepath.Join(root, ".dolt", "noms")
	if err := os.MkdirAll(filepath.Join(nomsDir, "oldgen"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("manifest-stub"), 0600); err != nil {
		t.Fatal(err)
	}
	if idxSize >= 0 {
		writeSized(t, filepath.Join(nomsDir, "journal.idx"), idxSize)
	}
	if journalSize >= 0 {
		name := strings.Repeat("v", 32)
		writeSized(t, filepath.Join(nomsDir, name), journalSize)
	}
	if oldgenChunkSize > 0 {
		writeSized(t, filepath.Join(nomsDir, "oldgen", "chunk1"), oldgenChunkSize)
	}
	return nomsDir
}

func writeSized(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if size > 0 {
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNomsDirLooksCorrupt(t *testing.T) {
	tests := []struct {
		name            string
		journalSize     int64
		idxSize         int64
		oldgenChunkSize int64
		want            bool
	}{
		{"empty journal + empty idx + empty oldgen", 40, 0, 0, true},
		{"no journal file, empty idx, empty oldgen", -1, 0, 0, true},
		{"journal has data", 8192, 0, 0, false},
		{"idx has data", 40, 4096, 0, false},
		{"oldgen has chunk", 40, 0, 2048, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			nomsDir := writeNomsDir(t, dir, tc.journalSize, tc.idxSize, tc.oldgenChunkSize)
			got, err := nomsDirLooksCorrupt(nomsDir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFindCorruptNomsDirs(t *testing.T) {
	root := t.TempDir()
	doltDir := filepath.Join(root, "dolt")

	// Corrupt database
	writeNomsDir(t, filepath.Join(doltDir, "bd"), 40, 0, 0)
	// Healthy database (has oldgen data)
	writeNomsDir(t, filepath.Join(doltDir, "other"), 40, 0, 2048)

	matches, err := findCorruptNomsDirs(doltDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1: %v", len(matches), matches)
	}
	if !strings.Contains(matches[0], filepath.Join("bd", ".dolt", "noms")) {
		t.Errorf("unexpected match: %s", matches[0])
	}
}

func TestRecoverCorruptManifest_NoLogSignatureNoop(t *testing.T) {
	beadsDir := t.TempDir()
	doltDir := filepath.Join(beadsDir, "dolt")
	writeNomsDir(t, filepath.Join(doltDir, "bd"), 40, 0, 0)

	backups, err := recoverCorruptManifest(beadsDir, doltDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("expected no backups without log signature, got %v", backups)
	}
}
