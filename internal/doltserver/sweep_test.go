package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIsUnderDir(t *testing.T) {
	cases := []struct {
		dir, root string
		want      bool
	}{
		{"/tmp", "/tmp", true},
		{"/tmp/foo", "/tmp", true},
		{"/tmp/foo/bar", "/tmp", true},
		{"/tmp2/foo", "/tmp", false},
		{"/tmpfoo", "/tmp", false},
		{"/", "/tmp", false},
		{"/tmp/foo", "", false},
	}
	for _, tc := range cases {
		if got := isUnderDir(tc.dir, tc.root); got != tc.want {
			t.Errorf("isUnderDir(%q, %q) = %v, want %v", tc.dir, tc.root, got, tc.want)
		}
	}
}

func TestGatherPSCandidates(t *testing.T) {
	psOutput := []byte(`
  101 dolt sql-server -H 127.0.0.1 -P 12345
not-a-pid dolt sql-server
  102 dolt status
  103 /opt/dolt --prof cpu sql-server -P 12346
  104 dolt sql-server -P 12347
`)
	cwds := map[int]struct {
		dir     string
		deleted bool
		ok      bool
	}{
		101: {dir: "/tmp/my-suite/.beads/dolt", ok: true},
		103: {dir: "/tmp/deleted-suite/.beads/dolt", deleted: true, ok: true},
		104: {ok: false},
	}

	candidates := gatherPSCandidates(psOutput, func(pid int) (string, bool, bool) {
		cwd := cwds[pid]
		return cwd.dir, cwd.deleted, cwd.ok
	})
	wantCandidates := []serverCandidate{
		{pid: 101, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/my-suite/.beads/dolt"},
		{pid: 103, cmdline: "/opt/dolt --prof cpu sql-server -P 12346", cwd: "/tmp/deleted-suite/.beads/dolt", cwdDeleted: true},
	}
	if !reflect.DeepEqual(candidates, wantCandidates) {
		t.Fatalf("gatherPSCandidates() = %#v, want %#v", candidates, wantCandidates)
	}

	// The darwin ps/lsof path must hand the selection a cwd for every
	// survivor, because that cwd is what the sweep's report prints
	// (wy-j2zc8q). Assert the whole SweptServer, not just the PIDs.
	gotServers := selectServersUnderRoots(candidates, []string{"/tmp/my-suite", "/tmp/deleted-suite"})
	wantServers := []SweptServer{
		{PID: 101, Cwd: "/tmp/my-suite/.beads/dolt"},
		{PID: 103, Cwd: "/tmp/deleted-suite/.beads/dolt"},
	}
	if !reflect.DeepEqual(gotServers, wantServers) {
		t.Errorf("darwin ps selection path = %v, want %v", gotServers, wantServers)
	}
}

// sweptPIDs projects a selection result down to its PIDs so the big tables
// above can stay readable. The cwd every entry carries is pinned directly by
// TestGatherPSCandidates and TestSweptServerNamesItsDirectory.
func sweptPIDs(servers []SweptServer) []int {
	if len(servers) == 0 {
		return nil
	}
	pids := make([]int, 0, len(servers))
	for _, server := range servers {
		pids = append(pids, server.PID)
	}
	return pids
}

// TestSweptServerNamesItsDirectory pins the rendering both sweep stderr lines
// rely on: a reader gets the leaked server's working directory, which names
// the test that leaked it, not just a PID that is already dead (wy-j2zc8q).
func TestSweptServerNamesItsDirectory(t *testing.T) {
	server := SweptServer{PID: 21881, Cwd: "/tmp/TestFoo123/001/.beads/dolt"}
	const want = "21881 cwd=/tmp/TestFoo123/001/.beads/dolt"
	if got := server.String(); got != want {
		t.Errorf("SweptServer.String() = %q, want %q", got, want)
	}
	if got := fmt.Sprintf("%v", []SweptServer{server}); got != "["+want+"]" {
		t.Errorf("formatted slice = %q, want %q", got, "["+want+"]")
	}
	// A cwd-less server can never be selected (selectServers skips them),
	// but the renderer must not print a dangling "cwd=" if one appears.
	if got := (SweptServer{PID: 7}).String(); got != "7" {
		t.Errorf("SweptServer with no cwd = %q, want %q", got, "7")
	}
}

// TestSelectServersUnderSuiteRoots pins the strictly root-scoped selection
// every sweep relies on. SweepDeadSuiteRoots runs at suite START, with
// sibling packages mid-run, and SweepSuiteTestServers at suite end, while
// other suites may still be running, so both must reap only what is
// provably inside the roots they were handed: a deleted cwd outside them is
// not debris, whether it sat under a global temp dir or a production
// workspace that was moved or unmounted (gastownhall/beads#6186,
// mybd-q6cz, wy-j2zc8q).
func TestSelectServersUnderSuiteRoots(t *testing.T) {
	candidates := []serverCandidate{
		{pid: 500, cmdline: "dolt sql-server -P 1", cwd: "/tmp/dead-root/.beads/dolt"},
		{pid: 501, cmdline: "dolt sql-server -P 2", cwd: "/tmp/dead-root/nested/deeper/dolt", cwdDeleted: true},
		{pid: 502, cmdline: "dolt sql-server -P 3", cwd: "/tmp/live-sibling-root/.beads/dolt"},
		{pid: 503, cmdline: "dolt sql-server -P 4", cwd: "/tmp/some-other-suite/.beads/dolt", cwdDeleted: true},
		{pid: 504, cmdline: "dolt sql-server -P 5", cwd: "/Users/dev/project/.beads/dolt", cwdDeleted: true},
		{pid: 505, cmdline: "not-dolt --flag", cwd: "/tmp/dead-root/whatever"},
		{pid: 506, cmdline: "dolt sql-server -P 6", cwd: ""},
		{pid: 507, cmdline: "dolt --prof cpu --prof-path /tmp/dead-root/dolt-pprof sql-server -P 7", cwd: "/tmp/dead-root/.beads/dolt"},
		{pid: 508, cmdline: "dolt status", cwd: "/tmp/dead-root/whatever"},
		{pid: 509, cmdline: "dolt sql-server -P 9", cwd: "/tmp/dead-root2/evil"},
		{pid: 510, cmdline: "dolt sql-server -P 10", cwd: "/home/dev/project/.beads/dolt"},
		{pid: 511, cmdline: "dolt sql-server -P 11", cwd: "/Volumes/external/work/.beads/dolt", cwdDeleted: true},
		{pid: 512, cmdline: "dolt sql-server -P 12", cwd: "/private/var/folders/c1/xx/T/TestUOWDependencyEditorContract1/002", cwdDeleted: true},
		{pid: 513, cmdline: "dolt sql-server -P 13", cwd: "/var/tmp/beads-bd-tests-xyz/.beads/dolt", cwdDeleted: true},
	}

	got := selectServersUnderRoots(candidates, []string{"/tmp/dead-root"})
	want := []SweptServer{
		{PID: 500, Cwd: "/tmp/dead-root/.beads/dolt"},
		{PID: 501, Cwd: "/tmp/dead-root/nested/deeper/dolt"},
		{PID: 507, Cwd: "/tmp/dead-root/.beads/dolt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectServersUnderRoots() = %v, want %v", got, want)
	}

	// A deleted cwd under the caller's own root needs no temp root at all,
	// and the macOS private form of a root matches only when the caller
	// passes it (canonicalRoots supplies it in production).
	if got, want := sweptPIDs(selectServersUnderRoots(candidates, []string{"/opt/none", "/private/var/folders/c1/xx/T"})), []int{512}; !reflect.DeepEqual(got, want) {
		t.Errorf("selectServersUnderRoots(private form) = %v, want %v", got, want)
	}

	if got := selectServersUnderRoots(candidates, nil); got != nil {
		t.Errorf("selectServersUnderRoots() with no roots = %v, want nothing", got)
	}
}

// TestCanonicalRootsForSweep covers the path-form expansion that lets a root
// match however the OS reports a process's cwd.
func TestCanonicalRootsForSweep(t *testing.T) {
	if got := canonicalRoots(nil); got != nil {
		t.Errorf("canonicalRoots(nil) = %v, want nothing", got)
	}
	if got := canonicalRoots([]string{"", ""}); got != nil {
		t.Errorf("canonicalRoots(empties) = %v, want nothing", got)
	}

	// A path that cannot be resolved is still kept as given.
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := canonicalRoots([]string{missing}); !reflect.DeepEqual(got, []string{missing}) {
		t.Errorf("canonicalRoots(%q) = %v, want just the literal path", missing, got)
	}

	// A real directory reached through a symlink yields both forms, in that
	// order, with no duplicates — this is what makes macOS's
	// /var/folders/… vs /private/var/folders/… pair match.
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", link, err)
	}
	got := canonicalRoots([]string{link, link, resolved})
	want := []string{link, resolved}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("canonicalRoots() = %v, want %v", got, want)
	}
}
