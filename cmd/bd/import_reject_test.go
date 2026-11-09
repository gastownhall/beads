package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// GH#4492: a JSONL import aborted in its entirety on the first record the
// writer refused, and reported nothing useful about which record or why. These
// tests, adapted from @maphew's PR #5202, pin the replacement contract at the
// screen's seams: the abort stays the default, but it names the offending line
// and the way past it.

// Strict-by-default keeps the reporter of GH#4492 in exactly the position they
// started in: plain `bd import` on a file with one bad row still imports
// nothing. The only thing that changes their outcome is the failure telling
// them the escape hatch exists, so that hint is load-bearing, not decoration.
func TestFirstRejectErrorNamesTheEscapeHatch(t *testing.T) {
	err := strictImportError([]importRejection{
		{Line: 2, ID: "tst-2", Reason: "invalid status: verify"},
		{Line: 4, ID: "tst-4", Reason: "title is required"},
	})
	if err == nil {
		t.Fatal("strictImportError = nil, want an error")
	}
	msg := err.Error()
	for _, want := range []string{"line 2", "tst-2", "invalid status: verify", "and 1 more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	for _, want := range []string{"--skip-invalid", "--rejects"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not tell the user about %s; without it the default leaves\n"+
				"the reporter of GH#4492 with the same empty import they filed about:\n%s", want, msg)
		}
	}
	// Nothing partial landed, and the message has to say so: a user who thinks
	// some rows imported will fix the file and re-import, double-applying it.
	if !strings.Contains(msg, "nothing was imported") {
		t.Errorf("error does not say the import was a no-op:\n%s", msg)
	}
}

// --rejects is user-supplied and writeImportRejects renames a temp file into
// place, so a --rejects that names the import source through any alias must be
// refused rather than replacing the source with a quarantine.
func TestRejectPathCollidesWithSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "issues.jsonl")
	if err := os.WriteFile(source, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Direct same path.
	if !sameImportFile(source, source) {
		t.Fatal("sameImportFile = false for the identical path")
	}

	// Relative alias of the same file.
	rel := filepath.Join(dir, "..", filepath.Base(dir), "issues.jsonl")
	if !sameImportFile(rel, source) {
		t.Fatal("sameImportFile = false for a relative alias of the same file")
	}

	// A different file in the same directory does not collide, existing or
	// not yet created.
	other := filepath.Join(dir, "issues.rejected.jsonl")
	if sameImportFile(other, source) {
		t.Fatal("sameImportFile = true for a distinct file that does not exist yet")
	}
	if err := os.WriteFile(other, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if sameImportFile(other, source) {
		t.Fatal("sameImportFile = true for a distinct existing file")
	}

	// The flag check refuses the collision, and never applies to stdin.
	origSkip, origRejects := importSkipInvalid, importRejectsPath
	t.Cleanup(func() { importSkipInvalid, importRejectsPath = origSkip, origRejects })
	importSkipInvalid, importRejectsPath = true, rel
	if err := validateImportRejectsFlag(source, false); err == nil || !strings.Contains(err.Error(), "is the input file") {
		t.Fatalf("validateImportRejectsFlag = %v, want it to refuse the input file", err)
	}
	if err := validateImportRejectsFlag("", true); err != nil {
		t.Fatalf("validateImportRejectsFlag(stdin) = %v, want nil", err)
	}

	// A hard-link alias has a distinct canonical path but the same underlying
	// file, so it must be refused too.
	hardLinkPath := filepath.Join(dir, "hard-link.jsonl")
	if err := os.Link(source, hardLinkPath); err != nil {
		t.Skipf("hard links unsupported in this environment: %v", err)
	}
	if !sameImportFile(hardLinkPath, source) {
		t.Fatal("sameImportFile = false for a hard link to the source")
	}

	// A symlinked reject path that resolves to the source path collides too.
	symlinkPath := filepath.Join(dir, "alias.jsonl")
	if err := os.Symlink(source, symlinkPath); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}
	if !sameImportFile(symlinkPath, source) {
		t.Fatal("sameImportFile = false for a symlink to the source")
	}
}

// TestWriteRejectFileHardening pins the write-path properties of the rejects
// file: a pre-existing symlink at the path is replaced as a link (never
// followed: a recovery's <source>.rejected.jsonl has no collision guard), a
// pre-existing file with a laxer mode is replaced with a fresh 0600 file
// rather than keeping its mode, and a run with nothing to save leaves an
// empty file at the path rather than removing it.
func TestWriteRejectFileHardening(t *testing.T) {
	rejects := []importRejection{{Line: 1, Reason: "r", raw: []byte(`{"id":"x"}`)}}

	t.Run("symlink at quarantine path is replaced, target untouched", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation is a privileged operation on default Windows configurations")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "precious.txt")
		const keep = "do not clobber\n"
		if err := os.WriteFile(target, []byte(keep), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "issues.jsonl.rejected.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		if err := writeImportRejects(link, rejects); err != nil {
			t.Fatalf("writeImportRejects: %v", err)
		}
		if data, rerr := os.ReadFile(target); rerr != nil || string(data) != keep {
			t.Errorf("symlink target changed: content=%q err=%v (quarantine write followed the link)", string(data), rerr)
		}
		fi, lerr := os.Lstat(link)
		if lerr != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("quarantine path is still a symlink (or missing): err=%v", lerr)
		}
		if data, _ := os.ReadFile(link); string(data) != "{\"id\":\"x\"}\n" {
			t.Errorf("quarantine = %q, want the rejected line", data)
		}
	})

	t.Run("pre-existing lax mode is replaced with 0600", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("requires POSIX file permission semantics")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "rejects.jsonl")
		if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeImportRejects(path, rejects); err != nil {
			t.Fatalf("writeImportRejects: %v", err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("quarantine mode = %o, want 0600 (rewrite must not inherit the old lax mode)", fi.Mode().Perm())
		}
	})

	t.Run("nothing to write leaves an empty file, not no file", func(t *testing.T) {
		dir := t.TempDir()
		// A file the user named that holds an earlier run's rejects.
		path := filepath.Join(dir, "notes.jsonl")
		if err := os.WriteFile(path, []byte("an earlier run's rejects\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeImportRejects(path, nil); err != nil {
			t.Fatalf("writeImportRejects: %v", err)
		}
		// The path still exists, so a later import can still write to it, and
		// it no longer holds rejects this run did not produce.
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
			t.Fatalf("path not a regular empty file: stat err=%v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("directory holds %d entries, want only the rejects file (no temp file left behind)", len(entries))
		}
	})
}
