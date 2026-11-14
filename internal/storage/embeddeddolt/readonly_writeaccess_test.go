//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// initWriteAccessFixture initializes a disposable embedded store and returns
// its .beads directory, fully closed: the fixture every case in this file
// copies, so a case's failed engine open cannot contaminate another's.
func initWriteAccessFixture(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return beadsDir
}

// copyWriteAccessFixture copies an initialized .beads tree into a fresh temp
// directory. Each case opens its own copy: a failed embedded open leaves
// engine cleanup state behind it, and one case's failure must not bleed into
// the next through files a half-opened engine left or took away.
func copyWriteAccessFixture(t *testing.T, srcBeadsDir string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir fixture copy: %v", err)
	}
	if err := os.CopyFS(dst, os.DirFS(srcBeadsDir)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

// denyFileWrites marks every regular file under root read-only (0o444),
// leaving directories searchable and writable — the reporter's sandbox shape
// (GH#7445): the embedded engine's chunk journal cannot be opened for
// writing, while nothing else about the tree is unusual. Permissions are
// restored on cleanup, and the denial is proven with a disposable probe file
// under the same policy: an executor that bypasses mode bits (root) would
// make every assertion below meaningless, so it fails the test instead.
func denyFileWrites(t *testing.T, root string) {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
			return os.Chmod(p, 0o444)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chmod read-only under %s: %v", root, err)
	}
	t.Cleanup(func() {
		for _, p := range files {
			_ = os.Chmod(p, 0o644)
		}
	})

	probe := filepath.Join(t.TempDir(), "write-access-probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	if err := os.Chmod(probe, 0o444); err != nil {
		t.Fatalf("chmod probe: %v", err)
	}
	f, err := os.OpenFile(probe, os.O_RDWR, 0)
	if err == nil {
		f.Close()
		t.Fatalf("executor bypasses file permissions (running as root?): write denial under %s is not real; this test needs an unprivileged executor", root)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("probe open: %v", err)
	}
}

// TestEmbeddedReadOpenWriteAccessDenied covers GH#7445: an initialized,
// readable embedded store whose engine files the process cannot write must
// fail every read open — strict OpenReadOnly, OpenForPreviewCommand, the
// classified-read OpenForReadOnlyCommand — with an error that carries the
// permission cause and says the store needs write access even for --readonly,
// so callers stop retrying a permanent failure. The embedded engine opens its
// chunk journal O_RDWR on every open, read command or not, so all three
// entry points share the failure.
func TestEmbeddedReadOpenWriteAccessDenied(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	tmpl := initWriteAccessFixture(t)

	openers := []struct {
		name string
		open func(ctx context.Context, beadsDir string) error
	}{
		{"OpenReadOnly", func(ctx context.Context, beadsDir string) error {
			_, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
			return err
		}},
		{"OpenForPreviewCommand", func(ctx context.Context, beadsDir string) error {
			_, err := embeddeddolt.OpenForPreviewCommand(ctx, beadsDir, "testdb", "main")
			return err
		}},
		{"OpenForReadOnlyCommand", func(ctx context.Context, beadsDir string) error {
			_, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main")
			return err
		}},
	}
	for _, tc := range openers {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := copyWriteAccessFixture(t, tmpl)
			denyFileWrites(t, filepath.Join(beadsDir, "embeddeddolt"))

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			err := tc.open(ctx, beadsDir)
			if err == nil {
				t.Fatal("open succeeded against a store whose files the process cannot write; want the write-access refusal")
			}
			t.Logf("open error: %v", err)
			if !strings.Contains(err.Error(), "require write access even for --readonly") {
				t.Errorf("error does not state the write-access requirement:\n%v", err)
			}
			if !strings.Contains(err.Error(), "retrying will not help") {
				t.Errorf("error does not say retrying cannot help:\n%v", err)
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("error lost the permission cause (fs.ErrPermission): %v", err)
			}
			if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
				t.Errorf("error lost the underlying errno: %v", err)
			}
			if !errors.Is(err, embeddeddolt.ErrStoreRequiresWriteAccess) {
				t.Errorf("error is not ErrStoreRequiresWriteAccess: %v", err)
			}
		})
	}
}
