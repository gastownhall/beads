//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAccessDiagnostic is what the embedded store's write-access refusal
// (embeddeddolt.ErrStoreRequiresWriteAccess) says: that the store needs write
// access even for read-only commands, and that retrying cannot help.
var writeAccessDiagnostic = []string{
	"require write access even for --readonly",
	"retrying will not help until access changes",
}

// denyEmbeddedWrites takes write permission away from every file and
// directory under root, keeping read and search permission, and restores it
// at cleanup so t.TempDir can remove the tree. It proves the denial holds for
// this process with a disposable test-owned file under the same policy: an
// executor that bypasses permissions (root) would make the test vacuous.
func denyEmbeddedWrites(t *testing.T, root string) {
	t.Helper()
	probe := filepath.Join(root, "write-access-probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
		t.Fatalf("write probe file: %v", err)
	}
	var files, dirs []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, p)
		case d.Type().IsRegular():
			files = append(files, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	t.Cleanup(func() {
		for _, p := range dirs {
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
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			t.Fatalf("chmod %s read-only: %v", dirs[i], err)
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
	if err := os.WriteFile(filepath.Join(root, "write-access-probe-new"), nil, 0o644); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("create a file in a read-only directory: %v, want a permission error", err)
	}
}

// TestEmbeddedReadonlyWriteAccessDiagnostic pins GH#7445 end to end: in a
// workspace whose embedded store the process may not write (a read-only
// sandbox), every command that opens the store, `bd --readonly list` included,
// must exit 1 through its existing error path with an error saying the store
// needs write access and that retrying will not help. At base it printed only
// the engine's bare "permission denied" on the chunk-journal LOCK, which
// agents retried as lock contention.
//
// Writes are denied only under .beads/embeddeddolt, so no ancillary file bd
// writes elsewhere in .beads can fail first and mask the engine's refusal.
func TestEmbeddedReadonlyWriteAccessDiagnostic(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "test")
	issue := bdCreate(t, bd, dir, "sample issue", "-t", "task")

	stdout, stderr, exit := bdRunStreams(t, bd, dir, "--readonly", "list", "--json")
	if exit != 0 {
		t.Fatalf("bd --readonly list --json on a writable store exited %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}

	denyEmbeddedWrites(t, filepath.Join(beadsDir, "embeddeddolt"))

	for _, tc := range []struct {
		name string
		args []string
		// jsonStdout is set for a route whose refusal is a JSON error object
		// on stdout today (the identity-preview gate under --json); every
		// other route reports on stderr as text, --json or not.
		jsonStdout bool
	}{
		{"strict readonly list", []string{"--readonly", "list"}, false},
		{"strict readonly list json", []string{"--readonly", "list", "--json"}, false},
		{"strict readonly show", []string{"--readonly", "show", issue.ID}, false},
		{"list", []string{"list"}, false},
		{"show", []string{"show", issue.ID}, false},
		{"preview create", []string{"create", "previewed issue", "--dry-run"}, false},
		{"identity preview gate", []string{"update", issue.ID, "--status", "in_progress"}, false},
		{"identity preview gate json", []string{"update", issue.ID, "--status", "in_progress", "--json"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := bdRunStreams(t, bd, dir, tc.args...)
			if exit != 1 {
				t.Fatalf("bd %s exited %d, want 1\nstdout:\n%s\nstderr:\n%s", strings.Join(tc.args, " "), exit, stdout, stderr)
			}
			reported := stderr
			if tc.jsonStdout {
				var envelope struct {
					Error string `json:"error"`
					Data  struct {
						Error string `json:"error"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
					t.Fatalf("bd %s stdout is not a JSON error object: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(tc.args, " "), err, stdout, stderr)
				}
				reported = envelope.Error + envelope.Data.Error
			}
			for _, want := range writeAccessDiagnostic {
				if !strings.Contains(reported, want) {
					t.Errorf("bd %s does not say %q\nstdout:\n%s\nstderr:\n%s", strings.Join(tc.args, " "), want, stdout, stderr)
				}
			}
		})
	}
}
