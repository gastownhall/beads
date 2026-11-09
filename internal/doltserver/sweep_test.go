package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
  105 tail -f /tmp/my-suite/dolt/sql-server.log
  106 vim /tmp/my-suite/dolt/sql-server.yaml
`)
	cwds := map[int]struct {
		dir     string
		deleted bool
		ok      bool
	}{
		101: {dir: "/tmp/my-suite/.beads/dolt", ok: true},
		103: {dir: "/tmp/deleted-suite/.beads/dolt", deleted: true, ok: true},
		104: {ok: false},
		105: {dir: "/tmp/my-suite/logs", ok: true},
		106: {dir: "/tmp/my-suite/logs", ok: true},
	}

	candidates := gatherPSCandidates(psOutput, func(pid int) (string, bool, bool) {
		cwd := cwds[pid]
		return cwd.dir, cwd.deleted, cwd.ok
	})
	// Rows 105/106 mention both words but are not dolt binaries: the gather
	// filter must drop them before any cwd is even asked for (#6186).
	wantCandidates := []serverCandidate{
		{pid: 101, argv: []string{"dolt", "sql-server", "-H", "127.0.0.1", "-P", "12345"}, cwd: "/tmp/my-suite/.beads/dolt"},
		{pid: 103, argv: []string{"/opt/dolt", "--prof", "cpu", "sql-server", "-P", "12346"}, cwd: "/tmp/deleted-suite/.beads/dolt", cwdDeleted: true},
	}
	if !reflect.DeepEqual(candidates, wantCandidates) {
		t.Fatalf("gatherPSCandidates() = %#v, want %#v", candidates, wantCandidates)
	}

	// The darwin ps/lsof path must hand the selection a cwd for every
	// survivor, because that cwd is what the sweep's report prints
	// (wy-j2zc8q). Assert the whole SweptServer, not just the PIDs. Only the
	// candidate under the vouched root survives the root-scoped selection.
	gotServers := selectServersUnderRoots(candidates, []string{"/tmp/my-suite"})
	wantServers := []SweptServer{
		{PID: 101, Cwd: "/tmp/my-suite/.beads/dolt"},
	}
	if !reflect.DeepEqual(gotServers, wantServers) {
		t.Errorf("darwin ps selection path = %v, want %v", gotServers, wantServers)
	}
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
// SweepDeadSuiteRoots relies on. It runs at suite START, with sibling
// packages mid-run, so it must reap only what is provably inside the roots
// it was handed: no deleted-cwd arm, no temp-dir reasoning of any kind.
func TestSelectServersUnderSuiteRoots(t *testing.T) {
	candidates := []serverCandidate{
		{pid: 500, argv: []string{"dolt", "sql-server", "-P", "1"}, cwd: "/tmp/dead-root/.beads/dolt"},
		{pid: 501, argv: []string{"dolt", "sql-server", "-P", "2"}, cwd: "/tmp/dead-root/nested/deeper/dolt", cwdDeleted: true},
		{pid: 502, argv: []string{"dolt", "sql-server", "-P", "3"}, cwd: "/tmp/live-sibling-root/.beads/dolt"},
		{pid: 503, argv: []string{"dolt", "sql-server", "-P", "4"}, cwd: "/tmp/some-other-suite/.beads/dolt", cwdDeleted: true},
		{pid: 504, argv: []string{"dolt", "sql-server", "-P", "5"}, cwd: "/Users/dev/project/.beads/dolt", cwdDeleted: true},
		{pid: 505, argv: []string{"not-dolt", "--flag"}, cwd: "/tmp/dead-root/whatever"},
		{pid: 506, argv: []string{"dolt", "sql-server", "-P", "6"}, cwd: ""},
		// Ported from the removed global-sweep selector table (#6186): a
		// live production workspace, a dolt without the server subcommand,
		// and a sibling path that merely shares the root's prefix.
		{pid: 507, argv: []string{"dolt", "sql-server", "-H", "127.0.0.1", "-P", "3307"}, cwd: "/home/dev/project/.beads/dolt"},
		{pid: 508, argv: []string{"dolt", "status"}, cwd: "/tmp/dead-root/whatever"},
		{pid: 509, argv: []string{"dolt", "sql-server", "-P", "7"}, cwd: "/tmp/dead-root2/evil"},
		// The deleted-cwd path in its symlink-resolved form, as macOS's lsof
		// reports it: selected when the caller vouches for both forms of the
		// owning root, which is what canonicalRoots hands the selector.
		{pid: 510, argv: []string{"dolt", "sql-server", "--config", "/private/var/folders/c1/xx/T/TestUOWDependencyEditorContract1/003/config.yaml"}, cwd: "/private/var/folders/c1/xx/T/TestUOWDependencyEditorContract1/002", cwdDeleted: true},
	}

	got := selectServersUnderRoots(candidates, []string{"/tmp/dead-root"})
	want := []SweptServer{
		{PID: 500, Cwd: "/tmp/dead-root/.beads/dolt"},
		{PID: 501, Cwd: "/tmp/dead-root/nested/deeper/dolt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectServersUnderRoots() = %v, want %v", got, want)
	}

	// Both spellings of the owning root (canonicalRoots' expansion) select
	// the deleted-cwd candidate reported in its resolved form.
	got = selectServersUnderRoots(candidates, []string{"/var/folders/c1/xx/T", "/private/var/folders/c1/xx/T"})
	want = []SweptServer{
		{PID: 510, Cwd: "/private/var/folders/c1/xx/T/TestUOWDependencyEditorContract1/002"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectServersUnderRoots() over both root forms = %v, want %v", got, want)
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

// TestIsDoltServerCmdlineRequiresDoltArgv0AndSqlServerArg pins the tightened
// predicate from #6186: argv[0] basename must be "dolt" and one argument must
// be exactly "sql-server"; a substring hit anywhere in the cmdline is not a
// dolt server.
func TestIsDoltServerCmdlineRequiresDoltArgv0AndSqlServerArg(t *testing.T) {
	cases := []struct {
		cmdline string
		want    bool
	}{
		{"dolt sql-server --port 3307", true},
		{"/usr/local/bin/dolt sql-server -H 127.0.0.1", true},
		{"dolt --prof cpu --prof-path /tmp/p sql-server --port 1", true},
		{"vim /tmp/dolt/sql-server.log", false},
		{"tail -f $HOME/.beads/dolt/sql-server.log", false},
		{"/opt/dolthub-tools/run sql-server", false},
		{"dolt sql-server-helper", false},
	}
	for _, tc := range cases {
		if got := isDoltServerCmdline(tc.cmdline); got != tc.want {
			t.Errorf("isDoltServerCmdline(%q) = %v, want %v", tc.cmdline, got, tc.want)
		}
	}
}

// TestIsDoltServerArgv tables the argv identity gate itself, including the
// shapes no single command line can produce (nil, a bare executable).
func TestIsDoltServerArgv(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"bare dolt", []string{"dolt", "sql-server"}, true},
		{"relative dolt", []string{"./dolt", "sql-server", "--port", "3306"}, true},
		{"absolute dolt", []string{"/usr/local/bin/dolt", "sql-server", "-H", "127.0.0.1"}, true},
		{"global flags before the subcommand", []string{"dolt", "--prof", "cpu", "--prof-path", "/tmp/p", "sql-server"}, true},
		{"executable path containing spaces", []string{"/opt/my tools/dolt", "sql-server"}, true},
		// A separate positional argument named sql-server still passes by
		// design: the predicate is lexical, not a parser of dolt's grammar.
		{"sql-server as an ordinary positional argument", []string{"dolt", "checkout", "sql-server"}, true},
		{"nil argv", nil, false},
		{"single element", []string{"dolt"}, false},
		{"empty argv[0]", []string{"", "sql-server"}, false},
		{"windows binary name", []string{`C:\dolt\dolt.exe`, "sql-server"}, false},
		{"test binary named like the server", []string{"doltserver.test", "sql-server"}, false},
		{"log tailer", []string{"tail", "-f", "dolt-sql-server.log"}, false},
		{"log viewer under a dolt dir", []string{"vim", "/tmp/dolt/sql-server.log"}, false},
		{"shell body mentioning the words", []string{"/bin/sh", "-c", "echo dolt; exec tail sql-server.log"}, false},
		{"watching a dolt server", []string{"watch", "dolt", "sql-server"}, false},
		{"grepping for dolt servers", []string{"grep", "dolt.*sql-server", "ps.txt"}, false},
		{"dolt without the server subcommand", []string{"dolt", "status"}, false},
		{"plural subcommand", []string{"dolt", "sql-servers"}, false},
		{"substring inside a flag value", []string{"dolt", "--config", "sql-server.yaml"}, false},
		{"sql-server only inside a larger argument", []string{"dolt", "-m", "restart sql-server"}, false},
	}
	for _, tc := range cases {
		if got := isDoltServerArgv(tc.argv); got != tc.want {
			t.Errorf("%s: isDoltServerArgv(%q) = %v, want %v", tc.name, tc.argv, got, tc.want)
		}
	}
}

// TestDeprecatedGlobalSweepEntryPointsAreGone guards the removal of the
// deprecated global sweep (#6186): SweepOrphanedTestServers and the
// temp-root heuristics only it used (tempDirRoots and its credibility
// witnesses) had no callers at main, and they were the last path by which
// the sweep could signal a process outside the roots a caller vouches for.
// The surviving sweeps are strictly root-scoped (selectServersUnderRoots);
// this test scans the package's non-test sources so a resurrection of the
// global arm cannot slip back in unnoticed.
func TestDeprecatedGlobalSweepEntryPointsAreGone(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	packageDir := filepath.Dir(thisFile)
	if !filepath.IsAbs(packageDir) {
		// Under Bazel the compile-time path is workspace-relative; the test
		// binary runs from the package's runfiles tree, where the sources
		// arrive as data (repo_go_srcs in BUILD.bazel).
		packageDir = "."
	}
	sources := []string{
		"sweep.go", "sweep_darwin.go", "sweep_linux.go", "sweep_other.go",
		"sweep_suite.go", "sweep_suite_other.go", "sweep_suite_unix.go",
		"doltserver.go", "doltserver_unix.go", "doltserver_windows.go",
	}
	removed := []string{
		"SweepOrphanedTestServers", "selectOrphanTestServers", "tempDirRoots",
		"isCredibleTempRoot", "isSandboxHome", "isUnderFixedTempRoots",
		"fixedTempRoots", "isSharedTempRoot", "sharedTempRoots",
		"isEchoedAccountHome", "accountHomeDir", "homeIsSuiteRoot",
		"suiteHomeWitness", "isTopLevelDir",
	}
	for _, name := range sources {
		data, err := os.ReadFile(filepath.Join(packageDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, ident := range removed {
			if strings.Contains(string(data), ident) {
				t.Errorf("%s still references the removed global-sweep identifier %s", name, ident)
			}
		}
	}
}
