//go:build darwin

package doltserver

import (
	"io/fs"
	"os"
	"testing"
)

// TestParseDarwinCwdOrphanDetection pins the darwin-only half of the
// deleted-cwd signal. macOS lsof prints the bare path of a working directory
// that has since been removed — no " (deleted)" suffix, unlike Linux's
// /proc/<pid>/cwd — so the stat probe is the only thing that lets
// selectOrphanTestServerPIDs recognize a leaked test server on this platform
// (wy-j2zc8q).
func TestParseDarwinCwdOrphanDetection(t *testing.T) {
	const livePath = "/private/var/folders/xx/T/some-suite/002"
	const gonePath = "/private/var/folders/xx/T/TestUOWDependencyEditorContract1/002"

	statLive := func(string) (os.FileInfo, error) { return nil, nil }
	statGone := func(string) (os.FileInfo, error) {
		return nil, &fs.PathError{Op: "stat", Path: gonePath, Err: fs.ErrNotExist}
	}
	statDenied := func(string) (os.FileInfo, error) {
		return nil, &fs.PathError{Op: "stat", Path: livePath, Err: fs.ErrPermission}
	}
	statPanics := func(string) (os.FileInfo, error) {
		t.Helper()
		t.Fatal("stat must not be called when lsof already reported the directory deleted")
		return nil, nil
	}

	cases := []struct {
		name        string
		lsofOutput  string
		stat        statFunc
		wantCwd     string
		wantDeleted bool
		wantOK      bool
	}{
		{
			name:       "existing directory is live",
			lsofOutput: "p123\nn" + livePath + "\n",
			stat:       statLive,
			wantCwd:    livePath,
			wantOK:     true,
		},
		{
			name:        "missing directory is the leak signature even with no suffix",
			lsofOutput:  "p123\nn" + gonePath + "\n",
			stat:        statGone,
			wantCwd:     gonePath,
			wantDeleted: true,
			wantOK:      true,
		},
		{
			name:        "explicit (deleted) suffix still wins without touching the filesystem",
			lsofOutput:  "p123\nn" + gonePath + " (deleted)\n",
			stat:        statPanics,
			wantCwd:     gonePath,
			wantDeleted: true,
			wantOK:      true,
		},
		{
			name:       "a stat error that is not ENOENT is never read as deleted",
			lsofOutput: "p123\nn" + livePath + "\n",
			stat:       statDenied,
			wantCwd:    livePath,
			wantOK:     true,
		},
		{
			name:       "no n field at all is unknown, not deleted",
			lsofOutput: "p123\nfcwd\n",
			stat:       statPanics,
			wantOK:     false,
		},
		{
			name:       "an empty n field is skipped in favor of the next one",
			lsofOutput: "p123\nn\nn" + livePath + "\n",
			stat:       statLive,
			wantCwd:    livePath,
			wantOK:     true,
		},
		{
			name:       "empty output is unknown",
			lsofOutput: "",
			stat:       statPanics,
			wantOK:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd, deleted, ok := parseDarwinCwd([]byte(tc.lsofOutput), tc.stat)
			if cwd != tc.wantCwd || deleted != tc.wantDeleted || ok != tc.wantOK {
				t.Errorf("parseDarwinCwd() = (%q, %v, %v), want (%q, %v, %v)",
					cwd, deleted, ok, tc.wantCwd, tc.wantDeleted, tc.wantOK)
			}
		})
	}
}

// TestParseDarwinCwdAgainstRealFilesystem exercises the same path with the
// real os.Stat, so the ENOENT classification is checked against the kernel
// rather than a hand-written error value.
func TestParseDarwinCwdAgainstRealFilesystem(t *testing.T) {
	live := t.TempDir()
	gone := t.TempDir()
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("RemoveAll(%s): %v", gone, err)
	}

	if _, deleted, ok := parseDarwinCwd([]byte("n"+live+"\n"), os.Stat); !ok || deleted {
		t.Errorf("live dir %s: deleted=%v ok=%v, want deleted=false ok=true", live, deleted, ok)
	}
	if cwd, deleted, ok := parseDarwinCwd([]byte("n"+gone+"\n"), os.Stat); !ok || !deleted || cwd != gone {
		t.Errorf("removed dir %s: cwd=%q deleted=%v ok=%v, want cwd=%s deleted=true ok=true", gone, cwd, deleted, ok, gone)
	}
}
