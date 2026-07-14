package doltserver

import (
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
func TestSelectOrphanTestServerPIDs(t *testing.T) {
	cases := []struct {
		name       string
		candidates []serverCandidate
		suiteRoots []string
		want       []int
	}{
		{
			name: "deleted cwd is reaped even with no suite roots at all",
			candidates: []serverCandidate{
				{pid: 100, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/beads-bd-tests-xyz/.beads/dolt", cwdDeleted: true},
			},
			suiteRoots: nil,
			want:       []int{100},
		},
		{
			name: "live server under the caller's own scoped root is reaped",
			candidates: []serverCandidate{
				{pid: 101, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/my-suite-root/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       []int{101},
		},
		{
			name: "live server under a DIFFERENT suite's temp dir is NOT reaped, even though both sit under the same global temp dir",
			candidates: []serverCandidate{
				{pid: 102, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/other-suite-xyz/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       nil,
		},
		{
			name: "debug-mode cmdline with flags before sql-server is still matched when under the scoped root",
			candidates: []serverCandidate{
				{pid: 103, cmdline: "dolt --prof cpu --prof-path /tmp/my-suite-root/dolt-pprof sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/my-suite-root/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       []int{103},
		},
		{
			name: "production server outside any suite root is never reaped",
			candidates: []serverCandidate{
				{pid: 200, cmdline: "dolt sql-server -H 127.0.0.1 -P 3307", cwd: "/home/dev/project/.beads/dolt"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       nil,
		},
		{
			name: "non-dolt process under the scoped root is ignored",
			candidates: []serverCandidate{
				{pid: 201, cmdline: "some-other-binary --flag", cwd: "/tmp/my-suite-root/whatever"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       nil,
		},
		{
			name: "dolt process without sql-server subcommand is ignored",
			candidates: []serverCandidate{
				{pid: 202, cmdline: "dolt status", cwd: "/tmp/my-suite-root/whatever"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       nil,
		},
		{
			name: "empty cwd and not deleted is left alone (unknown, not provably debris)",
			candidates: []serverCandidate{
				{pid: 203, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: ""},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       nil,
		},
		{
			name: "scoped-root sibling path is not treated as under the root",
			candidates: []serverCandidate{
				{pid: 204, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/my-suite-root2/evil"},
			},
			suiteRoots: []string{"/tmp/my-suite-root"},
			want:       nil,
		},
		{
			name: "no suite roots configured: only the deleted-cwd signal reaps anything",
			candidates: []serverCandidate{
				{pid: 205, cmdline: "dolt sql-server -H 127.0.0.1 -P 12345", cwd: "/tmp/some-suite/.beads/dolt"},
			},
			suiteRoots: nil,
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
			want:       []int{302, 303},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectOrphanTestServerPIDs(tc.candidates, tc.suiteRoots)
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
