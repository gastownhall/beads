package main

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func TestScope(t *testing.T) {
	tests := []struct {
		name            string
		allPackages     []string
		changedPackages []string
		deps            map[string][]string
		threshold       float64
		wantFullSuite   bool
		wantPackages    []string
	}{
		{
			name:            "no changed packages yields empty scope",
			allPackages:     []string{"a", "b", "c"},
			changedPackages: nil,
			deps:            map[string][]string{},
			threshold:       0.5,
			wantFullSuite:   false,
			wantPackages:    nil,
		},
		{
			name:            "changed leaf package with no callers scopes to itself",
			allPackages:     []string{"leaf", "other1", "other2"},
			changedPackages: []string{"leaf"},
			deps: map[string][]string{
				"other1": {"unrelated"},
				"other2": {"unrelated"},
			},
			threshold:     0.5,
			wantFullSuite: false,
			wantPackages:  []string{"leaf"},
		},
		{
			name:            "changed package pulls in direct and transitive test-aware callers",
			allPackages:     []string{"schema", "dolt", "embeddeddolt", "uow", "unrelated"},
			changedPackages: []string{"schema"},
			deps: map[string][]string{
				"dolt":         {"schema"},
				"embeddeddolt": {"schema"},
				"uow":          {"dolt"},
				"unrelated":    {"fmt"},
			},
			threshold:     0.9,
			wantFullSuite: false,
			wantPackages:  []string{"dolt", "embeddeddolt", "schema", "uow"},
		},
		{
			name:            "reverse-dep set at exactly the threshold fraction is scoped, not full suite",
			allPackages:     []string{"a", "b", "c", "d"},
			changedPackages: []string{"a"},
			deps: map[string][]string{
				"b": {"a"},
			},
			threshold:     0.5,
			wantFullSuite: false,
			wantPackages:  []string{"a", "b"},
		},
		{
			name:            "reverse-dep set exceeding the threshold fraction requires full suite",
			allPackages:     []string{"a", "b", "c", "d"},
			changedPackages: []string{"a"},
			deps: map[string][]string{
				"b": {"a"},
				"c": {"a"},
			},
			threshold:     0.5,
			wantFullSuite: true,
			wantPackages:  nil,
		},
		{
			name:            "custom threshold changes the outcome for the same graph",
			allPackages:     []string{"a", "b", "c", "d"},
			changedPackages: []string{"a"},
			deps: map[string][]string{
				"b": {"a"},
				"c": {"a"},
			},
			threshold:     0.8,
			wantFullSuite: false,
			wantPackages:  []string{"a", "b", "c"},
		},
		{
			name:            "multiple changed packages union their reverse-dep sets and dedupe",
			allPackages:     []string{"a", "b", "c", "d", "e"},
			changedPackages: []string{"a", "d"},
			deps: map[string][]string{
				"b": {"a"},
				"c": {"a", "d"},
			},
			threshold:     0.9,
			wantFullSuite: false,
			wantPackages:  []string{"a", "b", "c", "d"},
		},
		{
			name:            "caller reaching a changed package only through the supplied test-aware deps is scoped in",
			allPackages:     []string{"schema", "domain_db"},
			changedPackages: []string{"schema"},
			deps: map[string][]string{
				"domain_db": {"schema"},
			},
			threshold:     0.9,
			wantFullSuite: false,
			wantPackages:  []string{"domain_db", "schema"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scope(tt.allPackages, tt.changedPackages, tt.deps, tt.threshold)
			if got.fullSuiteRequired != tt.wantFullSuite {
				t.Fatalf("fullSuiteRequired = %v, want %v", got.fullSuiteRequired, tt.wantFullSuite)
			}
			gotPkgs := append([]string(nil), got.packages...)
			sort.Strings(gotPkgs)
			wantPkgs := append([]string(nil), tt.wantPackages...)
			sort.Strings(wantPkgs)
			if !reflect.DeepEqual(gotPkgs, wantPkgs) {
				t.Fatalf("packages = %v, want %v", gotPkgs, wantPkgs)
			}
		})
	}
}

func TestScopeThresholdIsFractionOfAllPackagesNotReverseDepSet(t *testing.T) {
	all := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		all = append(all, fmt.Sprintf("pkg%d", i))
	}
	deps := map[string][]string{}
	for i := 1; i < 10; i++ {
		deps[fmt.Sprintf("pkg%d", i)] = []string{"pkg0"}
	}

	got := scope(all, []string{"pkg0"}, deps, 0.5)
	if got.fullSuiteRequired {
		t.Fatalf("fullSuiteRequired = true for 10/20 (0.5, not exceeding threshold); want false")
	}
	if len(got.packages) != 10 {
		t.Fatalf("packages = %v (len %d), want 10 packages (pkg0..pkg9)", got.packages, len(got.packages))
	}
}

func TestParseThreshold(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    float64
		wantErr bool
	}{
		{name: "empty uses default", raw: "", want: 0.5},
		{name: "valid custom value", raw: "0.44", want: 0.44},
		{name: "boundary value 1 is valid", raw: "1", want: 1},
		{name: "not a number", raw: "not-a-number", wantErr: true},
		{name: "zero is out of range", raw: "0", wantErr: true},
		{name: "negative is out of range", raw: "-0.1", wantErr: true},
		{name: "above 1 is out of range", raw: "1.5", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseThreshold(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseThreshold(%q) = %v, nil; want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseThreshold(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("parseThreshold(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRealRepoSchemaChangeStaysScoped(t *testing.T) {
	all, err := listAllPackages()
	if err != nil {
		t.Fatalf("listAllPackages: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("listAllPackages returned no packages")
	}

	const target = "github.com/steveyegge/beads/internal/storage/schema"
	found := false
	for _, pkg := range all {
		if pkg == target {
			found = true
			break
		}
	}
	if !found {
		t.Skipf("%s not present in this checkout; skipping real-repo smoke test", target)
	}

	deps := make(map[string][]string, len(all))
	for _, pkg := range all {
		d, err := listTestDeps(pkg)
		if err != nil {
			t.Fatalf("listTestDeps(%s): %v", pkg, err)
		}
		deps[pkg] = d
	}

	res := scope(all, []string{target}, deps, 0.9)
	if res.fullSuiteRequired {
		t.Fatalf("scope() reported full-suite-required for a single leaf-package change at threshold 0.9")
	}
	inScope := false
	for _, pkg := range res.packages {
		if pkg == target {
			inScope = true
			break
		}
	}
	if !inScope {
		t.Fatalf("scope() result %v does not contain the changed package itself (%s)", res.packages, target)
	}
	if len(res.packages) < 2 {
		t.Fatalf("scope() result %v: expected at least one real test-aware caller of %s (e.g. dolt/embeddeddolt/uow), got only the package itself", res.packages, target)
	}
}
