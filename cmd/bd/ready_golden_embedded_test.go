//go:build cgo

package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var updateReadyGolden = flag.Bool("update-ready-golden", false, "rewrite cmd/bd/testdata/ready_golden/*.golden from the bd binary under test")

// readyGoldenTimestamp matches every RFC 3339 timestamp bd prints, which is the
// only nondeterminism in a seeded workspace's ready output besides tips.
var readyGoldenTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)

// readyGoldenTip matches the rotating tip the text renderings may append.
var readyGoldenTip = regexp.MustCompile(`\n💡 Tip: [^\n]*\n`)

// TestEmbeddedReadyGoldenOutput pins `bd ready`'s direct-route output byte for
// byte — JSON, text, pretty, brief, the truncation hint and the pagination
// envelope — across representative invocations, including gc's exact
// `bd ready --json --include-ephemeral --limit 0` (a plain array).
//
// The golden files were generated from the binary BEFORE `bd ready`'s listing
// moved onto issueops.ReadyLister (-update-ready-golden with
// BEADS_TEST_BD_BINARY pointing at it), so this test is the before/after
// equivalence for that move: the listing changed roles, the output did not.
// sharedReadyGoldenCases were added afterwards and pin the current output on
// both routes; the seed's `external:` row was added at the same time and left
// every earlier golden byte-identical, which is itself the check that the
// exclusion is invisible outside the rows it removes. Exit codes are recorded
// exactly.
func TestEmbeddedReadyGoldenOutput(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gd")

	run := func(args ...string) {
		t.Helper()
		if out, err := bdRunWithFlockRetry(t, bd, dir, args...); err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	seedReadyGoldenWorkspace(t, dir, newReadyGoldenForeignProject(t, bd), run)

	cases := append([]readyGoldenCase{
		{name: "json_default", args: []string{"ready", "--json"}},
		{name: "json_limit2", args: []string{"ready", "--json", "--limit", "2"}},
		{name: "json_limit2_envelope", args: []string{"ready", "--json", "--limit", "2"}, env: []string{"BD_JSON_ENVELOPE=1"}},
		{name: "json_gc_include_ephemeral_limit0", args: []string{"ready", "--json", "--include-ephemeral", "--limit", "0"}},
		{name: "json_brief", args: []string{"ready", "--json", "--brief", "--limit", "0"}},
		{name: "json_label", args: []string{"ready", "--json", "--label", "team-a"}},
		{name: "json_assignee_priority_sort", args: []string{"ready", "--json", "--assignee", "alice", "--sort", "priority"}},
		{name: "json_empty", args: []string{"ready", "--json", "--label", "nobody"}},
		{name: "text_default", args: []string{"ready"}},
		{name: "text_limit2", args: []string{"ready", "--limit", "2"}},
		{name: "text_pretty", args: []string{"ready", "--pretty"}},
		{name: "text_pretty_limit2", args: []string{"ready", "--pretty", "--limit", "2"}},
		{name: "text_include_ephemeral", args: []string{"ready", "--include-ephemeral", "--limit", "0"}},
		{name: "text_empty", args: []string{"ready", "--label", "nobody"}},
		{name: "text_max_rows_refused", args: []string{"ready", "--limit", "0", "--max-rows", "2"}},
		{name: "json_max_rows_refused", args: []string{"ready", "--json", "--limit", "0", "--max-rows", "2"}},
	}, sharedReadyGoldenCases...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bd, tc.args...)
			cmd.Dir = filepath.Join(dir, tc.sub)
			cmd.Env = append(bdEnv(dir), tc.env...)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			checkReadyGolden(t, "ready_golden", tc, stdout.String(), stderr.String(), err)
		})
	}
}

// readyGoldenCase is one pinned `bd ready` invocation. sub, when set, is the
// subdirectory of the workspace root it runs from.
type readyGoldenCase struct {
	name string
	args []string
	env  []string
	sub  string
}

// readyGoldenScopeDir is the subdirectory the seed's directory.labels maps to
// team-b (GH#541): a `bd ready` run from it scopes to that label unless the
// command line names labels of its own. The name is distinctive because the
// match is a suffix/substring test against the whole cwd.
const readyGoldenScopeDir = "golden-scope-dir"

// sharedReadyGoldenCases are pinned on BOTH routes, over the same seed:
// --plain, the directory-label default, the external-dependency exclusion,
// and a --limit equal to the ready total (no hint, no pagination key).
var sharedReadyGoldenCases = []readyGoldenCase{
	{name: "text_plain", args: []string{"ready", "--plain"}},
	{name: "text_plain_limit2", args: []string{"ready", "--plain", "--limit", "2"}},
	{name: "json_directory_label", args: []string{"ready", "--json"}, sub: readyGoldenScopeDir},
	{name: "text_directory_label", args: []string{"ready", "--plain"}, sub: readyGoldenScopeDir},
	{name: "json_directory_label_overridden", args: []string{"ready", "--json", "--label", "team-a"}, sub: readyGoldenScopeDir},
	{name: "json_external_excluded", args: []string{"ready", "--json", "--limit", "0", "--priority", "0"}},
	{name: "json_limit5_equals_total_envelope", args: []string{"ready", "--json", "--limit", "5"}, env: []string{"BD_JSON_ENVELOPE=1"}},
	{name: "text_limit5_equals_total", args: []string{"ready", "--limit", "5"}},
}

// checkReadyGolden compares (or, under -update-ready-golden, writes) one
// invocation's stdout, stderr and EXACT exit code against
// testdata/<goldenDir>/<name>.golden.
func checkReadyGolden(t *testing.T, goldenDir string, tc readyGoldenCase, stdout, stderr string, runErr error) {
	t.Helper()
	exit := "0"
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("bd %s did not run: %v", strings.Join(tc.args, " "), runErr)
		}
		exit = strconv.Itoa(exitErr.ExitCode())
	}
	where := ""
	if tc.sub != "" {
		where = "(in " + tc.sub + ") "
	}
	got := "$ " + where + "bd " + strings.Join(tc.args, " ") + "\n" +
		"--- exit: " + exit + "\n" +
		"--- stdout\n" + normalizeReadyGolden(stdout) +
		"--- stderr\n" + normalizeReadyGolden(stderr)
	path := filepath.Join(packageDir(t), "testdata", goldenDir, tc.name+".golden")
	if *updateReadyGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update-ready-golden): %v", err)
	}
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("bd %s output changed.\n--- got\n%s\n--- want\n%s", strings.Join(tc.args, " "), got, want)
	}
}

func normalizeReadyGolden(s string) string {
	s = readyGoldenTimestamp.ReplaceAllString(s, "<TS>")
	s = readyGoldenTip.ReplaceAllString(s, "")
	return s
}

// newReadyGoldenForeignProject creates the external project the golden
// workspace's `external:` edge points at: an embedded workspace whose only
// issue claims to provide "capability" but is still OPEN, so the edge resolves
// — without the unavailable-project warning an unconfigured project would
// print on every listing — and resolves as unsatisfied.
func newReadyGoldenForeignProject(t *testing.T, bd string) string {
	t.Helper()
	foreign, _, _ := bdInit(t, bd, "--prefix", "gf")
	if out, err := bdRunWithFlockRetry(t, bd, foreign, "create", "--silent", "--id", "gf-cap", "Provides capability", "--labels", "provides:capability"); err != nil {
		t.Fatalf("seed the foreign project: %v\n%s", err, out)
	}
	return foreign
}

// seedReadyGoldenWorkspace creates the workspace both golden tests list: five
// ready rows at distinct priorities, one locally blocked row, one row held
// back by an unsatisfied `external:` dependency on foreignDir, an epic with a
// child and a wisp — plus a directory.labels entry mapping readyGoldenScopeDir
// to team-b. run executes one bd command in dir and fails the test on error.
func seedReadyGoldenWorkspace(t *testing.T, dir, foreignDir string, run func(args ...string)) {
	t.Helper()
	// Distinct priorities where the hybrid order would otherwise fall back to
	// creation time, so the order is a property of the data.
	run("create", "--silent", "--id", "gd-alpha", "Alpha task", "--type", "task", "--priority", "1", "--labels", "team-a", "--estimate", "30")
	run("create", "--silent", "--id", "gd-bug", "Crash on start", "--type", "bug", "--priority", "0", "--description", "long text that --brief drops")
	run("create", "--silent", "--id", "gd-feat", "Shiny feature", "--type", "feature", "--priority", "2", "--assignee", "alice", "--labels", "team-a,team-b")
	run("create", "--silent", "--id", "gd-blocked", "Blocked task", "--type", "task", "--priority", "0", "--deps", "blocked-by:gd-alpha")
	run("create", "--silent", "--id", "gd-epic", "Big epic", "--type", "epic", "--priority", "3")
	run("create", "--silent", "Epic child", "--type", "task", "--priority", "4", "--parent", "gd-epic")
	run("create", "--silent", "--id", "gd-wisp", "Ephemeral step", "--type", "task", "--priority", "2", "--ephemeral")
	run("create", "--silent", "--id", "gd-ext", "Waits on another project", "--type", "task", "--priority", "0", "--deps", "blocked-by:external:foreign:capability")

	if err := os.MkdirAll(filepath.Join(dir, readyGoldenScopeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".beads", "config.yaml")
	existing, err := os.ReadFile(cfg)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	scoped := append(existing, []byte("\ndirectory:\n  labels:\n    "+readyGoldenScopeDir+": team-b\n"+
		"external_projects:\n  foreign: "+strconv.Quote(foreignDir)+"\n")...)
	if err := os.WriteFile(cfg, scoped, 0o644); err != nil {
		t.Fatal(err)
	}
}
