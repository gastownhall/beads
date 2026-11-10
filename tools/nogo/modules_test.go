package nogo_test

import (
	"os"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

func parseGoMod(t *testing.T, path string) *modfile.File {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return f
}

// TestModuleRequirementsAgreeWithRoot keeps the two modules gazelle.go.work
// joins at identical versions. go_deps resolves them as one graph (highest
// version wins), so a module this one requires at a higher version than the
// root go.mod would silently change what the bd build links under Bazel but
// not under `go build`; a lower one would build the analyzers here against
// versions Bazel never uses.
func TestModuleRequirementsAgreeWithRoot(t *testing.T) {
	root := parseGoMod(t, "../../go.mod")
	nogo := parseGoMod(t, "go.mod")
	rootVersions := map[string]string{}
	for _, r := range root.Require {
		rootVersions[r.Mod.Path] = r.Mod.Version
	}
	for _, r := range nogo.Require {
		want, ok := rootVersions[r.Mod.Path]
		if !ok {
			continue
		}
		if semver.Compare(r.Mod.Version, want) != 0 {
			t.Errorf("tools/nogo/go.mod requires %s %s; the root go.mod requires %s (align them)", r.Mod.Path, r.Mod.Version, want)
		}
	}
	if root.Go.Version != nogo.Go.Version {
		t.Errorf("tools/nogo/go.mod go %s; root go.mod go %s", nogo.Go.Version, root.Go.Version)
	}
}
