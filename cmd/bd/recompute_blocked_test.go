package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRenderRecomputeBlocked pins what bd recompute-blocked prints for a local
// repair, in every storage mode (the modes share this one renderer).
//
// The text for a database with nothing left as found is the text the command
// printed before it counted such rows, so a reader of the old output sees no
// change. The count is one more line when it is not zero. In --json both
// members are always present, so a caller can require either.
func TestRenderRecomputeBlocked(t *testing.T) {
	const abstainLine = "row(s) depend on a wisp this database does not have; is_blocked left as found."

	text := func(t *testing.T, changed, abstained int) string {
		t.Helper()
		oldJSON := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = oldJSON }()
		return captureStdout(t, func() error { return renderRecomputeBlocked(changed, abstained) })
	}

	if got, want := text(t, 0, 0), "is_blocked already consistent — nothing to recompute.\n"; got != want {
		t.Errorf("nothing corrected, nothing abstained: got %q, want %q", got, want)
	}
	if got, want := text(t, 3, 0), "Recomputed is_blocked: 3 row(s) corrected.\n"; got != want {
		t.Errorf("3 corrected, nothing abstained: got %q, want %q", got, want)
	}
	if got, want := text(t, 0, 2), "is_blocked already consistent — nothing to recompute.\n2 "+abstainLine+"\n"; got != want {
		t.Errorf("nothing corrected, 2 abstained: got %q, want %q", got, want)
	}
	if got, want := text(t, 3, 2), "Recomputed is_blocked: 3 row(s) corrected.\n2 "+abstainLine+"\n"; got != want {
		t.Errorf("3 corrected, 2 abstained: got %q, want %q", got, want)
	}

	for _, tc := range []struct{ changed, abstained int }{{0, 0}, {3, 2}} {
		oldJSON := jsonOutput
		jsonOutput = true
		out := captureStdout(t, func() error { return renderRecomputeBlocked(tc.changed, tc.abstained) })
		jsonOutput = oldJSON

		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
			t.Fatalf("parse --json output for %+v: %v\nraw: %s", tc, err, out)
		}
		for member, want := range map[string]int{"rows_corrected": tc.changed, "rows_abstained": tc.abstained} {
			got, ok := payload[member].(float64)
			if !ok {
				t.Errorf("%+v: %s missing or not a number in %s", tc, member, out)
				continue
			}
			if int(got) != want {
				t.Errorf("%+v: %s = %v, want %d", tc, member, got, want)
			}
		}
		if _, ok := payload["maintained_by"]; ok {
			t.Errorf("%+v: a local repair must not carry maintained_by: %s", tc, out)
		}
	}
}
