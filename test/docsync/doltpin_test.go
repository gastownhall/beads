package docsync

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The pinned Dolt version appears in three places that must agree: the prose
// pin and the copy-paste install snippet in docs/architecture/dolt.md, and
// the test container tag in internal/testutil/testdoltcommon.go. Raising one
// and forgetting the others is how the docs drifted onto a Dolt release with
// a broken CALL DOLT_RESET('--hard') in the first place (see that page for
// the measurements). Read as text rather than importing testutil, which
// would pull testcontainers into the docs guard.
const (
	doltPinPage      = "docs/architecture/dolt.md"
	doltPinImageFile = "internal/testutil/testdoltcommon.go"
)

var (
	doltPinProseRE   = regexp.MustCompile(`Beads pins Dolt to \*\*([0-9]+\.[0-9]+\.[0-9]+)\*\*`)
	doltPinSnippetRE = regexp.MustCompile(`DOLT_VERSION=([0-9]+\.[0-9]+\.[0-9]+)`)
	doltPinImageRE   = regexp.MustCompile(`DoltDockerImage = "dolthub/dolt-sql-server:([0-9]+\.[0-9]+\.[0-9]+)"`)
)

func TestDoltVersionPinsAgree(t *testing.T) {
	root := repoRoot()

	find := func(rel string, re *regexp.Regexp, what string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		m := re.FindSubmatch(data)
		if m == nil {
			t.Fatalf("%s: no %s found (pattern %s). If the wording moved, update this guard "+
				"rather than deleting it.", rel, what, re)
		}
		return string(m[1])
	}

	prose := find(doltPinPage, doltPinProseRE, "prose pin")
	snippet := find(doltPinPage, doltPinSnippetRE, "install-snippet DOLT_VERSION")
	image := find(doltPinImageFile, doltPinImageRE, "DoltDockerImage tag")

	if prose != snippet {
		t.Errorf("%s states the pin as %s but its install snippet uses DOLT_VERSION=%s",
			doltPinPage, prose, snippet)
	}
	if prose != image {
		t.Errorf("%s pins Dolt to %s but %s pins the test container to %s — "+
			"the documented version and the version CI exercises must match",
			doltPinPage, prose, doltPinImageFile, image)
	}
}
