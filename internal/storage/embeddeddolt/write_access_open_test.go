//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// writeAccessOpeners are the embedded opens GH#7445 covers: the strict
// read-only and preview opens that never mutate a store, the classified-read
// open every read command takes, and the ordinary writable open, which shares
// the same engine open.
var writeAccessOpeners = []struct {
	name string
	open func(ctx context.Context, beadsDir, database, branch string) (*embeddeddolt.EmbeddedDoltStore, error)
}{
	{"OpenReadOnly", embeddeddolt.OpenReadOnly},
	{"OpenForPreviewCommand", embeddeddolt.OpenForPreviewCommand},
	{"OpenForReadOnlyCommand", embeddeddolt.OpenForReadOnlyCommand},
	{"Open", embeddeddolt.Open},
}

// newWriteAccessFixture initializes an embedded store holding one issue under
// a fresh t.TempDir and fully closes it, returning its .beads directory.
func newWriteAccessFixture(t *testing.T, ctx context.Context) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "accessdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "accessdb"); err != nil {
		t.Fatalf("SetConfig issue_prefix: %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "accessdb-1", Title: "sample issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return beadsDir
}

// denyWrites takes write permission away from every file under root, and from
// every directory too when dirs is set, keeping read and search permission,
// and restores it at cleanup so t.TempDir can remove the tree. It then proves
// the denial holds for this process (an executor that bypasses permissions,
// such as root, would make the test vacuous) with a disposable test-owned
// file under the same policy, never by probing the engine's own files.
func denyWrites(t *testing.T, root string, dirs bool) {
	t.Helper()
	probe := filepath.Join(root, "write-access-probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
		t.Fatalf("write probe file: %v", err)
	}
	var files, directories []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			directories = append(directories, p)
		case d.Type().IsRegular():
			files = append(files, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	t.Cleanup(func() {
		for _, p := range directories {
			_ = os.Chmod(p, 0o755)
		}
		for _, p := range files {
			_ = os.Chmod(p, 0o644)
		}
	})
	for _, p := range files {
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatalf("chmod %s read-only: %v", p, err)
		}
	}
	if dirs {
		// Deepest first, so a parent is still writable while its
		// children change.
		for i := len(directories) - 1; i >= 0; i-- {
			if err := os.Chmod(directories[i], 0o555); err != nil {
				t.Fatalf("chmod %s read-only: %v", directories[i], err)
			}
		}
	}

	f, err := os.OpenFile(probe, os.O_RDWR, 0)
	if err == nil {
		_ = f.Close()
		t.Skip("this executor writes read-only files (running as root?): the denial cannot be reproduced")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("open read-only probe for writing: %v, want a permission error", err)
	}
	if dirs {
		err := os.WriteFile(filepath.Join(root, "write-access-probe-new"), nil, 0o644)
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("create a file in a read-only directory: %v, want a permission error", err)
		}
	}
}

// TestEmbeddedReadOpenWriteAccessDenied pins GH#7445 at the storage layer.
// The embedded engine opens its files for writing on every open, so an
// initialized, readable store the process may not write cannot be opened even
// by OpenReadOnly. That refusal is permanent until access changes, but at base
// it surfaced as a bare "permission denied" (on Windows "Access is denied",
// which reads like lock contention), so sandboxed agents retried it. Every
// opener must now return ErrStoreRequiresWriteAccess carrying the engine's
// own error and the guidance that retrying cannot help.
//
// Two denial shapes run: the reporter's (every file read-only, directories
// untouched) and a read-only sandbox's (no file or directory writable). Each
// case opens a store of its own, so one failed engine open cannot leak state
// into the next.
func TestEmbeddedReadOpenWriteAccessDenied(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	for _, shape := range []struct {
		name string
		dirs bool
	}{
		{"read-only files", false},
		{"read-only tree", true},
	} {
		for _, opener := range writeAccessOpeners {
			t.Run(shape.name+"/"+opener.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
				defer cancel()
				beadsDir := newWriteAccessFixture(t, ctx)
				denyWrites(t, filepath.Join(beadsDir, "embeddeddolt"), shape.dirs)

				store, err := opener.open(ctx, beadsDir, "accessdb", "main")
				if err == nil {
					_ = store.Close()
					t.Fatalf("%s on a store the process cannot write = nil error, want a write-access refusal", opener.name)
				}
				if ctx.Err() != nil {
					t.Fatalf("%s hung until the deadline instead of refusing: %v", opener.name, err)
				}
				msg := err.Error()
				for _, want := range []string{
					"require write access even for --readonly",
					"retrying will not help until access changes",
				} {
					if !strings.Contains(msg, want) {
						t.Errorf("%s error %q does not explain %q", opener.name, msg, want)
					}
				}
				if !errors.Is(err, fs.ErrPermission) {
					t.Errorf("%s error %v lost the underlying permission error", opener.name, err)
				}
				var pathErr *fs.PathError
				if !errors.As(err, &pathErr) {
					t.Errorf("%s error %v lost the engine's *fs.PathError", opener.name, err)
				}
				if !errors.Is(err, embeddeddolt.ErrStoreRequiresWriteAccess) {
					t.Errorf("%s error %v does not match ErrStoreRequiresWriteAccess", opener.name, err)
				}
			})
		}
	}
}

// TestEmbeddedWritableStoreOpensWithoutWriteAccessDiagnostic is the control
// for TestEmbeddedReadOpenWriteAccessDenied: on a store the process may write,
// every opener still succeeds and reads the sample issue, the writable opens
// still write, and the write-refusing opens still refuse with ErrReadOnly. No
// successful open and no refusal carries the GH#7445 write-access diagnostic.
func TestEmbeddedWritableStoreOpensWithoutWriteAccessDiagnostic(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	for _, opener := range writeAccessOpeners {
		t.Run(opener.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			beadsDir := newWriteAccessFixture(t, ctx)

			store, err := opener.open(ctx, beadsDir, "accessdb", "main")
			if err != nil {
				t.Fatalf("%s on a writable store: %v", opener.name, err)
			}
			defer func() { _ = store.Close() }()

			issue, err := store.GetIssue(ctx, "accessdb-1")
			if err != nil {
				t.Fatalf("GetIssue after %s: %v", opener.name, err)
			}
			if issue.Title != "sample issue" {
				t.Fatalf("GetIssue after %s = %q, want the sample issue", opener.name, issue.Title)
			}

			writeErr := store.SetLocalMetadata(ctx, "write_access_control", "written")
			switch opener.name {
			case "OpenReadOnly", "OpenForPreviewCommand":
				if !errors.Is(writeErr, embeddeddolt.ErrReadOnly) {
					t.Fatalf("write through %s = %v, want ErrReadOnly", opener.name, writeErr)
				}
			default:
				if writeErr != nil {
					t.Fatalf("write through %s: %v", opener.name, writeErr)
				}
			}
			if errors.Is(writeErr, embeddeddolt.ErrStoreRequiresWriteAccess) ||
				(writeErr != nil && strings.Contains(writeErr.Error(), "require write access")) {
				t.Fatalf("write through %s on a writable store carries the write-access diagnostic: %v", opener.name, writeErr)
			}
		})
	}
}
