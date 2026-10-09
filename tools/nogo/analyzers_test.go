package nogo_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/steveyegge/beads/tools/nogo/analyzers/depguard"
	"github.com/steveyegge/beads/tools/nogo/analyzers/errcheck"
	"github.com/steveyegge/beads/tools/nogo/analyzers/forbidigo"
	"github.com/steveyegge/beads/tools/nogo/analyzers/gosec"
	"github.com/steveyegge/beads/tools/nogo/analyzers/misspell"
	"github.com/steveyegge/beads/tools/nogo/analyzers/sloglint"
	"github.com/steveyegge/beads/tools/nogo/analyzers/unconvert"
	"github.com/steveyegge/beads/tools/nogo/analyzers/unparam"
	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
)

// starlarkList returns the string elements of `name = [...]` in analyzers.bzl.
func starlarkList(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile("analyzers.bzl")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?ms)^` + name + ` = \[(.*?)^\]`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("analyzers.bzl has no %s list", name)
	}
	var out []string
	for _, s := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		out = append(out, string(s[1]))
	}
	return out
}

// TestGolangciLintersAreTheEnabledOnes keeps analyzers.bzl's GOLANGCI_LINTERS
// equal to .golangci.yml's linters.enable: a linter enabled there and missing
// here would silently stop gating, and one listed here but not enabled would
// do nothing (Wrap skips it).
func TestGolangciLintersAreTheEnabledOnes(t *testing.T) {
	data, err := os.ReadFile("../../.golangci.yml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := golangci.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	enabled := slices.Sorted(slices.Values(cfg.Linters.Enable))
	listed := slices.Sorted(slices.Values(starlarkList(t, "GOLANGCI_LINTERS")))
	if !slices.Equal(enabled, listed) {
		t.Errorf(".golangci.yml enables %v; analyzers.bzl's GOLANGCI_LINTERS is %v", enabled, listed)
	}
}

// Each wrapper is named after its golangci-lint linter, which is what
// //nolint:<linter> directives and .golangci.yml's exclusion rules name.
func TestWrapperNamesAreLinterNames(t *testing.T) {
	for _, a := range []*analysis.Analyzer{
		depguard.Analyzer, errcheck.Analyzer, forbidigo.Analyzer, gosec.Analyzer,
		misspell.Analyzer, sloglint.Analyzer, unconvert.Analyzer, unparam.Analyzer,
	} {
		if !slices.Contains(starlarkList(t, "GOLANGCI_LINTERS"), a.Name) {
			t.Errorf("analyzer %q is not a GOLANGCI_LINTERS entry", a.Name)
		}
	}
}

// The embedded copy Wrap applies is the repository's .golangci.yml.
func TestEmbeddedConfigIsTheRepositoryConfig(t *testing.T) {
	data, err := os.ReadFile("../../.golangci.yml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := golangci.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := golangci.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Linters.Enable, want.Linters.Enable) || len(got.Linters.Exclusions.Rules) != len(want.Linters.Exclusions.Rules) {
		t.Errorf("embedded .golangci.yml differs from the repository's")
	}
}

// config.json's _base scope: external repos, Bazel outputs and testdata only.
func TestBaseScope(t *testing.T) {
	data, err := os.ReadFile("config.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"^external/"`, `"(^|/)bazel-out/"`, `"(^|/)testdata/"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config.json does not exclude %s", want)
		}
	}
}
