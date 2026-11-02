package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// requireSymlinkSupport skips the test on platforms where creating symlinks
// needs privileges the test user does not have.
func requireSymlinkSupport(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
}

// victimWithLink sets up a victim file plus a leaf symlink pointing at it and
// returns (victim path, symlink path). A dangling variant is set up by callers
// that point the link at a path that does not exist.
func victimWithLink(t *testing.T, dir, linkName string) (victim, link string) {
	t.Helper()
	victim = filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(dir, linkName)
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	return victim, link
}

func assertVictimUntouched(t *testing.T, victim string) {
	t.Helper()
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original\n" {
		t.Errorf("write went through the symlink: victim now %q", got)
	}
}

func assertSymlinkRefusal(t *testing.T, err error, link string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	var symErr *ErrSymlink
	if !errors.As(err, &symErr) {
		t.Fatalf("expected *ErrSymlink, got %T: %v", err, err)
	}
	if symErr.Path != link {
		t.Errorf("error names %q, want %q", symErr.Path, link)
	}
}

// TestNoFollowRejectsSymlinks: a live or dangling leaf symlink is refused by
// every NoFollow entry point, with an error naming the path, and the link's
// target is never written (bd-1tmym4 / #7888).
func TestNoFollowRejectsSymlinks(t *testing.T) {
	requireSymlinkSupport(t)

	t.Run("WriteFileNoFollow live link", func(t *testing.T) {
		dir := t.TempDir()
		victim, link := victimWithLink(t, dir, "export.jsonl")
		err := WriteFileNoFollow(link, []byte("evil\n"), 0o644)
		assertSymlinkRefusal(t, err, link)
		assertVictimUntouched(t, victim)
	})

	t.Run("WriteFileNoFollow dangling link", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "export.jsonl")
		if err := os.Symlink(filepath.Join(dir, "no-such-target"), link); err != nil {
			t.Fatal(err)
		}
		err := WriteFileNoFollow(link, []byte("evil\n"), 0o644)
		assertSymlinkRefusal(t, err, link)
		// The dangling link itself must survive, not be replaced by a file.
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dangling link was replaced: %v %v", fi, err)
		}
	})

	t.Run("CreateNoFollow live link", func(t *testing.T) {
		dir := t.TempDir()
		victim, link := victimWithLink(t, dir, "backup.jsonl")
		w, err := CreateNoFollow(link, 0o644)
		if err == nil {
			_ = w.Abort()
			t.Fatal("expected a refusal, got nil")
		}
		assertSymlinkRefusal(t, err, link)
		assertVictimUntouched(t, victim)
	})

	t.Run("CreateNoFollow dangling link", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "backup.jsonl")
		if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
			t.Fatal(err)
		}
		w, err := CreateNoFollow(link, 0o644)
		if err == nil {
			_ = w.Abort()
			t.Fatal("expected a refusal, got nil")
		}
		assertSymlinkRefusal(t, err, link)
	})

	t.Run("OpenNoFollow live link", func(t *testing.T) {
		dir := t.TempDir()
		victim, link := victimWithLink(t, dir, "lock")
		f, err := OpenNoFollow(link, os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil {
			_ = f.Close()
			t.Fatal("expected a refusal, got nil")
		}
		assertSymlinkRefusal(t, err, link)
		assertVictimUntouched(t, victim)
	})

	t.Run("OpenNoFollow dangling link", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "lock")
		if err := os.Symlink(filepath.Join(dir, "no-such-target"), link); err != nil {
			t.Fatal(err)
		}
		f, err := OpenNoFollow(link, os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil {
			_ = f.Close()
			t.Fatal("expected a refusal, got nil")
		}
		assertSymlinkRefusal(t, err, link)
	})
}

// TestNoFollowRegularFiles: regular and absent targets keep the behavior of
// the corresponding existing APIs (bd-1tmym4 / #7888).
func TestNoFollowRegularFiles(t *testing.T) {
	dir := t.TempDir()

	t.Run("WriteFileNoFollow absent target creates", func(t *testing.T) {
		path := filepath.Join(dir, "fresh.jsonl")
		if err := WriteFileNoFollow(path, []byte("data\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "data\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("WriteFileNoFollow regular target replaces", func(t *testing.T) {
		path := filepath.Join(dir, "replace.jsonl")
		if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := WriteFileNoFollow(path, []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "new\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("CreateNoFollow streams and closes", func(t *testing.T) {
		path := filepath.Join(dir, "stream.jsonl")
		w, err := CreateNoFollow(path, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("line1\n")); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "line1\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("CreateNoFollow abort leaves target untouched", func(t *testing.T) {
		path := filepath.Join(dir, "abort.jsonl")
		if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w, err := CreateNoFollow(path, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Abort(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "keep\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("OpenNoFollow regular file opens writable", func(t *testing.T) {
		path := filepath.Join(dir, "existing.lock")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := OpenNoFollow(path, os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("y"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "y" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("OpenNoFollow absent target creates", func(t *testing.T) {
		path := filepath.Join(dir, "fresh.lock")
		f, err := OpenNoFollow(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("locked\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "locked\n" {
			t.Errorf("got %q", got)
		}
	})
}