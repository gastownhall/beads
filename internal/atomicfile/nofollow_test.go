package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoFollowRejectsSymlinks(t *testing.T) {
	for name, op := range map[string]func(string) error{
		"create": func(path string) error {
			f, err := CreateNoFollow(path, 0600)
			if f != nil {
				_ = f.Close()
			}
			return err
		},
		"open": func(path string) error {
			f, err := OpenNoFollow(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
			if f != nil {
				_ = f.Close()
			}
			return err
		},
		"write": func(path string) error { return WriteFileNoFollow(path, []byte("replacement"), 0600) },
	} {
		t.Run(name, func(t *testing.T) {
			for _, dangling := range []bool{false, true} {
				dir := t.TempDir()
				target := filepath.Join(dir, "sentinel")
				if !dangling {
					if err := os.WriteFile(target, []byte("sentinel"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(dir, "link")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				if err := op(path); err == nil {
					t.Error("accepted symlink")
				}
				data, err := os.ReadFile(target)
				if dangling {
					if !os.IsNotExist(err) {
						t.Errorf("created dangling target: %q, %v", data, err)
					}
				} else if err != nil || string(data) != "sentinel" {
					t.Errorf("sentinel changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestNoFollowRegularFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	f, err := CreateNoFollow(path, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("old"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if f, err := CreateNoFollow(path, 0600); err == nil {
		_ = f.Close()
		t.Fatal("exclusive create replaced file")
	}
	f, err = OpenNoFollow(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for _, data := range [][]byte{[]byte("new"), nil} {
		if err := WriteFileNoFollow(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(data) {
			t.Fatalf("contents: %q, %v", got, err)
		}
	}
	missing := filepath.Join(dir, "missing", "file")
	if err := WriteFileNoFollow(missing, nil, 0600); err == nil {
		t.Error("write accepted missing parent")
	}
	if f, err := OpenNoFollow(missing, os.O_RDWR, 0600); err == nil {
		_ = f.Close()
		t.Error("open accepted missing parent")
	}
	if f, err := CreateNoFollow(missing, 0600); err == nil {
		_ = f.Close()
		t.Error("create accepted missing parent")
	}
	if err := WriteFileNoFollow(dir, nil, 0600); err == nil {
		t.Error("write replaced directory")
	}
}
