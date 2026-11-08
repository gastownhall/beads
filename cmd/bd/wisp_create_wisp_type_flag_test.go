package main

import "testing"

// TestWispCreateAcceptsWispType asserts that both wisp-creating entry points
// (`bd mol wisp <proto>` and `bd mol wisp create <proto>`) accept --wisp-type,
// as `bd create --ephemeral` does, so wisps instantiated from a proto can carry
// a type (gastownhall/beads#8209).
func TestWispCreateAcceptsWispType(t *testing.T) {
	for _, c := range []struct {
		name string
		flag func() bool
	}{
		{"bd mol wisp <proto>", func() bool { return wispCmd.Flags().Lookup("wisp-type") != nil }},
		{"bd mol wisp create <proto>", func() bool { return wispCreateCmd.Flags().Lookup("wisp-type") != nil }},
	} {
		if !c.flag() {
			t.Errorf("%s: no --wisp-type flag; wisps created through it are always untyped", c.name)
		}
	}
}
