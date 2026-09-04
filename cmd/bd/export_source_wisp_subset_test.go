package main

import (
	"reflect"
	"testing"
)

// wispPlaneSubset feeds the wisp-plane relation readers in
// uowExportSource.LoadExportRelations: only ids that live in the wisps table
// go to them, in input order; a set that marks none sends nothing, so those
// readers issue no statement at all (wy-237yfi).
func TestWispPlaneSubset(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}

	if got := wispPlaneSubset(ids, map[string]bool{}); got != nil {
		t.Fatalf("empty set: want nil, got %v", got)
	}
	got := wispPlaneSubset(ids, map[string]bool{"d": true, "b": true, "zz": true, "a": false})
	if !reflect.DeepEqual(got, []string{"b", "d"}) {
		t.Fatalf("want [b d] in input order, got %v", got)
	}
	if got := wispPlaneSubset(nil, map[string]bool{"a": true}); got != nil {
		t.Fatalf("no ids: want nil, got %v", got)
	}
}

// wispReaderIDs is the policy on top: a nil set is "membership unreadable"
// and must FAIL OPEN to every id — narrowing on it would silently export
// every wisp with no labels and no comments — while a non-nil empty set is a
// real answer (no wisps) and sends nothing.
func TestWispReaderIDs(t *testing.T) {
	ids := []string{"a", "b", "c"}

	if got := wispReaderIDs(ids, nil); !reflect.DeepEqual(got, ids) {
		t.Fatalf("nil set must fail open to all ids, got %v", got)
	}
	if got := wispReaderIDs(ids, map[string]bool{}); got != nil {
		t.Fatalf("empty non-nil set means no wisps: want nil, got %v", got)
	}
	if got := wispReaderIDs(ids, map[string]bool{"c": true}); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("want [c], got %v", got)
	}
}
