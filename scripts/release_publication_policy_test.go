package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This fork distributes bd only through its own GitHub Releases. Publishing to
// any public package registry, or announcing the release anywhere, is outside
// the operator's authorization, so the surfaces that could do so must not exist
// in this repository at all — a guard condition is not enough, because it keeps
// the credentialed publish step one tag name away from running.
func TestReleaseWorkflowHasNoPublicRegistryPublishers(t *testing.T) {
	workflow := readReleaseWorkflowText(t)

	for _, forbidden := range []string{
		"publish-pypi",
		"publish-npm",
		"twine upload",
		"npm publish",
		"registry.npmjs.org",
		"PYPI_API_TOKEN",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release workflow still carries public publishing surface %q", forbidden)
		}
	}
}

func TestGoReleaserDoesNotAnnounceOrPublishExternally(t *testing.T) {
	config := readGoReleaserText(t)

	if strings.Contains(config, "skip: false") {
		t.Error("goreleaser announce is enabled; the fork must not announce releases")
	}
	for _, forbidden := range []string{"brews:", "nix:", "winget:", "dockers:", "aurs:"} {
		if strings.Contains(config, forbidden) {
			t.Errorf("goreleaser still declares external publisher %q", forbidden)
		}
	}
}

func readReleaseWorkflowText(t *testing.T) string {
	t.Helper()
	return readRepoFileText(t, filepath.Join(".github", "workflows", "release.yml"))
}

func readGoReleaserText(t *testing.T) string {
	t.Helper()
	return readRepoFileText(t, ".goreleaser.yml")
}

func readRepoFileText(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), relative))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
