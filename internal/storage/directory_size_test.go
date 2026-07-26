package storage

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMeasureDirectorySize(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "first"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "second"), []byte("world!"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := MeasureDirectorySize(t.Context(), root)
	if err != nil {
		t.Fatalf("MeasureDirectorySize: %v", err)
	}
	if got != 11 {
		t.Fatalf("MeasureDirectorySize = %d, want 11", got)
	}
}

func TestMeasureDirectorySizeEmptyDirectory(t *testing.T) {
	t.Parallel()

	got, err := MeasureDirectorySize(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("MeasureDirectorySize: %v", err)
	}
	if got != 0 {
		t.Fatalf("MeasureDirectorySize = %d, want 0", got)
	}
}

func TestMeasureDirectorySizeRejectsMissingRoot(t *testing.T) {
	t.Parallel()

	_, err := MeasureDirectorySize(t.Context(), filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MeasureDirectorySize error = %v, want fs.ErrNotExist", err)
	}
}

func TestMeasureDirectorySizeSkipsVanishedDescendant(t *testing.T) {
	t.Parallel()

	root := filepath.Clean("/root")
	walk := func(_ string, visit filepath.WalkFunc) error {
		if err := visit(root, fakeFileInfo{name: "root", dir: true}, nil); err != nil {
			return err
		}
		if err := visit(filepath.Join(root, "vanished"), nil, fs.ErrNotExist); err != nil {
			return err
		}
		return visit(filepath.Join(root, "present"), fakeFileInfo{name: "present", size: 7}, nil)
	}

	got, err := measureDirectorySizeWithWalk(t.Context(), root, walk)
	if err != nil {
		t.Fatalf("measureDirectorySizeWithWalk: %v", err)
	}
	if got != 7 {
		t.Fatalf("measureDirectorySizeWithWalk = %d, want 7", got)
	}
}

func TestMeasureDirectorySizeDoesNotSuppressRootOrOtherWalkErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("permission denied")
	tests := []struct {
		name string
		path string
		err  error
	}{
		{name: "missing root", path: filepath.Clean("/root"), err: fs.ErrNotExist},
		{name: "descendant permission failure", path: filepath.Clean("/root/child"), err: sentinel},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Clean("/root")
			walk := func(_ string, visit filepath.WalkFunc) error {
				return visit(tc.path, nil, tc.err)
			}
			_, err := measureDirectorySizeWithWalk(t.Context(), root, walk)
			if !errors.Is(err, tc.err) {
				t.Fatalf("measureDirectorySizeWithWalk error = %v, want %v", err, tc.err)
			}
		})
	}
}

func TestMeasureDirectorySizeHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := filepath.Clean("/root")
	walk := func(_ string, visit filepath.WalkFunc) error {
		return visit(root, fakeFileInfo{name: "root", dir: true}, nil)
	}
	_, err := measureDirectorySizeWithWalk(ctx, root, walk)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("measureDirectorySizeWithWalk error = %v, want context.Canceled", err)
	}
}

func TestMeasureDirectorySizeRejectsOverflow(t *testing.T) {
	t.Parallel()

	root := filepath.Clean("/root")
	walk := func(_ string, visit filepath.WalkFunc) error {
		if err := visit(root, fakeFileInfo{name: "root", dir: true}, nil); err != nil {
			return err
		}
		if err := visit(filepath.Join(root, "large"), fakeFileInfo{name: "large", size: math.MaxInt64}, nil); err != nil {
			return err
		}
		return visit(filepath.Join(root, "overflow"), fakeFileInfo{name: "overflow", size: 1}, nil)
	}
	_, err := measureDirectorySizeWithWalk(t.Context(), root, walk)
	if err == nil {
		t.Fatal("measureDirectorySizeWithWalk succeeded, want overflow error")
	}
}

type fakeFileInfo struct {
	name string
	size int64
	dir  bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }
