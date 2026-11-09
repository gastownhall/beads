package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestImportRejectsFileMergesAndRefusesUnsafeDestinations pins the rejects
// file both `--skip-invalid --rejects` and the recovery quarantine write
// (GH#4492, AC-6): earlier rejected records survive a later save, an exact
// repeat is not duplicated, and a destination that would lose data — a
// symlink, a directory, the source itself or a hardlink to it — is refused
// before anything is imported.
func TestImportRejectsFileMergesAndRefusesUnsafeDestinations(t *testing.T) {
	r1 := importRejection{Line: 3, ID: "rq-1", Raw: []byte(`{"id":"rq-1","status":"verify","x_unknown":1}`)}
	r2 := importRejection{Line: 4, ID: "rq-2", Raw: []byte(`{"id":"rq-2","status":"verify"}`)}

	// save runs the explicit --skip-invalid --rejects path.
	save := func(source, rejects string, rejected ...importRejection) error {
		var result importResultJSON
		return applyImportRejections(&result, source, rejected, true, rejects)
	}
	newSource := func(t *testing.T, dir string) string {
		t.Helper()
		source := filepath.Join(dir, "issues.jsonl")
		if err := os.WriteFile(source, []byte("source data\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return source
	}
	assertFile := func(t *testing.T, path, want string) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Errorf("%s =\n%q\nwant\n%q", filepath.Base(path), got, want)
		}
	}

	t.Run("merges_with_earlier_records_and_dedupes_exact_lines", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		// An earlier quarantine whose last line has no newline.
		if err := os.WriteFile(rejects, []byte("old-1\nold-2"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := save(source, rejects, r1); err != nil {
			t.Fatalf("first save: %v", err)
		}
		if err := save(source, rejects, r1, r2); err != nil {
			t.Fatalf("second save: %v", err)
		}
		assertFile(t, rejects, "old-1\nold-2\n"+string(r1.Raw)+"\n"+string(r2.Raw)+"\n")
	})

	refused := func(t *testing.T, source, rejects string) {
		t.Helper()
		if err := save(source, rejects, r1); err == nil {
			t.Fatalf("saving the rejects to %s succeeded, want a refusal", rejects)
		}
		assertFile(t, source, "source data\n")
	}

	t.Run("refuses_a_symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on Windows")
		}
		dir := t.TempDir()
		source := newSource(t, dir)
		outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
		if err := os.WriteFile(outside, []byte("elsewhere\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.Symlink(outside, rejects); err != nil {
			t.Fatal(err)
		}
		refused(t, source, rejects)
		if info, err := os.Lstat(rejects); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink replaced (lstat %v, err %v)", info, err)
		}
		assertFile(t, outside, "elsewhere\n")
	})

	t.Run("refuses_a_directory", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.Mkdir(rejects, 0o750); err != nil {
			t.Fatal(err)
		}
		refused(t, source, rejects)
	})

	t.Run("refuses_the_source", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		refused(t, source, source)
	})

	t.Run("refuses_a_hardlink_to_the_source", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.Link(source, rejects); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		refused(t, source, rejects)
		if same, err := sameFileForTest(source, rejects); err != nil || !same {
			t.Errorf("hardlink to the source was replaced (same=%v, err %v)", same, err)
		}
	})

	t.Run("failed_replacement_keeps_the_old_file", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a POSIX directory the test cannot write")
		}
		dir := t.TempDir()
		source := newSource(t, t.TempDir())
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.WriteFile(rejects, []byte("old-1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })
		if err := save(source, rejects, r1); err == nil {
			t.Fatal("save into a read-only directory succeeded")
		}
		assertFile(t, rejects, "old-1\n")
	})
}

func sameFileForTest(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}
