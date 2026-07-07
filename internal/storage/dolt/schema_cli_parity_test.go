package dolt

import (
	"fmt"
	"strings"
	"testing"
)

func firstSchemaSnapshotDiff(cliSnapshot, runtimeSnapshot []string) string {
	i, j := 0, 0
	var diffs []string
	omitted := 0
	addDiff := func(diff string) {
		if len(diffs) < 10 {
			diffs = append(diffs, diff)
			return
		}
		omitted++
	}

	for i < len(cliSnapshot) && j < len(runtimeSnapshot) {
		switch {
		case cliSnapshot[i] == runtimeSnapshot[j]:
			i++
			j++
		case cliSnapshot[i] < runtimeSnapshot[j]:
			addDiff(fmt.Sprintf("only in CLI: %s", cliSnapshot[i]))
			i++
		default:
			addDiff(fmt.Sprintf("only in runtime: %s", runtimeSnapshot[j]))
			j++
		}
	}
	for ; i < len(cliSnapshot); i++ {
		addDiff(fmt.Sprintf("only in CLI: %s", cliSnapshot[i]))
	}
	for ; j < len(runtimeSnapshot); j++ {
		addDiff(fmt.Sprintf("only in runtime: %s", runtimeSnapshot[j]))
	}
	if len(diffs) == 0 {
		return ""
	}

	var b strings.Builder
	for _, diff := range diffs {
		b.WriteString(diff)
		b.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "... %d more differences omitted\n", omitted)
	}
	fmt.Fprintf(&b, "CLI entries: %d\nruntime entries: %d", len(cliSnapshot), len(runtimeSnapshot))
	return b.String()
}

func TestFirstSchemaSnapshotDiff(t *testing.T) {
	cliSnapshot := []string{"a", "c", "e"}
	runtimeSnapshot := []string{"a", "b", "d"}
	diff := firstSchemaSnapshotDiff(cliSnapshot, runtimeSnapshot)
	for _, want := range []string{
		"only in runtime: b",
		"only in CLI: c",
		"only in runtime: d",
		"only in CLI: e",
		"CLI entries: 3",
		"runtime entries: 3",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff = %q, want %q", diff, want)
		}
	}
}
