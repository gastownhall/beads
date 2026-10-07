package depguard_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/depguard"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
)

// The rules come from the repository's .golangci.yml.

func TestFilesGlobsMatchRepositoryRelativeNames(t *testing.T) {
	got := analyzertest.RunInRepo(t, depguard.Analyzer, "internal/workapi", map[string]string{
		"w.go": "package workapi\n\nimport _ \"github.com/spf13/cobra\"\n",
	})
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "import 'github.com/spf13/cobra' is not allowed from list 'workapi-frontend-boundary'") {
		t.Fatalf("diagnostics = %+v, want the workapi-frontend-boundary denial", got)
	}
}

// "$all" is "**/*.go": golangci-lint hands depguard absolute paths, so it
// covers files at the repository root too.
func TestAllCoversTheRepositoryRoot(t *testing.T) {
	got := analyzertest.RunInRepo(t, depguard.Analyzer, "", map[string]string{
		"root.go": "package beads\n\nimport _ \"github.com/dolthub/dolt/go/libraries/doltcore/doltdb\"\n",
	})
	if len(got) != 1 || !strings.Contains(got[0].Message, "'dolt-storage-boundary'") {
		t.Fatalf("diagnostics = %+v, want the dolt-storage-boundary denial", got)
	}
}

func TestNegatedFilesAreExempt(t *testing.T) {
	got := analyzertest.RunInRepo(t, depguard.Analyzer, "internal/storage/x", map[string]string{
		"x.go": "package x\n\nimport _ \"github.com/dolthub/dolt/go/libraries/doltcore/doltdb\"\n",
	})
	analyzertest.Equal(t, got, nil)
}

// beadserrors' strict rule allows only $gostd.
func TestGoStdAllowList(t *testing.T) {
	got := analyzertest.RunInRepo(t, depguard.Analyzer, "beadserrors", map[string]string{
		"e.go": "package beadserrors\n\nimport (\n\t_ \"errors\"\n\t_ \"net/http\"\n\t_ \"github.com/steveyegge/beads/internal/types\"\n)\n",
	})
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "import 'github.com/steveyegge/beads/internal/types' is not allowed from list 'beadserrors-leaf'") {
		t.Fatalf("diagnostics = %+v, want only the internal/types denial", got)
	}
}
