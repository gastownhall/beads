package workspacegate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFinalPathToGateParent pins the string conversion from a Windows
// GetFinalPathNameByHandle result to the spelling a gate path uses: the
// extended-length prefix is dropped for drive-letter and UNC volumes, a
// volume-GUID spelling is kept verbatim (a letterless volume root keeps
// its trailing separator so a guarded directory directly at that root
// still resolves), and every unrecognized shape is an error — the caller
// falls back to another resolver rather than gate on a guessed path.
// The function is pure string handling, so the table runs on Linux.
func TestFinalPathToGateParent(t *testing.T) {
	const guid = `\\?\Volume{0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0}`
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"drive path", `\\?\C:\x\y`, `C:\x\y`, false},
		{"drive root", `\\?\C:\`, `C:\`, false},
		{"unc path", `\\?\UNC\srv\share\x`, `\\srv\share\x`, false},
		{"volume guid path", guid + `\x`, guid + `\x`, false},
		{"volume guid root keeps separator", guid + `\`, guid + `\`, false},
		{"empty", ``, ``, true},
		{"no extended-length prefix", `C:\x\y`, ``, true},
		{"nt device form", `\Device\HarddiskVolume3\x`, ``, true},
		{"unc without share", `\\?\UNC\srv`, ``, true},
		{"bare prefix", `\\?\`, ``, true},
		{"unc share empty", `\\?\UNC\srv\`, ``, true},
		{"drive path without root", `\\?\C:`, ``, true},
		{"lowercase drive", `\\?\c:\x`, ``, true},
		{"volume guid unclosed", `\\?\Volume{0f1e2d3c`, ``, true},
		{"volume guid without separator", `\\?\Volume{0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0}`, ``, true},
		{"not a volume spelling", `\\?\con`, ``, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := finalPathToGateParent(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("finalPathToGateParent(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("finalPathToGateParent(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("finalPathToGateParent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// stubResolver counts how often resolveParent consulted it and what to
// answer.
type stubResolver struct {
	calls int
	out   string
	err   error
}

func (r *stubResolver) fn() (string, error) {
	r.calls++
	return r.out, r.err
}

// TestResolveParent pins the fallback chain that picks a canonical parent
// spelling: the DOS final path first, the volume-GUID final path when the
// DOS query fails or its shape is rejected, filepath.EvalSymlinks when
// both fail (today's behavior, kept so nothing that resolves today stops
// resolving), and a joined error otherwise — never the unresolved input.
// Resolvers are injected, so the chain is table-tested on Linux.
func TestResolveParent(t *testing.T) {
	sentinelDos := `C:\x`
	sentinelGUID := `\\?\Volume{0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0}\x`
	sentinelEval := `C:\y`
	queryErr := errors.New("query failed")
	_, shapeErr := finalPathToGateParent(`\Device\HarddiskVolume3\x`)

	cases := []struct {
		name                                       string
		dos, guid                                  stubResolver
		eval                                       stubResolver
		want                                       string
		wantErr                                    bool
		wantErrIs                                  error  // errors.Is on the returned error
		wantErrText                                string // substring when wantErr
		wantDosCalls, wantGUIDCalls, wantEvalCalls int
	}{
		{
			name:         "dos accepted, later resolvers never run",
			dos:          stubResolver{out: sentinelDos},
			want:         sentinelDos,
			wantDosCalls: 1,
		},
		{
			name:         "dos query failure falls to guid",
			dos:          stubResolver{err: queryErr},
			guid:         stubResolver{out: sentinelGUID},
			want:         sentinelGUID,
			wantDosCalls: 1, wantGUIDCalls: 1,
		},
		{
			name:         "dos shape rejected falls to guid",
			dos:          stubResolver{err: shapeErr},
			guid:         stubResolver{out: sentinelGUID},
			want:         sentinelGUID,
			wantDosCalls: 1, wantGUIDCalls: 1,
		},
		{
			name:         "guid failure falls to evalsymlinks",
			dos:          stubResolver{err: queryErr},
			guid:         stubResolver{err: queryErr},
			eval:         stubResolver{out: sentinelEval},
			want:         sentinelEval,
			wantDosCalls: 1, wantGUIDCalls: 1, wantEvalCalls: 1,
		},
		{
			name:         "all fail, not-exist preserved and joined",
			dos:          stubResolver{err: queryErr},
			guid:         stubResolver{err: queryErr},
			eval:         stubResolver{err: fs.ErrNotExist},
			wantErr:      true,
			wantErrIs:    fs.ErrNotExist,
			wantErrText:  "dos volume name",
			wantDosCalls: 1, wantGUIDCalls: 1, wantEvalCalls: 1,
		},
		{
			name:         "all fail without not-exist",
			dos:          stubResolver{err: queryErr},
			guid:         stubResolver{err: errors.New("guid nope")},
			eval:         stubResolver{err: errors.New("eval nope")},
			wantErr:      true,
			wantErrText:  "evalsymlinks",
			wantDosCalls: 1, wantGUIDCalls: 1, wantEvalCalls: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveParent(tc.dos.fn, tc.guid.fn, tc.eval.fn)
			if tc.dos.calls != tc.wantDosCalls || tc.guid.calls != tc.wantGUIDCalls || tc.eval.calls != tc.wantEvalCalls {
				t.Fatalf("resolver calls dos/guid/eval = %d/%d/%d, want %d/%d/%d",
					tc.dos.calls, tc.guid.calls, tc.eval.calls,
					tc.wantDosCalls, tc.wantGUIDCalls, tc.wantEvalCalls)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveParent returned %q, want an error", got)
				}
				if got != "" {
					t.Fatalf("resolveParent errored with result %q; the unresolved input must never stand in for a resolution", got)
				}
				if !strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("error %q lacks %q (causes not joined?)", err, tc.wantErrText)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("errors.Is(%v, %v) = false, want true", err, tc.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveParent: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveParent = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestForDirMissingParent pins the missing-parent error contract on
// every platform: the wrap starts with "workspacegate: gate parent", the
// error chain carries fs.ErrNotExist, and nothing is ever created at the
// parent's path (the Windows resolver opens with OPEN_EXISTING, never
// creating, and returns the open failure unchanged). On Unix this is
// today's EvalSymlinks behavior, unchanged by design; the test exists so
// the Windows handle route cannot break it.
func TestForDirMissingParent(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no", "such", "dir")

	_, err := ForWorkspace(filepath.Join(missing, ".beads"))
	if err == nil {
		t.Fatal("ForWorkspace beneath a missing parent must fail")
	}
	if !strings.HasPrefix(err.Error(), "workspacegate: gate parent") {
		t.Fatalf("err = %q, want prefix %q", err, "workspacegate: gate parent")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("errors.Is(%v, fs.ErrNotExist) = false, want true", err)
	}
	if _, serr := os.Lstat(missing); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("resolution must not create the parent: Lstat(%s) = %v, want not-exist", missing, serr)
	}
}

// TestNonWindowsCanonicalParentMatchesEvalSymlinks pins the non-Windows
// resolution to exactly filepath.EvalSymlinks, byte for byte: nothing
// about gate identity changes on those platforms when Windows switches to
// handle-based resolution. Windows canonicalization is deliberately
// different (see gate_windows_test.go).
func TestNonWindowsCanonicalParentMatchesEvalSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows canonicalization is handle-based; see gate_windows_test.go")
	}
	dir := t.TempDir()

	got, err := canonicalParent(dir)
	if err != nil {
		t.Fatalf("canonicalParent(%s): %v", dir, err)
	}
	if want := mustEval(t, dir); got != want {
		t.Fatalf("canonicalParent(%s) = %q, want %q (EvalSymlinks, byte for byte)", dir, got, want)
	}

	// A symlinked spelling resolves to the physical directory, as
	// EvalSymlinks does.
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	got, err = canonicalParent(link)
	if err != nil {
		t.Fatalf("canonicalParent(%s): %v", link, err)
	}
	if want := mustEval(t, link); got != want {
		t.Fatalf("canonicalParent(%s) = %q, want %q", link, got, want)
	}

	// A missing parent errors with fs.ErrNotExist, as EvalSymlinks does.
	_, err = canonicalParent(filepath.Join(dir, "gone"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("canonicalParent(missing) = %v, want fs.ErrNotExist", err)
	}
}
