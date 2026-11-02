package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNoFollowRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	ops := map[string]func(string) error{
		"open": func(path string) error {
			f, err := OpenNoFollow(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
			if f != nil {
				_ = f.Close()
			}
			return err
		},
		"create": func(path string) error {
			w, err := CreateNoFollow(path, 0o600)
			if w != nil {
				if _, werr := w.Write([]byte("replacement")); werr != nil {
					t.Fatal(werr)
				}
				return w.Close()
			}
			return err
		},
		"write": func(path string) error { return WriteFileNoFollow(path, []byte("replacement"), 0o600) },
	}
	for name, op := range ops {
		for _, dangling := range []bool{false, true} {
			sub := name + "/live"
			if dangling {
				sub = name + "/dangling"
			}
			t.Run(sub, func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "sentinel")
				if !dangling {
					if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(dir, "link")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}

				err := op(path)
				if err == nil {
					t.Fatal("accepted symlink")
				}
				if !errors.Is(err, ErrSymlink) {
					t.Errorf("error %v does not wrap ErrSymlink", err)
				}
				if !strings.Contains(err.Error(), path) {
					t.Errorf("error %q does not name %s", err, path)
				}

				data, readErr := os.ReadFile(target)
				if dangling {
					if !os.IsNotExist(readErr) {
						t.Errorf("created dangling target: %q, %v", data, readErr)
					}
				} else if readErr != nil || string(data) != "sentinel" {
					t.Errorf("target changed: %q, %v", data, readErr)
				}
				if info, lerr := os.Lstat(path); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Errorf("link replaced: %v, %v", info, lerr)
				}
				assertNoTempFiles(t, dir)
			})
		}
	}
}

func TestNoFollowRegularFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")

	// Absent target: the atomic writer creates it.
	w, err := CreateNoFollow(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	assertContents(t, path, "old")
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o644) {
		t.Errorf("perm: %v, %v", info, err)
	}

	// Regular target: Abort leaves it alone, Close replaces it.
	w, err = CreateNoFollow(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("discarded")); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	assertContents(t, path, "old")

	for _, data := range []string{"new", ""} {
		if err := WriteFileNoFollow(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		assertContents(t, path, data)
	}

	// OpenNoFollow keeps os.OpenFile semantics for regular and absent files.
	f, err := OpenNoFollow(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tail"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	assertContents(t, path, "tail")
	if f, err := OpenNoFollow(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		t.Error("O_EXCL open replaced an existing file")
	} else if !os.IsExist(err) {
		t.Errorf("O_EXCL error = %v, want exists", err)
	}
	fresh := filepath.Join(dir, "fresh")
	f, err = OpenNoFollow(fresh, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if f, err := OpenNoFollow(filepath.Join(dir, "absent"), os.O_RDONLY, 0); err == nil {
		_ = f.Close()
		t.Error("opened an absent file without O_CREATE")
	} else if !os.IsNotExist(err) {
		t.Errorf("absent open error = %v, want not-exist", err)
	}

	// Errors other than absence propagate.
	missing := filepath.Join(dir, "missing", "file")
	if err := WriteFileNoFollow(missing, nil, 0o600); err == nil {
		t.Error("write accepted missing parent")
	}
	if w, err := CreateNoFollow(missing, 0o600); err == nil {
		_ = w.Abort()
		t.Error("create accepted missing parent")
	}
	if err := WriteFileNoFollow(dir, nil, 0o600); err == nil {
		t.Error("write replaced a directory")
	}
	assertNoTempFiles(t, dir)
}

// TestOpenNoFollowRefusesSymlinkSwappedInAfterLstat pins the kernel side of
// OpenNoFollow: a symlink swapped into the gap between the Lstat refusal and
// os.OpenFile must still be refused by O_NOFOLLOW — the Lstat pre-check
// alone cannot cover it. Mirrors the caFileReadHookForTest swap pattern in
// internal/httpclient.
func TestOpenNoFollowRefusesSymlinkSwappedInAfterLstat(t *testing.T) {
	if noFollowFlag == 0 {
		t.Skip("no O_NOFOLLOW on this platform; the Lstat check is the only layer")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	if err := os.WriteFile(path, []byte("real"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}

	restore := openNoFollowSwapHookForTest
	t.Cleanup(func() { openNoFollowSwapHookForTest = restore })
	openNoFollowSwapHookForTest = func(real string) {
		if err := os.Remove(real); err != nil {
			t.Fatalf("remove checked path before symlink swap: %v", err)
		}
		if err := os.Symlink(elsewhere, real); err != nil {
			t.Fatalf("symlink swap-in over checked path: %v", err)
		}
	}

	f, err := OpenNoFollow(path, os.O_RDWR|os.O_TRUNC, 0)
	if err == nil {
		_ = f.Close()
		t.Fatal("OpenNoFollow opened a symlink swapped in after the Lstat check")
	}
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("OpenNoFollow = %v, want ErrSymlink", err)
	}
	assertContents(t, elsewhere, "target")
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
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
