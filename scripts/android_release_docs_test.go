package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAndroidCGOReleaseIsDocumented pins the docs and comments that change
// with #3538, when bd-android-arm64 became a cgo build: the release guide's
// pre-tag snapshot (the android id only builds on Linux x86_64, so macOS
// releasers must skip it), the changelog entry, the Termux install section,
// and the two comments that counted android/arm64 as a CGO_ENABLED=0 target.
func TestAndroidCGOReleaseIsDocumented(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	// Whitespace-normalized, so reflowing a paragraph keeps the check.
	releasing := strings.Join(strings.Fields(read("RELEASING.md")), " ")
	for _, want := range []string{
		"linux/amd64, linux/arm64, windows/amd64, android/arm64 and the darwin pair ship with `CGO_ENABLED=1`",
		"The android/arm64 CGO snapshot is Linux x86_64 only",
		"goreleaser build --snapshot --clean --id bd-android-arm64",
		"omit `--id bd-android-arm64`",
	} {
		if !strings.Contains(releasing, want) {
			t.Errorf("RELEASING.md does not contain %q", want)
		}
	}

	// Any release's Fixed section, so the check survives the [Unreleased]
	// rollup at tag time.
	if !changelogFixedMentions(read("CHANGELOG.md"), "#3538") {
		t.Error("CHANGELOG.md has no ### Fixed entry for #3538 (android_arm64 built with cgo)")
	}

	if !strings.Contains(read("docs/getting-started/installation.md"), "\n### Android (Termux)\n") {
		t.Error("docs/getting-started/installation.md has no ### Android (Termux) section")
	}

	serverMode := read("cmd/bd/store_factory_server_mode.go")
	if strings.Contains(serverMode, "three CGO_ENABLED=0 targets") || !strings.Contains(serverMode, "two CGO_ENABLED=0 targets") {
		t.Error("cmd/bd/store_factory_server_mode.go must count two CGO_ENABLED=0 release targets (windows/arm64, freebsd/amd64), not android/arm64")
	}

	cross := read("scripts/ci/bazel-release-cross-compile.sh")
	if !strings.Contains(cross, "linux/amd64, linux/arm64,\n# windows/amd64 and android/arm64 with CGO_ENABLED=1") {
		t.Error("scripts/ci/bazel-release-cross-compile.sh's coverage comment must list android/arm64 among the CGO_ENABLED=1 builds")
	}
}

// changelogFixedMentions reports whether a ### Fixed section of any release in
// changelog mentions ref.
func changelogFixedMentions(changelog, ref string) bool {
	inFixed := false
	for _, line := range strings.Split(changelog, "\n") {
		switch {
		case strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### "):
			inFixed = strings.TrimSpace(line) == "### Fixed"
		case inFixed && strings.Contains(line, ref):
			return true
		}
	}
	return false
}
