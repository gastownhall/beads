package main

import (
	"slices"
	"testing"
)

// Pure helpers for exclusive label namespaces (bd-7u5ki), kept out of the
// cgo-tagged label_exclusive_test.go so non-CGO lanes run them too.

func TestExclusiveLabelEvictions(t *testing.T) {
	prefixes := []string{"tier:", "review:"}

	got := exclusiveLabelEvictions(prefixes, []string{"tier:fable", "area:x", "review:opus"}, []string{"tier:opus"})
	if !slices.Equal(got, []string{"tier:fable"}) {
		t.Fatalf("only the added label's namespace is swapped, got %v", got)
	}
	// A label being added is never evicted, even when the issue already has it.
	got = exclusiveLabelEvictions(prefixes, []string{"tier:opus", "tier:fable"}, []string{"tier:opus"})
	if !slices.Equal(got, []string{"tier:fable"}) {
		t.Fatalf("re-adding a present label still evicts its siblings, got %v", got)
	}
	got = exclusiveLabelEvictions(prefixes, []string{"tier:fable", "review:opus"}, []string{"area:x"})
	if len(got) != 0 {
		t.Fatalf("a label outside every exclusive namespace evicts nothing, got %v", got)
	}
	got = exclusiveLabelEvictions(nil, []string{"tier:fable"}, []string{"tier:opus"})
	if len(got) != 0 {
		t.Fatalf("no exclusive prefixes configured: a plain add, got %v", got)
	}
}
