//go:build cgo

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedReadonlyWriteAccessDiagnostic covers GH#7445 end to end at the
// CLI: inside a sandbox that denies writes to the embedded store's files,
// read commands — strict --readonly and ordinary classified reads alike —
// must exit nonzero through the existing error-output path with the
// write-access requirement and the retry guidance visible, not a bare
// permission-denied on the chunk journal that reads like lock contention.
// The exit code (1) and the JSON conventions of each route are unchanged;
// only the message gains the guidance.
func TestEmbeddedReadonlyWriteAccessDiagnostic(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rwdiag")

	// Writable phase (the control for AC-4): a normal write and a strict
	// read both succeed, and neither output carries the write-access
	// diagnostic.
	issue := bdCreate(t, bd, dir, "Sample issue")
	writableOut := bdCommand(t, bd, dir, "--readonly", "list", "--json")
	if strings.Contains(string(writableOut), "write access") {
		t.Errorf("strict read on a writable store carries the write-access diagnostic:\n%s", writableOut)
	}

	// Deny writes to every file under the embedded store only, as a
	// read-only sandbox does (GH#7445): ancillary .beads files (gate lock,
	// config, metadata) stay writable so the engine-open refusal is the
	// failure that surfaces, unmasked.
	denyEmbeddedFileWrites(t, filepath.Join(beadsDir, "embeddeddolt"))

	routes := []struct {
		name string
		args []string
	}{
		{"strict_readonly_list", []string{"--readonly", "list"}},
		{"strict_readonly_list_json", []string{"--readonly", "list", "--json"}},
		{"strict_readonly_show", []string{"--readonly", "show", issue.ID}},
		{"classified_read_list", []string{"list"}},
		{"classified_read_show", []string{"show", issue.ID}},
		// A write command fails earlier, at the identity check's preview
		// open (refuseUnverifiablePreviewOpen): the same engine open, so
		// the same refusal must reach the operator there too.
		{"identity_preview_write_command", []string{"create", "Another issue"}},
		// A preview command (--dry-run) opens through
		// OpenForPreviewCommand: the third entry point of the same engine
		// open.
		{"preview_command", []string{"compact", "--dry-run"}},
	}
	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			out, err := bdRunWithFlockRetry(t, bd, dir, tc.args...)
			if err == nil {
				t.Fatalf("bd %s succeeded against a store whose files the process cannot write:\n%s",
					strings.Join(tc.args, " "), out)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("bd %s: non-exit error %v", strings.Join(tc.args, " "), err)
			}
			if code := ee.ExitCode(); code != 1 {
				t.Errorf("bd %s exit code = %d, want the existing 1", strings.Join(tc.args, " "), code)
			}
			for _, phrase := range []string{
				"require write access even for --readonly",
				"retrying will not help",
			} {
				if !strings.Contains(string(out), phrase) {
					t.Errorf("bd %s output lacks the guidance %q:\n%s", strings.Join(tc.args, " "), phrase, out)
				}
			}
		})
	}
}

// denyEmbeddedFileWrites marks every regular file under root read-only
// (0o444), leaving directories searchable and writable, restores them on
// cleanup, and proves with a disposable probe file that this executor really
// cannot open files for writing: a root executor would bypass mode bits and
// void every assertion that depends on the denial.
func denyEmbeddedFileWrites(t *testing.T, root string) {
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
