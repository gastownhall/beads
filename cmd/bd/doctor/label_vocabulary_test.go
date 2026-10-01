package doctor

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestUndefinedLabelsInUse(t *testing.T) {
	defs := []types.LabelDefinition{
		{Label: "backend"},
		{Label: "frontend"},
	}
	counts := map[string]int{
		"backend":  3,
		"frontend": 1,
		"urgent":   2,
		"bug":      5,
	}
	got := undefinedLabelsInUse(defs, counts)
	want := []string{"bug", "urgent"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("undefinedLabelsInUse = %v, want %v", got, want)
	}
}

func TestUndefinedLabelsInUse_AllDefined(t *testing.T) {
	defs := []types.LabelDefinition{{Label: "backend"}}
	counts := map[string]int{"backend": 1}
	if got := undefinedLabelsInUse(defs, counts); len(got) != 0 {
		t.Errorf("expected no undefined labels, got %v", got)
	}
}

// TestUndefinedLabelsInUse_ExactMatchOnly confirms a case-variant of a
// defined label ("Backend" vs "backend") still counts as undefined here --
// this function answers the labels.vocabulary check, not the case-variant
// cluster check, and the two are deliberately independent.
func TestUndefinedLabelsInUse_ExactMatchOnly(t *testing.T) {
	defs := []types.LabelDefinition{{Label: "backend"}}
	counts := map[string]int{"Backend": 1}
	got := undefinedLabelsInUse(defs, counts)
	want := []string{"Backend"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("undefinedLabelsInUse = %v, want %v (case-variants are not exact matches)", got, want)
	}
}

func TestCaseVariantLabelClusters(t *testing.T) {
	counts := map[string]int{
		"Backend":  2,
		"backend":  5,
		"BACKEND":  1,
		"frontend": 3,
		"bug":      4,
	}
	clusters := caseVariantLabelClusters(counts)
	if len(clusters) != 1 {
		t.Fatalf("expected exactly 1 cluster, got %d: %v", len(clusters), clusters)
	}
	got := append([]string(nil), clusters[0]...)
	sort.Strings(got)
	want := []string{"BACKEND", "Backend", "backend"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cluster = %v, want %v", got, want)
	}
}

func TestCaseVariantLabelClusters_NoDuplicatesNoClusters(t *testing.T) {
	counts := map[string]int{"backend": 1, "frontend": 1, "bug": 1}
	if got := caseVariantLabelClusters(counts); len(got) != 0 {
		t.Errorf("expected no clusters, got %v", got)
	}
}

func TestCaseVariantLabelClusters_Empty(t *testing.T) {
	if got := caseVariantLabelClusters(map[string]int{}); len(got) != 0 {
		t.Errorf("expected no clusters for empty input, got %v", got)
	}
}

func TestFormatLabelVocabularyExamples_Truncates(t *testing.T) {
	items := make([]string, maxLabelVocabularyExamples+3)
	for i := range items {
		items[i] = "x"
	}
	out := formatLabelVocabularyExamples(items, func(s string) string { return s })
	if got := out; got == "" {
		t.Fatal("expected non-empty output")
	}
	// +3 items beyond the cap must be named in the truncation tail.
	if !strings.Contains(out, "(+3 more)") {
		t.Errorf("expected truncation tail naming 3 more items, got: %q", out)
	}
}

func TestFormatLabelVocabularyExamples_NoTruncationUnderCap(t *testing.T) {
	out := formatLabelVocabularyExamples([]string{"a", "b"}, func(s string) string { return s })
	if strings.Contains(out, "more)") {
		t.Errorf("must not append a truncation tail under the cap, got: %q", out)
	}
}
