// Command revdepscope computes the reverse-dependency-scoped set of Go
// packages that need their tests re-run for a given set of changed
// packages. See be-mv0ww.3's design (bd show be-mv0ww.3) for the full
// rationale; FR-1 is why this uses `go list -test -deps` rather than a
// build-only or single-hop import scan.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type scopeResult struct {
	fullSuiteRequired bool
	packages          []string
}

// scope returns the union of changedPackages and every package in
// allPackages whose entry in deps (its go-list-test-deps closure, already
// transitive) contains a changed package. deps[pkg] not containing pkg
// itself is fine; changedPackages are unioned in explicitly.
func scope(allPackages, changedPackages []string, deps map[string][]string, threshold float64) scopeResult {
	changed := make(map[string]bool, len(changedPackages))
	for _, pkg := range changedPackages {
		changed[pkg] = true
	}

	result := make(map[string]bool, len(changed))
	for pkg := range changed {
		result[pkg] = true
	}
	for pkg, pkgDeps := range deps {
		for _, dep := range pkgDeps {
			if changed[dep] {
				result[pkg] = true
				break
			}
		}
	}

	if len(result) == 0 {
		return scopeResult{}
	}

	if total := len(allPackages); total > 0 && float64(len(result))/float64(total) > threshold {
		return scopeResult{fullSuiteRequired: true}
	}

	packages := make([]string, 0, len(result))
	for pkg := range result {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	return scopeResult{packages: packages}
}

// parseThreshold parses the configured threshold fraction. Empty means the
// FR-3 default of 50%.
func parseThreshold(raw string) (float64, error) {
	if raw == "" {
		return 0.5, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid threshold %q: %w", raw, err)
	}
	if v <= 0 || v > 1 {
		return 0, fmt.Errorf("threshold %q out of range: must be > 0 and <= 1", raw)
	}
	return v, nil
}

var cachedModulePath string

func modulePath() (string, error) {
	if cachedModulePath != "" {
		return cachedModulePath, nil
	}
	out, err := exec.Command("go", "list", "-m").Output()
	if err != nil {
		return "", fmt.Errorf("go list -m: %w", err)
	}
	cachedModulePath = strings.TrimSpace(string(out))
	return cachedModulePath, nil
}

// listAllPackages lists every package in the module. It uses the
// module-qualified pattern (rather than "./...") so the result does not
// depend on the caller's working directory — "./..." is relative to cwd,
// which e.g. `go test` sets to the package's own source directory rather
// than the module root.
func listAllPackages() ([]string, error) {
	mod, err := modulePath()
	if err != nil {
		return nil, err
	}
	pattern := mod + "/..."
	out, err := exec.Command("go", "list", pattern).Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w", pattern, err)
	}
	return splitLines(string(out)), nil
}

// listTestDeps returns pkg's go-list-test-deps closure, filtered to
// packages within this module (NFR-2: single-module scope).
func listTestDeps(pkg string) ([]string, error) {
	mod, err := modulePath()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("go", "list", "-test", "-deps", pkg).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list -test -deps %s: %w: %s", pkg, err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("go list -test -deps %s: %w", pkg, err)
	}
	var internal []string
	for _, line := range splitLines(string(out)) {
		if line == mod || strings.HasPrefix(line, mod+"/") {
			internal = append(internal, line)
		}
	}
	return internal, nil
}

func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: revdepscope <changed-package-import-path> [...]")
		os.Exit(2)
	}
	changedPackages := os.Args[1:]

	threshold, err := parseThreshold(os.Getenv("REVDEPSCOPE_THRESHOLD"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	allPackages, err := listAllPackages()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	deps := make(map[string][]string, len(allPackages))
	for _, pkg := range allPackages {
		d, err := listTestDeps(pkg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		deps[pkg] = d
	}

	res := scope(allPackages, changedPackages, deps, threshold)
	if res.fullSuiteRequired {
		fmt.Println("full suite required")
		return
	}
	for _, pkg := range res.packages {
		fmt.Println(pkg)
	}
}
