package scripts_test

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The historical-upgrade corpus runs as //tests/migration's per-release
// targets. Their release list and their pinned inputs live in Bazel files;
// scripts/migration-test/lib/versions.sh stays the reviewed definition of the
// corpus. These checks keep the two equal, so a release added to the corpus
// gets a target and a pinned binary, and a target never runs a binary whose
// digest differs from the one the harness would have verified.

// bashArray returns the elements of `declare -ar NAME=( ... )` in versions.sh.
func bashArray(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)declare -ar ` + name + `=\((.*?)\)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("versions.sh has no array %s", name)
	}
	return regexp.MustCompile(`"([^"]+)"`).FindAllString(m[1], -1)
}

// bashScalar returns the value of `readonly NAME="..."` in versions.sh.
func bashScalar(t *testing.T, src, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^readonly ` + name + `="([^"]*)"$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("versions.sh has no scalar %s", name)
	}
	return m[1]
}

// starlarkList returns the string elements of `NAME = [ ... ]` in a BUILD file.
func starlarkList(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)\n` + name + ` = \[(.*?)\]`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("tests/migration/BUILD.bazel has no list %s", name)
	}
	var out []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

func unquote(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = strings.Trim(s, `"`)
	}
	return out
}

func sortedSet(items ...[]string) []string {
	var out []string
	for _, list := range items {
		out = append(out, list...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestMigrationCorpusBazelTargetsMatchVersionsSh(t *testing.T) {
	root := sourceRepoRoot(t)
	versions := readPolicyFile(t, root, "scripts/migration-test/lib/versions.sh")
	build := readPolicyFile(t, root, "tests/migration/BUILD.bazel")

	sqlite := []string{
		bashScalar(t, versions, "SOURCE_TAG_SQLITE_VERSION"),
		bashScalar(t, versions, "PRE_CANONICAL_SQLITE_VERSION"),
		bashScalar(t, versions, "CLASSIC_SQLITE_VERSION"),
		bashScalar(t, versions, "CONFIGURED_SQLITE_VERSION"),
	}
	server := unquote(bashArray(t, versions, "HISTORICAL_DOLT_VERSIONS"))
	embedded := unquote(bashArray(t, versions, "EMBEDDED_DOLT_VERSIONS"))
	wisp := unquote(bashArray(t, versions, "WISP_PLANE_VERSIONS"))

	if got, want := sortedSet(starlarkList(t, build, "_CORPUS")), sortedSet(sqlite, server, embedded); !slices.Equal(got, want) {
		t.Errorf("tests/migration _CORPUS = %v, want versions.sh's corpus %v", got, want)
	}
	if got, want := sortedSet(starlarkList(t, build, "_DOLT_RUNTIME")), sortedSet(server, wisp); !slices.Equal(got, want) {
		t.Errorf("tests/migration _DOLT_RUNTIME = %v, want HISTORICAL_DOLT_VERSIONS + WISP_PLANE_VERSIONS %v", got, want)
	}
	if m := regexp.MustCompile(`(?m)^_SOURCE_TAG = "([^"]+)"$`).FindStringSubmatch(build); m == nil || m[1] != sqlite[0] {
		t.Errorf("tests/migration _SOURCE_TAG = %v, want SOURCE_TAG_SQLITE_VERSION %s", m, sqlite[0])
	}
	if !strings.Contains(build, `"github.com/steveyegge/beads `+sqlite[0]+` `+bashScalar(t, versions, "SOURCE_TAG_SQLITE_GO_TOOLCHAIN")+`"`) {
		t.Errorf("tests/migration:bd_source_tag does not build %s@%s with %s",
			bashScalar(t, versions, "SOURCE_TAG_SQLITE_MODULE"), sqlite[0], bashScalar(t, versions, "SOURCE_TAG_SQLITE_GO_TOOLCHAIN"))
	}
}

// Every pinned release a target runs is the archive lib/binary.sh would have
// downloaded and verified: the catalog's linux/amd64 digest (what
// @bd_releases checks) equals STRICT_RELEASE_SHA256.
func TestMigrationCorpusCatalogDigestsMatchStrictPins(t *testing.T) {
	root := sourceRepoRoot(t)
	versions := readPolicyFile(t, root, "scripts/migration-test/lib/versions.sh")
	var catalog struct {
		Versions []struct {
			Version       string `json:"version"`
			GithubRelease *struct {
				Asset *struct {
					Name   string `json:"name"`
					Digest string `json:"digest"`
				} `json:"linux_amd64_asset"`
			} `json:"github_release"`
			SourceZip struct {
				SHA256 string `json:"sha256"`
			} `json:"source_zip"`
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(readPolicyFile(t, root, "scripts/migration-test/release-catalog.json")), &catalog); err != nil {
		t.Fatal(err)
	}
	digests, sourceZips := map[string]string{}, map[string]string{}
	for _, v := range catalog.Versions {
		sourceZips[v.Version] = v.SourceZip.SHA256
		if v.GithubRelease != nil && v.GithubRelease.Asset != nil {
			digests[v.Version] = strings.TrimPrefix(v.GithubRelease.Asset.Digest, "sha256:")
		}
	}
	pins := regexp.MustCompile(`\["(v[^|"]+)\|linux\|amd64"\]="([0-9a-f]{64})"`).FindAllStringSubmatch(versions, -1)
	if len(pins) == 0 {
		t.Fatal("versions.sh has no linux/amd64 STRICT_RELEASE_SHA256 pins")
	}
	for _, p := range pins {
		if got := digests[p[1]]; got != p[2] {
			t.Errorf("%s: catalog linux_amd64 digest %q, versions.sh STRICT_RELEASE_SHA256 %q", p[1], got, p[2])
		}
	}

	// The source-tag build's own module zip is the catalog's source_zip.
	var lock struct {
		Module  string `json:"module"`
		Version string `json:"version"`
		Modules []struct {
			Path      string `json:"path"`
			Version   string `json:"version"`
			ZipSHA256 string `json:"zip_sha256"`
		} `json:"modules"`
	}
	if err := json.Unmarshal([]byte(readPolicyFile(t, root, "tests/migration/source_tag_modules.json")), &lock); err != nil {
		t.Fatal(err)
	}
	if want := bashScalar(t, versions, "SOURCE_TAG_SQLITE_VERSION"); lock.Version != want || lock.Module != bashScalar(t, versions, "SOURCE_TAG_SQLITE_MODULE") {
		t.Errorf("source_tag_modules.json pins %s@%s, want %s@%s", lock.Module, lock.Version, bashScalar(t, versions, "SOURCE_TAG_SQLITE_MODULE"), want)
	}
	found := false
	for _, m := range lock.Modules {
		if m.Path == lock.Module && m.Version == lock.Version {
			found = true
			if m.ZipSHA256 != sourceZips[lock.Version] {
				t.Errorf("source_tag_modules.json %s zip %q, catalog source_zip %q", lock.Version, m.ZipSHA256, sourceZips[lock.Version])
			}
		}
	}
	if !found {
		t.Errorf("source_tag_modules.json does not pin %s@%s itself", lock.Module, lock.Version)
	}
}

// The external Dolt runtime the server-Dolt and wisp-plane lanes run is the
// one versions.sh pins: same release, same archive digest.
func TestMigrationDoltRuntimeMatchesVersionsSh(t *testing.T) {
	root := sourceRepoRoot(t)
	versions := readPolicyFile(t, root, "scripts/migration-test/lib/versions.sh")
	version := strings.TrimPrefix(bashScalar(t, versions, "DOLT_TEST_RUNTIME_VERSION"), "v")
	digest := bashScalar(t, versions, "DOLT_TEST_RUNTIME_SHA256")

	module := readPolicyFile(t, root, "MODULE.bazel")
	m := regexp.MustCompile(`(?s)name = "dolt_test_runtime_linux_amd64",\s*platform = "linux-amd64",\s*version = "([^"]+)",`).FindStringSubmatch(module)
	if m == nil || m[1] != version {
		t.Errorf("MODULE.bazel's dolt_test_runtime_linux_amd64 = %v, want version %q", m, version)
	}
	bzl := readPolicyFile(t, root, "tools/bazel/dolt.bzl")
	d := regexp.MustCompile(`(?s)"` + regexp.QuoteMeta(version) + `": \{\s*"linux-amd64": "([0-9a-f]{64})",`).FindStringSubmatch(bzl)
	if d == nil || d[1] != digest {
		t.Errorf("tools/bazel/dolt.bzl DOLT_SHA256[%q][linux-amd64] = %v, want %s", version, d, digest)
	}
}
