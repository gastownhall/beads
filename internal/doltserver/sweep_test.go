package doltserver

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSelectOrphanTestServerPIDs pins down the safety-critical selection
// logic used by SweepOrphanedTestServers: only cmdlines that look like a
// dolt sql-server are candidates at all, and a *live* (non-deleted-cwd) one
// is only reaped when its cwd is nested under a root the caller explicitly
// vouches for as its own suite — never merely because it sits somewhere
// under a shared/global temp dir. That distinction is the whole point: a
// parallel test run (scripts/test.sh -p N) has many suites with live
// servers all living under os.TempDir(), and only a suite's own scoped
// root may reap its own debris, not everyone else's (gastownhall/beads
// mybd-q6cz).
//
// The second bound is on the deleted-cwd arm: a deleted working directory
// only counts as debris when the path it named was under a TEMP root. A
// production server is spawned with cmd.Dir = <workspace>/.beads/dolt, so
// without that bound a moved workspace or an unmounted volume would make a
// developer's live server look exactly like test debris (wy-j2zc8q).
func TestSelectOrphanTestServerPIDs(t *testing.T) {
	cases := []struct {
		name       string
		candidates []serverCandidate
		suiteRoots []string
		tempRoots  []string
		want       []int
	}{
		{
			name: "deleted cwd is reaped even with no suite roots at all",
			candidates: []serverCandidate{
				{pid: 100, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/beads-bd-tests-xyz/.beads/dolt", cwdDeleted: true},
			},
			suiteRoots: nil,
			tempRoots:  []string{"/tmp"},
			want:       []int{100},
		},
		{
			name: "live server under the caller's own scoped root is reaped",
			candidates: []serverCandidate{
				{pid: 101, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/my-suite-root/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       []int{101},
		},
		{
			name: "live server under a DIFFERENT suite's temp dir is NOT reaped, even though both sit under the same global temp dir",
			candidates: []serverCandidate{
				{pid: 102, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/other-suite-xyz/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "debug-mode cmdline with flags before sql-server is still matched when under the scoped root",
			candidates: []serverCandidate{
				{pid: 103, cmdline: "dolt --prof cpu --prof-path /tmp/my-suite-root/dolt-pprof sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/my-suite-root/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       []int{103},
		},
		{
			name: "production server outside any suite root is never reaped",
			candidates: []serverCandidate{
				{pid: 200, cmdline: "dolt sql-server -H 127.0.0.1 -P 3307", cwd: "/home/dev/project/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "non-dolt process under the scoped root is ignored",
			candidates: []serverCandidate{
				{pid: 201, cmdline: "some-other-binary --flag", cwd: "/tmp/my-suite-root/whatever"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "dolt process without sql-server subcommand is ignored",
			candidates: []serverCandidate{
				{pid: 202, cmdline: "dolt status", cwd: "/tmp/my-suite-root/whatever"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "empty cwd and not deleted is left alone (unknown, not provably debris)",
			candidates: []serverCandidate{
				{pid: 203, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: ""},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "scoped-root sibling path is not treated as under the root",
			candidates: []serverCandidate{
				{pid: 204, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/my-suite-root2/evil"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "no suite roots configured: only the deleted-cwd signal reaps anything",
			candidates: []serverCandidate{
				{pid: 205, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/some-suite/.beads/dolt"},
			},
			suiteRoots: nil,
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "mixed candidates: production dir, another suite's live server, this suite's live server, and a deleted-cwd orphan",
			candidates: []serverCandidate{
				{pid: 300, cmdline: "dolt sql-server -P 1", cwd: "/home/dev/real-project/.beads/dolt"},
				{pid: 301, cmdline: "dolt sql-server -P 2", cwd: "/tmp/other-suite-abc/.beads/dolt"},
				{pid: 302, cmdline: "dolt sql-server -P 3", cwd: "/tmp/my-suite-root/.beads/dolt"},
				{pid: 303, cmdline: "dolt sql-server -P 4", cwdDeleted: true, cwd: "/tmp/whatever-else/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp"},
			want:       []int{302, 303},
		},
		{
			name: "a PRODUCTION server whose workspace was moved or deleted is left alone",
			candidates: []serverCandidate{
				{pid: 400, cmdline: "dolt sql-server --config /Users/dev/project/.beads/dolt-server-config.yaml", cwd: "/Users/dev/project/.beads/dolt", cwdDeleted: true},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  []string{"/tmp", "/private/var/folders/c1/xx/T"},
			want:       nil,
		},
		{
			name: "a production server on an unmounted volume is left alone",
			candidates: []serverCandidate{
				{pid: 401, cmdline: "dolt sql-server -P 3307", cwd: "/Volumes/external/work/.beads/dolt", cwdDeleted: true},
			},
			suiteRoots: nil,
			tempRoots:  []string{"/tmp"},
			want:       nil,
		},
		{
			name: "the observed orphan: deleted cwd under the macOS private temp form is reaped with no suite roots",
			candidates: []serverCandidate{
				{pid: 402, cmdline: "dolt sql-server --config /private/var/folders/c1/xx/T/TestUOWDependencyEditorContract1/003/config.yaml", cwd: "/private/var/folders/c1/xx/T/TestUOWDependencyEditorContract1/002", cwdDeleted: true},
			},
			suiteRoots: nil,
			tempRoots:  []string{"/var/folders/c1/xx/T", "/private/var/folders/c1/xx/T"},
			want:       []int{402},
		},
		{
			name: "the same orphan reported in the unresolved /var form is reaped too",
			candidates: []serverCandidate{
				{pid: 403, cmdline: "dolt sql-server -P 1", cwd: "/var/folders/c1/xx/T/TestUOWDependencyEditorContract1/002", cwdDeleted: true},
			},
			suiteRoots: nil,
			tempRoots:  []string{"/var/folders/c1/xx/T", "/private/var/folders/c1/xx/T"},
			want:       []int{403},
		},
		{
			name: "a deleted cwd inside the caller's own suite root needs no temp root at all",
			candidates: []serverCandidate{
				{pid: 404, cmdline: "dolt sql-server -P 1", cwd: "/opt/scratch/my-suite-root/.beads/dolt", cwdDeleted: true},
			},
			suiteRoots: []string{"/opt/scratch/my-suite-root"},
			tempRoots:  nil,
			want:       []int{404},
		},
		{
			name: "no temp roots at all disables the deleted-cwd arm entirely",
			candidates: []serverCandidate{
				{pid: 405, cmdline: "dolt sql-server -P 1", cwd: "/tmp/some-suite/.beads/dolt", cwdDeleted: true},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			tempRoots:  nil,
			want:       nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectOrphanTestServerPIDs(tc.candidates, tc.suiteRoots, tc.tempRoots)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selectOrphanTestServerPIDs() = %v, want %v", got, tc.want)
			}
		})
	}
}

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

	gotPIDs := selectOrphanTestServerPIDs(candidates, []string{"/tmp/my-suite"}, []string{"/tmp"})
	wantPIDs := []int{101, 103}
	if !reflect.DeepEqual(gotPIDs, wantPIDs) {
		t.Errorf("darwin ps selection path = %v, want %v", gotPIDs, wantPIDs)
	}
}

// TestSelectServersUnderSuiteRoots pins the strictly root-scoped selection
// SweepDeadSuiteRoots relies on. It runs at suite START, with sibling
// packages mid-run, so it must reap only what is provably inside the roots
// it was handed: no deleted-cwd arm, no temp-dir reasoning of any kind.
func TestSelectServersUnderSuiteRoots(t *testing.T) {
	candidates := []serverCandidate{
		{pid: 500, cmdline: "dolt sql-server -P 1", cwd: "/tmp/dead-root/.beads/dolt"},
		{pid: 501, cmdline: "dolt sql-server -P 2", cwd: "/tmp/dead-root/nested/deeper/dolt", cwdDeleted: true},
		{pid: 502, cmdline: "dolt sql-server -P 3", cwd: "/tmp/live-sibling-root/.beads/dolt"},
		{pid: 503, cmdline: "dolt sql-server -P 4", cwd: "/tmp/some-other-suite/.beads/dolt", cwdDeleted: true},
		{pid: 504, cmdline: "dolt sql-server -P 5", cwd: "/Users/dev/project/.beads/dolt", cwdDeleted: true},
		{pid: 505, cmdline: "not-dolt --flag", cwd: "/tmp/dead-root/whatever"},
		{pid: 506, cmdline: "dolt sql-server -P 6", cwd: ""},
	}

	got := selectServersUnderRoots(candidates, []string{"/tmp/dead-root"})
	want := []int{500, 501}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectServersUnderRoots() = %v, want %v", got, want)
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

// TestTempDirRootsBoundTheOrphanArm checks that the deleted-cwd bound
// actually covers the directory this process's own t.TempDir() lands in —
// if it did not, the arm would silently never fire.
func TestTempDirRootsBoundTheOrphanArm(t *testing.T) {
	roots := tempDirRoots()
	if len(roots) == 0 {
		t.Fatal("tempDirRoots() is empty; the deleted-cwd arm could never fire")
	}
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	if !underAnyRoot(dir, roots) && !underAnyRoot(resolved, roots) {
		t.Errorf("t.TempDir() %q (resolved %q) is under none of tempDirRoots() %v", dir, resolved, roots)
	}
	if underAnyRoot("/Users/dev/project/.beads/dolt", roots) {
		t.Errorf("a production workspace path matched tempDirRoots() %v", roots)
	}
}

// TestTempDirRootsRejectsOverbroadTMPDIR covers the bound on the bound.
// TMPDIR is an ordinary environment variable, so os.TempDir() can be handed
// "/" or a home directory; either would put every workspace on the box back
// inside the deleted-cwd arm, which is precisely what tempDirRoots exists to
// prevent. /tmp and the real per-user temp dir must survive that filter.
func TestTempDirRootsRejectsOverbroadTMPDIR(t *testing.T) {
	// A real, non-temp home. The ambient one cannot be trusted for these
	// expectations: CI (scripts/ci/lib/test-env.sh) runs the suite with HOME
	// under /tmp, where "a workspace under the home directory" and "a path
	// under /tmp" are the same path.
	const home = "/home/beads-fixture"
	t.Setenv("HOME", home)

	t.Run("normal TMPDIR keeps a usable root", func(t *testing.T) {
		roots := tempDirRoots()
		if len(roots) == 0 {
			t.Fatal("tempDirRoots() is empty for the ambient TMPDIR")
		}
		for _, root := range roots {
			if root == string(filepath.Separator) {
				t.Errorf("tempDirRoots() = %v, which includes the filesystem root", roots)
			}
			if isUnderDir(filepath.Clean(home), root) {
				t.Errorf("tempDirRoots() root %q contains the home directory %q", root, home)
			}
		}
	})

	t.Run("TMPDIR=/ is dropped", func(t *testing.T) {
		t.Setenv("TMPDIR", "/")
		roots := tempDirRoots()
		for _, root := range roots {
			if filepath.Clean(root) == string(filepath.Separator) {
				t.Fatalf("tempDirRoots() = %v, want the filesystem root dropped", roots)
			}
		}
		// The hardcoded /tmp fallback still has to come through, or the
		// deleted-cwd arm would quietly stop working.
		if !underAnyRoot("/tmp/beads-bd-tests-xyz/.beads/dolt", roots) {
			t.Errorf("tempDirRoots() = %v, want /tmp still covered", roots)
		}
		if underAnyRoot(filepath.Join(home, "project", ".beads", "dolt"), roots) {
			t.Errorf("tempDirRoots() = %v still covers a path under the home directory", roots)
		}
	})

	t.Run("TMPDIR=$HOME is dropped", func(t *testing.T) {
		t.Setenv("TMPDIR", home)
		roots := tempDirRoots()
		if underAnyRoot(filepath.Join(home, "project", ".beads", "dolt"), roots) {
			t.Errorf("tempDirRoots() = %v covers a workspace under the home directory", roots)
		}
		if !underAnyRoot("/tmp/beads-bd-tests-xyz/.beads/dolt", roots) {
			t.Errorf("tempDirRoots() = %v, want /tmp still covered", roots)
		}
	})

	// The CI shape: HOME is a throwaway directory under /tmp. /tmp contains
	// it, and must survive anyway — this is the case that emptied
	// tempDirRoots on every Linux runner and disabled the arm there.
	t.Run("sandbox HOME under /tmp keeps /tmp", func(t *testing.T) {
		sandbox := "/tmp/beads-test-env-abc123/home"
		t.Setenv("HOME", sandbox)
		roots := tempDirRoots()
		if !underAnyRoot("/tmp/beads-bd-tests-xyz/.beads/dolt", roots) {
			t.Errorf("tempDirRoots() = %v with HOME=%s, want /tmp still covered", roots, sandbox)
		}
		for _, root := range roots {
			if filepath.Clean(root) == string(filepath.Separator) {
				t.Errorf("tempDirRoots() = %v, which includes the filesystem root", roots)
			}
		}
	})

	// TMPDIR=$HOME with a sandbox home: the root IS the home, so it is
	// dropped even though a sandbox home is otherwise ignored.
	t.Run("TMPDIR=sandbox HOME is dropped", func(t *testing.T) {
		sandbox := "/tmp/beads-test-env-abc123/home"
		t.Setenv("HOME", sandbox)
		t.Setenv("TMPDIR", sandbox)
		for _, root := range tempDirRoots() {
			if filepath.Clean(root) == sandbox {
				t.Errorf("tempDirRoots() kept %q, the sandbox home itself", root)
			}
		}
	})
}

// TestIsCredibleTempRoot tables the predicate directly, including the shapes
// no environment on this box can produce.
func TestIsCredibleTempRoot(t *testing.T) {
	const home = "/Users/dev"
	cases := []struct {
		root string
		want bool
	}{
		{"/tmp", true},
		{"/private/tmp", true},
		{"/var/folders/c1/xx/T", true},
		{"/var/folders/c1/xx/T/beads-bd-tests-1", true},
		{"/", false},
		{"//", false},
		{"", false},
		{".", false},
		{"relative/tmp", false},
		{home, false},
		{home + "/", false},
		{"/Users", false},
		{"/Users/dev/tmp", true},
		{"/Users/other", true},
	}
	for _, tc := range cases {
		if got := isCredibleTempRoot(tc.root, home); got != tc.want {
			t.Errorf("isCredibleTempRoot(%q, %q) = %v, want %v", tc.root, home, got, tc.want)
		}
	}

	// A sandbox home (CI's HOME under /tmp) does not disqualify the roots
	// that contain it; the root that IS the home is still out, and a real
	// home under an overbroad TMPDIR still rejects that TMPDIR.
	const sandbox = "/tmp/beads-test-env-abc123/home"
	sandboxCases := []struct {
		root, home string
		want       bool
	}{
		{"/tmp", sandbox, true},
		{"/tmp/beads-test-env-abc123", sandbox, true},
		{sandbox, sandbox, false},
		{sandbox + "/", sandbox, false},
		{"/", sandbox, false},
		{"/home", "/home/runner", false},
		{"/tmp", "/tmp", false},
	}
	for _, tc := range sandboxCases {
		if got := isCredibleTempRoot(tc.root, tc.home); got != tc.want {
			t.Errorf("isCredibleTempRoot(%q, %q) = %v, want %v", tc.root, tc.home, got, tc.want)
		}
	}

	// With no home known, only the structural rejections apply.
	if !isCredibleTempRoot("/Users/dev", "") {
		t.Error("isCredibleTempRoot with no home should accept an ordinary absolute path")
	}
	if isCredibleTempRoot("/", "") {
		t.Error("isCredibleTempRoot must reject the filesystem root even with no home")
	}
}
