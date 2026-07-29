package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteFile_Basic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("perm = %o, want 0644", perm)
	}
}

func TestWriteFile_Overwrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(path, []byte("replaced"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replaced" {
		t.Errorf("got %q, want %q", got, "replaced")
	}
}

func TestCreate_Close(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "streamed.txt")

	w, err := Create(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	for _, chunk := range []string{"line1\n", "line2\n", "line3\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "line1\nline2\nline3\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCreate_Abort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "aborted.txt")

	w, err := Create(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("should not appear")); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected target to not exist after Abort, got err=%v", err)
	}
}

func TestCreate_Abort_PreservesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")

	if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := Create(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("overwrite attempt")); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me" {
		t.Errorf("original content clobbered: got %q, want %q", got, "keep me")
	}
}

func TestWriteFile_TempCleanup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.txt")

	if err := WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".~") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFile_SameDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "target.txt")

	// Create and immediately abort so we can inspect the temp file location.
	w, err := Create(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Dir(w.f.Name())
	_ = w.Abort()

	if tmpDir != sub {
		t.Errorf("temp file in %q, want %q (same directory as target)", tmpDir, sub)
	}
}

func TestConcurrentWriters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.txt")

	const numWriters = 20
	const dataSize = 4096

	var wg sync.WaitGroup
	wg.Add(numWriters)

	for i := 0; i < numWriters; i++ {
		go func(id int) {
			defer wg.Done()
			// Each writer writes a distinct byte repeated dataSize times.
			data := make([]byte, dataSize)
			for j := range data {
				data[j] = byte('A' + id%26)
			}
			// Errors from concurrent rename races are acceptable;
			// the point is the final file must be valid.
			_ = WriteFile(path, data, 0o644)
		}(i)
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != dataSize {
		t.Fatalf("file size = %d, want %d", len(got), dataSize)
	}

	// Every byte must be the same character — no interleaving from
	// different writers.
	first := got[0]
	for i, b := range got {
		if b != first {
			t.Fatalf("corruption at byte %d: got %c, expected %c (consistent single-writer content)", i, b, first)
		}
	}
}

func TestConcurrentWriters_NoCorruption(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nocorrupt.jsonl")

	const numWriters = 20

	// Simulate JSONL export: each writer writes multiple lines.
	var wg sync.WaitGroup
	wg.Add(numWriters)

	for i := 0; i < numWriters; i++ {
		go func(id int) {
			defer wg.Done()
			w, err := Create(path, 0o644)
			if err != nil {
				return // concurrent temp file creation can race; ok
			}
			for line := 0; line < 10; line++ {
				data := []byte(strings.Repeat(string(rune('A'+id%26)), 80) + "\n")
				if _, err := w.Write(data); err != nil {
					_ = w.Abort()
					return
				}
			}
			// Close may fail if another writer renamed over us; that's fine.
			if err := w.Close(); err != nil {
				return
			}
		}(i)
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("expected 10 lines, got %d", len(lines))
	}

	// All lines must contain the same character — proving the file came
	// from a single writer, not interleaved from multiple.
	firstChar := lines[0][0]
	for i, line := range lines {
		if len(line) != 80 {
			t.Fatalf("line %d length = %d, want 80", i, len(line))
		}
		for j, b := range []byte(line) {
			if b != firstChar {
				t.Fatalf("line %d byte %d: got %c, expected %c (interleaved writers)", i, j, b, firstChar)
			}
		}
	}
}
