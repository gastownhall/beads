package scripts_test

// T4 (TestNogoIntegrationStepCoversItsConfiguration), the integration/test
// file source scans it shares with T6
// (TestUnvalidatedIntegrationTestsFileIsExact), and the scan's own unit test
// live in their own file and go_test target (cost fix, independent review of
// #7486): both scans read //:repo_go_srcs and //:repo_go_test_srcs (every
// tracked .go file, test and non-test), so putting them in scripts_test
// alongside everything else made ANY Go source change re-run the whole
// nogo-policy suite (scripts_test's rerun rate rose from 73% to 91% of main
// commits). This target alone depends on those two data partitions; nothing
// else in scripts/BUILD.bazel needs to.

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// goLibraryNamePattern finds a go_library rule's own "name" attribute,
// assumed to be the first go_library block in the file (every scanned
// package here is a single-package Go directory with at most one
// non-test go_library). Used by integrationOnlyLibraryTargets (independent
// review of #7486, must-fix 2) to pin the nogo-only step's exact target
// label, not just its package: a package-only comparison would accept the
// step retargeted at any other rule in the same package (e.g. the
// package's :repo_doc_files filegroup), silently validating nothing.
var goLibraryNamePattern = regexp.MustCompile(`go_library\(\s*\n\s*name = "([^"]+)"`)

// integrationOnlyLibraryTargets pairs each of integrationOnlyLibraryPackages'
// packages with its go_library target name, read from the package's own
// BUILD.bazel, returning full Bazel labels ("//dir:name"). A bare package
// label does not pin which target within it the nogo-only step must build.
func integrationOnlyLibraryTargets(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, pkg := range integrationOnlyLibraryPackages(t, root) {
		dir := strings.TrimPrefix(pkg, "//")
		build := readPolicyFile(t, root, filepath.Join(filepath.FromSlash(dir), "BUILD.bazel"))
		m := goLibraryNamePattern.FindStringSubmatch(build)
		if m == nil {
			t.Fatalf("%s has no go_library rule in its BUILD.bazel; integrationOnlyLibraryPackages found it by source files, but there is no target to pin a label for", pkg)
		}
		out = append(out, pkg+":"+m[1])
	}
	sort.Strings(out)
	return out
}

// T4 TestNogoIntegrationStepCoversItsConfiguration: f5-spec.md §6 T4
// (Variant A). bazel-integration's nogo-only step's target set must be
// exactly the integration configuration's non-test library packages, found
// by a source scan (go/build's own tag matching, diffed with and without
// the "integration" tag) rather than hardcoded, so a new integration-tagged
// library file lands in the nogo-only step automatically instead of
// silently losing coverage, and a file that stops needing the tag drops
// out instead of the step validating a target gazelle no longer builds that
// way.
//
// The comparison is by exact label ("//pkg:name"), not package alone, and a
// "-//" exclusion pattern on the step's command line is rejected outright,
// as TestNogoRaceOwnerBuildsEverything (T3) already does for the race
// owner's own target set (independent review of #7486, must-fix 2): a
// package-only match would accept the step retargeted at a different rule
// in the same package (e.g. :repo_doc_files), or let a "-//..." pattern
// silently exclude a target the source scan found, with nothing here
// noticing either way.
func TestNogoIntegrationStepCoversItsConfiguration(t *testing.T) {
	root := bazelPolicyRoot(t)
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelIntegJobName)

	var nogoArgs []string
	for _, st := range job.Steps {
		for _, args := range bazelInvocations(st.Run, "build") {
			if slices.Contains(args, "--output_groups=nogo_fix") {
				nogoArgs = args
			}
		}
	}
	if nogoArgs == nil {
		t.Fatalf("%s has no `bazel build --output_groups=nogo_fix` step", bazelIntegJobName)
	}

	got := map[string]bool{}
	for _, a := range nogoArgs {
		switch {
		case strings.HasPrefix(a, "-//"):
			t.Errorf("%s's nogo-only step excludes a target (%s) with a negative pattern; every target the source scan found must be built, not excluded", bazelIntegJobName, a)
		case strings.HasPrefix(a, "//"):
			got[a] = true
		}
	}

	want := integrationOnlyLibraryTargets(t, root)
	if len(want) == 0 {
		t.Fatal("source scan found no integration-tagged library target; the scan itself is broken")
	}
	for _, target := range want {
		if !got[target] {
			t.Errorf("source scan found integration-only go_library target %s with no exact match in %s's nogo-only step (targets: %v)", target, bazelIntegJobName, nogoArgs)
		}
	}
	for target := range got {
		if !slices.Contains(want, target) {
			t.Errorf("%s's nogo-only step targets %s, which is not one of the exact go_library labels the source scan found (scan found: %v); a retargeted label (even one in the same package) would silently stop validating the real target", bazelIntegJobName, target, want)
		}
	}
}

// TestIntegrationOnlyLibraryPackagesScanConstraints (independent review of
// #7486, must-fix 3 and the bee-ghosttrack nit): unit-tests
// scanIntegrationOnlyLibraryPackages' constraint handling directly, against
// a small synthetic file tree, rather than only indirectly through whatever
// the real repository happens to contain today.
func TestIntegrationOnlyLibraryPackagesScanConstraints(t *testing.T) {
	root := t.TempDir()
	write := func(rel, constraint string) {
		dir := filepath.Join(root, filepath.FromSlash(filepath.Dir(rel)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		pkg := filepath.Base(filepath.Dir(rel))
		src := fmt.Sprintf("package %s\n", pkg)
		if constraint != "" {
			src = fmt.Sprintf("//go:build %s\n\n%s", constraint, src)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// cgo: only shows up once CgoEnabled is true (must-fix 3).
	write("cgocase/f.go", "cgo && integration")
	// race: only shows up once the race-on scan's tags include "race"
	// (must-fix 3).
	write("racecase/f.go", "race && integration")
	// racezero: never compiled by any of the "integ" row's members (they all
	// set race=true), but still a real integration-tagged library file no
	// lane validates; only the race-off scan surfaces it (must-fix 3).
	write("racezerocase/f.go", "!race && integration")
	// gmsonly: requires gms_pure_go but not integration; must NOT be
	// reported, or the "without" context's missing gms_pure_go tag would
	// make it look integration-only (bee-ghosttrack nit on #7486).
	write("gmsonlycase/f.go", "gms_pure_go && !integration")
	// plain: no constraint at all; compiles either way, must NOT be
	// reported.
	write("plaincase/f.go", "")

	files := []string{
		"cgocase/f.go", "racecase/f.go", "racezerocase/f.go",
		"gmsonlycase/f.go", "plaincase/f.go",
	}
	got := scanIntegrationOnlyLibraryPackages(root, files)

	want := []string{"//cgocase", "//racecase", "//racezerocase"}
	sort.Strings(want)
	if !equalStrings(got, want) {
		t.Errorf("scanIntegrationOnlyLibraryPackages = %v, want %v", got, want)
	}
}

// integrationOnlyLibraryPackages scans root for non-test .go files that
// compile only when the "integration" build tag is set (go/build.Context's
// MatchFile, diffed with and without the tag), and returns their Bazel
// package labels ("//dir", repo root relative), sorted and deduplicated. A
// file that compiles either way (no integration constraint), or that exists
// only as a _test.go file, is not included: a _test.go file is not a
// library's ValidateNogo target either way (f5-spec.md §3's accepted loss).
//
// Uses repoFiles (//:repo_go_srcs under Bazel, BUILD.bazel's scripts_test
// data) rather than its own filepath.WalkDir: under Bazel, root is a
// runfiles symlink forest holding only what the go_test declares as data,
// not an arbitrary subtree of the checkout.
func integrationOnlyLibraryPackages(t *testing.T, root string) []string {
	t.Helper()
	return scanIntegrationOnlyLibraryPackages(root, repoFiles(t, root, 500))
}

// scanIntegrationOnlyLibraryPackages is integrationOnlyLibraryPackages' pure
// scan logic, kept apart so TestIntegrationOnlyLibraryPackagesScanConstraints
// can feed it a small synthetic file list instead of the whole repository.
//
// It scans twice, with race on and with race off, and returns the union:
// every member of nogoConfigurations' "integ" row sets
// --@rules_go//go/config:race (so a file gated `race && integration` is only
// ever actually compiled that way, and matching race=off for it would miss
// it), but a file gated `!race && integration` would never be compiled by
// any of that row's members at all -- an uncovered configuration no lane
// validates -- and the race-off scan is what surfaces it here instead of
// silently losing it (independent review of #7486, must-fix 3). Each scan's
// own "without" context carries the other scan's non-integration tags
// (gms_pure_go, and race for the race-on scan) so the with/without diff
// isolates the integration tag alone: an earlier version's "without" had no
// tags at all, so a file gated on gms_pure_go alone (no integration
// constraint) would have shown up as integration-only too (review of #7486,
// bee-ghosttrack nit). CgoEnabled is true in every context: race requires
// cgo, so a file additionally gated `cgo && integration` only shows up with
// it set (independent review of #7486, must-fix 3).
func scanIntegrationOnlyLibraryPackages(root string, files []string) []string {
	return scanIntegrationTaggedFiles(root, files, false)
}

// integrationOnlyTestFiles is scanIntegrationOnlyLibraryPackages' mirror
// image: _test.go files (instead of library files) that require the
// integration build tag, returned as sorted repo-relative paths (instead of
// Bazel package labels, since many such files share a package and T6 of
// must-fix 6 (independent review of #7486) checks the exact file list, not
// the package set). This is tools/nogo/unvalidated_integration_tests.txt's
// own source of truth: TestUnvalidatedIntegrationTestsFileIsExact pins the
// file's content equal to it, so a newly integration-tagged test file lands
// there automatically instead of silently losing coverage with nothing
// making the loss explicit.
func integrationOnlyTestFiles(t *testing.T, root string) []string {
	t.Helper()
	return scanIntegrationTaggedFiles(root, repoFiles(t, root, 500), true)
}

// scanIntegrationTaggedFiles is integrationOnlyLibraryPackages' and
// integrationOnlyTestFiles' shared pure scan logic: see
// scanIntegrationOnlyLibraryPackages' doc comment for the race-on/race-off
// union and CgoEnabled rationale (independent review of #7486, must-fix 3).
// testFiles selects which half of the source tree is scanned (non-test
// library files, or _test.go files) and what each match is reported as
// (a Bazel package label, or a repo-relative file path).
func scanIntegrationTaggedFiles(root string, files []string, testFiles bool) []string {
	scan := func(raceOn bool) []string {
		base := []string{"gms_pure_go"}
		if raceOn {
			base = append(base, "race")
		}
		withTags := append(append([]string{}, base...), "integration")
		without := &build.Context{GOOS: "linux", GOARCH: "amd64", Compiler: "gc", CgoEnabled: true, BuildTags: base}
		with := &build.Context{GOOS: "linux", GOARCH: "amd64", Compiler: "gc", CgoEnabled: true, BuildTags: withTags}

		var out []string
		for _, rel := range files {
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") != testFiles {
				continue
			}
			dir := filepath.ToSlash(filepath.Dir(rel))
			name := filepath.Base(rel)
			dirPath := filepath.Join(root, filepath.FromSlash(dir)) + string(filepath.Separator)
			matchesWithout, errW := without.MatchFile(dirPath, name)
			matchesWith, errT := with.MatchFile(dirPath, name)
			if errW != nil || errT != nil {
				continue // an unparsable file is not this scan's concern
			}
			if !matchesWith || matchesWithout {
				continue
			}
			if testFiles {
				out = append(out, rel)
			} else {
				out = append(out, "//"+dir)
			}
		}
		return out
	}

	seen := map[string]bool{}
	var out []string
	for _, pkg := range append(scan(true), scan(false)...) {
		if !seen[pkg] {
			seen[pkg] = true
			out = append(out, pkg)
		}
	}
	sort.Strings(out)
	return out
}

// TestUnvalidatedIntegrationTestsFileIsExact (independent review of #7486,
// must-fix 6): tools/nogo/unvalidated_integration_tests.txt -- f5-spec.md
// §3's "lost_2a.txt", the list of integration-tagged _test.go files no nogo
// lane validates under Variant A -- was otherwise unread by any test: a file
// could drift arbitrarily out of date with no check noticing. This pins it
// exactly equal to the same source scan TestNogoIntegrationStepCoversItsConfiguration
// uses, so a newly integration-tagged test file (a new loss of coverage)
// fails here explicitly instead of silently not being tracked anywhere.
func TestUnvalidatedIntegrationTestsFileIsExact(t *testing.T) {
	root := bazelPolicyRoot(t)
	const path = "tools/nogo/unvalidated_integration_tests.txt"
	var got []string
	for _, line := range strings.Split(readPolicyFile(t, root, path), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		got = append(got, line)
	}
	sort.Strings(got)

	want := integrationOnlyTestFiles(t, root)
	if len(want) == 0 {
		t.Fatal("source scan found no integration-tagged _test.go file; the scan itself is broken")
	}
	if !equalStrings(got, want) {
		var missing, extra []string
		for _, f := range want {
			if !slices.Contains(got, f) {
				missing = append(missing, f)
			}
		}
		for _, f := range got {
			if !slices.Contains(want, f) {
				extra = append(extra, f)
			}
		}
		t.Errorf("%s does not match the source scan of integration-tagged _test.go files:\n  missing (scan found, file does not list): %v\n  extra (file lists, scan did not find): %v", path, missing, extra)
	}
}
