package versioncontrolops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractAddressConflictName(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
		{
			name: "unrelated error",
			err:  fmt.Errorf("connection refused"),
			want: "",
		},
		{
			name: "standard conflict",
			err:  fmt.Errorf("Error 1105: address conflict with a remote: 'default' -> file:///backup"),
			want: "default",
		},
		{
			name: "full dolt error format from doc comment",
			err:  fmt.Errorf("Error 1105: address conflict with a remote: 'backup_export' -> file:///some/path"),
			want: "backup_export",
		},
		{
			name: "missing closing quote",
			err:  fmt.Errorf("address conflict with a remote: 'oops"),
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractAddressConflictName(tt.err); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDirToFileURL_ResolvesSymlinks_GH3524 is a regression test for
// gastownhall/beads#3524: when the supplied directory traverses a
// symlink, DirToFileURL must emit the realpath form so that the
// resulting file:// URL is reachable from filesystem views (e.g.
// container bind-mounts) that only expose the target path.
func TestDirToFileURL_ResolvesSymlinks_GH3524(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// On macOS / some Linux setups t.TempDir itself can sit behind
	// a symlink (/tmp -> /private/tmp). Capture the resolved real
	// directory to compare against; the assertion is that the URL
	// matches the resolved path of the link, not the link path.
	wantRealAbs, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("evalsymlinks real: %v", err)
	}

	got, err := DirToFileURL(link)
	if err != nil {
		t.Fatalf("DirToFileURL(link): %v", err)
	}
	want := "file://" + wantRealAbs
	if got != want {
		t.Errorf("symlink not resolved: got %q, want %q", got, want)
	}
	if strings.Contains(got, "/link") {
		t.Errorf("URL still contains symlink component: %q", got)
	}
}

// TestDirToFileURL_BrokenSymlinkFallsBack covers the resilience case
// — EvalSymlinks fails on a dangling symlink; DirToFileURL must
// fall through to the un-resolved abs form rather than erroring out.
// This preserves the behavior callers had before the symlink-resolve
// patch when a path simply can't be resolved.
func TestDirToFileURL_BrokenSymlinkFallsBack(t *testing.T) {
	tmp := t.TempDir()
	link := filepath.Join(tmp, "broken-link")
	if err := os.Symlink(filepath.Join(tmp, "does-not-exist"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := DirToFileURL(link)
	if err != nil {
		t.Fatalf("DirToFileURL(broken-link) returned error: %v", err)
	}
	// The exact form is the un-resolved abs path of the symlink
	// itself. Whether t.TempDir is itself behind a symlink
	// (e.g. on macOS) is irrelevant — we only require that the
	// function did not error, and that the URL is the file:// abs
	// of the symlink path.
	abs, err := filepath.Abs(link)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	want := "file://" + abs
	if got != want {
		t.Errorf("broken-symlink fallback: got %q, want %q", got, want)
	}
}

// TestDirToFileURL_AlreadyRealpath confirms the no-op case: a
// directory that contains no symlink components round-trips
// unchanged. Guards against an over-eager EvalSymlinks
// implementation that might mangle simple paths.
func TestDirToFileURL_AlreadyRealpath(t *testing.T) {
	tmp := t.TempDir()
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("evalsymlinks tmp: %v", err)
	}

	got, err := DirToFileURL(resolved)
	if err != nil {
		t.Fatalf("DirToFileURL(resolved): %v", err)
	}
	want := "file://" + resolved
	if got != want {
		t.Errorf("already-realpath altered: got %q, want %q", got, want)
	}
}
