package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

var goTopLevelTestRe = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(t \*testing\.T\) \{`)

// Every name a pinned-shard manifest pins is a top-level test of the
// target's package: a stale name runs nowhere, and a renamed test silently
// falls back to the round-robin shards (regenerate with
// tools/bazel/pin_shards.py).
func TestPinnedShardManifestNamesAreTests(t *testing.T) {
	root := sourceRepoRoot(t)
	for _, tgt := range pinnedShardTargets {
		t.Run(tgt.rule, func(t *testing.T) {
			tests := map[string]bool{}
			srcs, err := filepath.Glob(filepath.Join(root, tgt.pkg, "*_test.go"))
			if err != nil || len(srcs) == 0 {
				t.Fatalf("no *_test.go in %s: %v", tgt.pkg, err)
			}
			for _, src := range srcs {
				b, err := os.ReadFile(src)
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range goTopLevelTestRe.FindAllStringSubmatch(string(b), -1) {
					tests[m[1]] = true
				}
			}
			pinned, _ := readPinnedShardManifest(t, root, tgt.manifest)
			var stale []string
			for name := range pinned {
				if !tests[name] {
					stale = append(stale, name)
				}
			}
			sort.Strings(stale)
			for _, name := range stale {
				t.Errorf("%s pins %s, which is not a top-level test in %s/*_test.go; regenerate it (tools/bazel/pin_shards.py)", tgt.manifest, name, tgt.pkg)
			}
		})
	}
}
