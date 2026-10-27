package versioncontrolops

import (
	"bytes"
	"testing"
)

func TestWarnExcludedHistory(t *testing.T) {
	previous := FederationWarnWriter
	var output bytes.Buffer
	FederationWarnWriter = &output
	t.Cleanup(func() { FederationWarnWriter = previous })

	WarnExcludedHistory(0)
	WarnExcludedHistory(2)
	WarnExcludedHistory(5)
	want := "Warning: federation.exclude_types removed 2 issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n"
	if output.String() != want {
		t.Fatalf("warning = %q, want %q", output.String(), want)
	}
}
