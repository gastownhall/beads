package dolt

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// helperSentinelRead matches how a TestHelper* entry point reads its own
// sentinel, e.g. os.Getenv("BEADS_SCHEMA_INIT_HELPER").
var helperSentinelRead = regexp.MustCompile(`os\.Getenv\("(BEADS_[A-Z0-9_]*_HELPER)"\)`)

// TestHelperSubprocessSentinelsAreComplete keeps helperSubprocessSentinels in
// sync with the helper entry points that actually exist. A missing entry is
// silent and expensive: TestMain would start a Dolt container the subprocess
// never uses, then leak it when the helper exits via os.Exit.
func TestHelperSubprocessSentinelsAreComplete(t *testing.T) {
	sources, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test sources: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("no *_test.go sources found — the scan below would vacuously pass")
	}

	var found []string
	for _, source := range sources {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		for _, match := range helperSentinelRead.FindAllStringSubmatch(string(data), -1) {
			found = append(found, match[1])
			if !slices.Contains(helperSubprocessSentinels, match[1]) {
				t.Errorf("%s reads helper sentinel %s, which is missing from helperSubprocessSentinels in testmain_test.go", source, match[1])
			}
		}
	}

	// The reverse direction is the control: if the pattern above ever stops
	// matching, this fails instead of letting the scan pass on zero hits.
	for _, sentinel := range helperSubprocessSentinels {
		if !slices.Contains(found, sentinel) {
			t.Errorf("helperSubprocessSentinels lists %s, but no test source reads it — stale entry, or helperSentinelRead no longer matches", sentinel)
		}
	}
}
