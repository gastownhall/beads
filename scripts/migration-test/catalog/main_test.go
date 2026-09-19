package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckedCatalogIsCanonicalAndComplete(t *testing.T) {
	catalog := readCheckedCatalog(t)
	if len(catalog.Versions) != 122 || len(catalog.Exclusions.RepositoryOnlyStable) != 49 ||
		len(catalog.Exclusions.RepositoryOnlyPrereleases) != 3 {
		t.Fatalf("catalog counts = %d/%d/%d, want 122/49/3",
			len(catalog.Versions), len(catalog.Exclusions.RepositoryOnlyStable),
			len(catalog.Exclusions.RepositoryOnlyPrereleases))
	}
	refs := map[string]string{}
	for _, entry := range catalog.Versions {
		refs[entry.Version] = entry.Origin.Ref
	}
	if refs["v0.56.0"] == "" {
		t.Fatal("missing proxy-preserved v0.56.0")
	}
	if refs["v0.9.11"] != "refs/heads/main" || refs["v0.17.2"] != "refs/heads/main" {
		t.Fatalf("preserved non-tag refs = %q, %q", refs["v0.9.11"], refs["v0.17.2"])
	}
	catalog.Versions[0].Sum = ""
	if err := validateCatalog(catalog); err == nil {
		t.Fatal("validator accepted missing authenticated provenance")
	}
}

func TestCheckedCatalogRejectsWellFormedIdentitySubstitution(t *testing.T) {
	base := readCheckedCatalog(t)
	tests := []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"version", func(c *Catalog) { c.Versions[13].Version = "v0.13.0" }},
		{"module sum", func(c *Catalog) { c.Versions[13].Sum = testH1 }},
		{"origin", func(c *Catalog) { c.Versions[13].Origin.Hash = strings.Repeat("a", 40) }},
		{"source zip", func(c *Catalog) { c.Versions[13].SourceZip.SHA256 = strings.Repeat("b", 64) }},
		{"exclusion", func(c *Catalog) { c.Exclusions.RepositoryOnlyStable[0] = "v0.57.11" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := cloneCatalog(t, base)
			tc.mutate(&catalog)
			raw, err := encodeCatalog(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeCatalog(raw); err == nil {
				t.Fatal("decodeCatalog accepted a well-formed identity substitution")
			}
		})
	}
}

func TestClassifyVersionsUsesProxyAsStableUniverse(t *testing.T) {
	stable, excluded, err := classifyVersions(
		[]string{"v1.1.2", "v1.1.0-rc.2", "v0.56.0", "v1.1.0-rc.1", "v0.9.1", "v1.2.0"},
		[]string{"v0.9.1", "v0.57.12", "v0.58.8-nosqlite", "v1.1.0-rc.1", "v1.1.2", "2026.218.0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stable, ","); got != "v0.9.1,v0.56.0,v1.1.2" {
		t.Fatalf("stable = %s", got)
	}
	if got := strings.Join(excluded.ProxyPrereleases, ","); got != "v1.1.0-rc.1,v1.1.0-rc.2" {
		t.Fatalf("proxy prereleases = %s", got)
	}
	if got := strings.Join(excluded.RepositoryOnlyStable, ","); got != "v0.57.12" {
		t.Fatalf("repository-only stable = %s", got)
	}
	if got := strings.Join(excluded.RepositoryOnlyPrereleases, ","); got != "v0.58.8-nosqlite" {
		t.Fatalf("repository-only prereleases = %s", got)
	}
}

func TestClassifyCatalogVersionsIncludesNewRemoteOnlyTags(t *testing.T) {
	_, excluded, err := classifyCatalogVersions(
		[]string{"v0.9.1", "v1.1.2"},
		[]string{"v0.57.12"},
		map[string]string{
			"v0.9.1":           strings.Repeat("a", 40),
			"v0.58.8-nosqlite": strings.Repeat("b", 40),
			"v1.1.2":           strings.Repeat("c", 40),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(excluded.RepositoryOnlyStable, ","); got != "v0.57.12" {
		t.Fatalf("repository-only stable = %s", got)
	}
	if got := strings.Join(excluded.RepositoryOnlyPrereleases, ","); got != "v0.58.8-nosqlite" {
		t.Fatalf("repository-only prereleases = %s", got)
	}
}

func TestCatalogEntryHashesExactProxyZip(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "module.zip")
	content := []byte("exact proxy zip bytes")
	if err := os.WriteFile(zipPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	download := downloadJSON{Version: "v0.9.1", Sum: testH1, GoModSum: testH1, Zip: zipPath,
		Origin: Origin{Hash: strings.Repeat("a", 40), Ref: "refs/tags/v0.9.1"}}
	entry, err := catalogEntry(download, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(content))
	if entry.SourceZip.SHA256 != want || entry.SourceZip.Size != int64(len(content)) {
		t.Fatalf("source zip = %+v, want sha256 %s size %d", entry.SourceZip, want, len(content))
	}
}

func TestVersionComparePrereleaseOrdering(t *testing.T) {
	// SemVer 2.0.0 §11 precedence for a shared numeric core, ascending. The
	// prior strings.Compare tiebreak misordered several of these — rc.10 before
	// rc.2, beta.11 before beta.2, and the stable release before its own
	// prereleases — and because generate and validate share versionCompare, a
	// wrong canonical order self-validated against the pinned digest.
	ascending := []string{
		"v1.2.3-alpha",
		"v1.2.3-alpha.1",
		"v1.2.3-alpha.beta",
		"v1.2.3-beta",
		"v1.2.3-beta.2",
		"v1.2.3-beta.11",
		"v1.2.3-rc.1",
		"v1.2.3-rc.2",
		"v1.2.3-rc.10",
		"v1.2.3",
	}
	for i := range ascending {
		for j := range ascending {
			if got, want := versionCompare(ascending[i], ascending[j]), compareInt(i, j); got != want {
				t.Errorf("versionCompare(%q, %q) = %d, want %d",
					ascending[i], ascending[j], got, want)
			}
		}
	}

	// A different numeric core dominates the prerelease tail entirely.
	if versionCompare("v1.2.3-rc.10", "v1.3.0-rc.2") >= 0 {
		t.Error("numeric core must dominate the prerelease comparison")
	}

	// Sorting a shuffled copy must recover the canonical order, and the strict
	// monotonicity guard the validator applies must then hold across it.
	shuffled := []string{
		"v1.2.3", "v1.2.3-rc.10", "v1.2.3-beta.2", "v1.2.3-alpha",
		"v1.2.3-rc.2", "v1.2.3-beta.11", "v1.2.3-alpha.beta", "v1.2.3-rc.1",
		"v1.2.3-alpha.1", "v1.2.3-beta",
	}
	slices.SortFunc(shuffled, versionCompare)
	if !slices.Equal(shuffled, ascending) {
		t.Fatalf("sorted = %v\nwant   = %v", shuffled, ascending)
	}
	for i := 1; i < len(shuffled); i++ {
		if versionCompare(shuffled[i-1], shuffled[i]) >= 0 {
			t.Fatalf("order not strictly increasing at %q, %q", shuffled[i-1], shuffled[i])
		}
	}
}

func TestValidOriginRefShapes(t *testing.T) {
	cases := []struct {
		version, ref string
		want         bool
	}{
		{"v0.9.1", "refs/tags/v0.9.1", true},  // tag ref for the entry's own version
		{"v0.9.11", "refs/heads/main", true},  // known main-branch origin
		{"v0.9.1", "refs/tags/v9.9.9", false}, // tag ref for a different version
		{"v0.9.1", "refs/pull/1/head", false}, // foreign ref family
		{"v0.9.1", "refs/tags/", false},       // empty tag name
		{"v0.9.1", "", false},                 // missing ref
	}
	for _, tc := range cases {
		if got := validOriginRef(tc.version, tc.ref); got != tc.want {
			t.Errorf("validOriginRef(%q, %q) = %v, want %v", tc.version, tc.ref, got, tc.want)
		}
	}
}

const testH1 = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func readCheckedCatalog(t *testing.T) Catalog {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "release-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := decodeCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func cloneCatalog(t *testing.T, catalog Catalog) Catalog {
	t.Helper()
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var clone Catalog
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
