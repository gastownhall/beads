package schema

import (
	"reflect"
	"testing"
)

func obligationNames(t *testing.T, cursors map[string]int, retired map[droppedDefault]droppedDefaultRetirement) map[string][]string {
	t.Helper()
	obligations, err := obligationsAtCursors(cursors, retired)
	if err != nil {
		t.Fatalf("obligationsAtCursors(%v): %v", cursors, err)
	}
	out := map[string][]string{}
	for _, o := range obligations {
		out[o.d.String()] = o.sources
	}
	return out
}

// The recorded obligations follow each source's own cursor, deduplicate a
// column several migrations drop (keeping every source), and drop a column a
// recorded later migration retires (GH#5269).
func TestObligationsAtCursorsFollowsEachSourceAndRetirement(t *testing.T) {
	none := map[droppedDefault]droppedDefaultRetirement{}

	if got := obligationNames(t, map[string]int{"main": 42, "ignored": 9}, none); len(got) != 0 {
		t.Fatalf("below 0043 and ignored 0010: obligations %v, want none", got)
	}

	got := obligationNames(t, map[string]int{"main": 49, "ignored": 9}, none)
	want := map[string][]string{"dependencies.id": {"main 0043_drop_dependencies_generated_column.up.sql"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("main 49, ignored 9: %v, want %v", got, want)
	}

	got = obligationNames(t, map[string]int{"main": mainSource.latest(), "ignored": ignoredSource.latest()}, none)
	want = map[string][]string{
		"comments.id":             {"main 0051_drop_aux_id_defaults.up.sql"},
		"compaction_snapshots.id": {"main 0051_drop_aux_id_defaults.up.sql"},
		"dependencies.id": {
			"main 0043_drop_dependencies_generated_column.up.sql",
			"main 0050_dependencies_deterministic_id.up.sql",
		},
		"events.id":            {"main 0051_drop_aux_id_defaults.up.sql"},
		"issue_snapshots.id":   {"main 0051_drop_aux_id_defaults.up.sql"},
		"wisp_comments.id":     {"ignored 0010_drop_wisp_id_defaults.up.sql"},
		"wisp_dependencies.id": {"ignored 0010_drop_wisp_id_defaults.up.sql"},
		"wisp_events.id":       {"ignored 0010_drop_wisp_id_defaults.up.sql"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("at latest: %v, want %v", got, want)
	}

	retired := map[droppedDefault]droppedDefaultRetirement{{"comments", "id"}: {source: "main", version: 60}}
	if got := obligationNames(t, map[string]int{"main": 59, "ignored": 0}, retired); got["comments.id"] == nil {
		t.Fatalf("retirement at main 60 applied at cursor 59: %v", got)
	}
	if got := obligationNames(t, map[string]int{"main": 60, "ignored": 0}, retired); got["comments.id"] != nil {
		t.Fatalf("retirement at main 60 not applied at cursor 60: %v", got)
	}
}
