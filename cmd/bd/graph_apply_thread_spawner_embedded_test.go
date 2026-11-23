//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// edgeRow is the raw persisted shape of a dependencies row that this test
// cares about: the JSON metadata blob and the thread_id column.
type edgeRow struct {
	metadata string
	threadID string
}

// queryEdgeRow reads the metadata/thread_id columns for a single dependency
// edge directly via SQL, bypassing any CLI-level projection, so this test is
// a true golden parity check of what --graph actually persisted.
func queryEdgeRow(t *testing.T, beadsDir, database, issueID, dependsOnID string) edgeRow {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var row edgeRow
	err = db.QueryRowContext(t.Context(),
		"SELECT COALESCE(metadata,''), COALESCE(thread_id,'') FROM dependencies "+
			"WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		issueID, dependsOnID).Scan(&row.metadata, &row.threadID)
	if err != nil {
		t.Fatalf("query edge row %s -> %s: %v", issueID, dependsOnID, err)
	}
	return row
}

// TestGraphApplyThreadIDAndSpawnerStampGoldenParity is the golden
// input->stored-row parity test required by the S11 review fix-up for HIGH-1
// (edges[].thread_id was being rejected, but was previously stored and must
// round-trip) and HIGH-2 (edges[].spawner_key/spawner_id must stamp
// metadata.spawner_id only when the plan actually named a spawner; an
// unnamed waits-for edge must keep gate-only metadata, matching the
// pre-BatchApplier types.NewGraphEdgeDependency behavior).
func TestGraphApplyThreadIDAndSpawnerStampGoldenParity(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gp")

	plan := `{
		"nodes": [
			{"key": "a", "title": "A"},
			{"key": "b", "title": "B"},
			{"key": "s", "title": "Spawner"},
			{"key": "w", "title": "Waiter"},
			{"key": "s2", "title": "Spawner2"},
			{"key": "w2", "title": "Waiter2"}
		],
		"edges": [
			{"from_key": "a", "to_key": "b", "type": "replies-to", "thread_id": "th-1"},
			{"from_key": "w", "to_key": "s", "type": "waits-for", "gate": "any-children", "spawner_key": "s"},
			{"from_key": "w2", "to_key": "s2", "type": "waits-for"}
		]
	}`
	planFile := filepath.Join(dir, "golden-plan.json")
	if err := os.WriteFile(planFile, []byte(plan), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	result := bdCreateGraph(t, bd, dir, planFile)

	aID, bID := result.IDs["a"], result.IDs["b"]
	sID, wID := result.IDs["s"], result.IDs["w"]
	s2ID, w2ID := result.IDs["s2"], result.IDs["w2"]
	for key, id := range map[string]string{"a": aID, "b": bID, "s": sID, "w": wID, "s2": s2ID, "w2": w2ID} {
		if id == "" {
			t.Fatalf("missing resolved id for key %q in %+v", key, result.IDs)
		}
	}

	// HIGH-1: thread_id must round-trip from plan input to stored row.
	threadRow := queryEdgeRow(t, beadsDir, "gp", aID, bID)
	if threadRow.threadID != "th-1" {
		t.Errorf("replies-to thread_id = %q, want %q", threadRow.threadID, "th-1")
	}

	// HIGH-2 positive: a waits-for edge with a named spawner_key must stamp
	// metadata.spawner_id to the resolved spawner issue ID.
	spawnRow := queryEdgeRow(t, beadsDir, "gp", wID, sID)
	var spawnMeta map[string]any
	if err := json.Unmarshal([]byte(spawnRow.metadata), &spawnMeta); err != nil {
		t.Fatalf("parse spawner edge metadata %q: %v", spawnRow.metadata, err)
	}
	if spawnMeta["gate"] != "any-children" {
		t.Errorf("spawner edge gate = %v, want any-children", spawnMeta["gate"])
	}
	if spawnMeta["spawner_id"] != sID {
		t.Errorf("spawner edge spawner_id = %v, want %q", spawnMeta["spawner_id"], sID)
	}

	// HIGH-2 negative: a waits-for edge with no named spawner must keep
	// gate-only metadata and must NOT acquire a spawner_id (the bug: it was
	// previously stamped unconditionally from the resolved target).
	noSpawnRow := queryEdgeRow(t, beadsDir, "gp", w2ID, s2ID)
	var noSpawnMeta map[string]any
	if err := json.Unmarshal([]byte(noSpawnRow.metadata), &noSpawnMeta); err != nil {
		t.Fatalf("parse no-spawner edge metadata %q: %v", noSpawnRow.metadata, err)
	}
	if _, present := noSpawnMeta["spawner_id"]; present {
		t.Errorf("no-spawner edge metadata = %q, must not contain spawner_id", noSpawnRow.metadata)
	}
}
