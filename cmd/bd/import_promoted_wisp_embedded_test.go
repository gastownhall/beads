//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedImportPromotedWispRoundtrip is the classic-mode round-trip
// oracle for bd-r9uce: a promoted no-history wisp — a durable issues-table
// row that (in wild data) still carries no_history=true — must survive
// export→import→export with its relations intact.
//
// Pre-fix, classic import routed the record by row flags (issueops.IsWisp:
// Ephemeral || NoHistory), silently re-planing the durable row into the
// wisps table; its dependency edge to a durable friend in the same batch was
// then dropped as a "cross-bucket dependency", and the row itself left the
// durable plane. Import now routes by the export stream's explicit "wisp_plane"
// plane marker: marker absent ⇒ durable plane, whatever no_history says.
func TestEmbeddedImportPromotedWispRoundtrip(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// recordByID returns the parsed export record for id, or nil.
	recordByID := func(t *testing.T, stream, id string) map[string]any {
		t.Helper()
		for _, rec := range exportLines(t, stream) {
			if rec["_type"] == "issue" && rec["id"] == id {
				return rec
			}
		}
		return nil
	}

	t.Run("promoted_shape_routes_durable", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "rtp")

		// The wild promoted shape, as an export stream produced before the
		// promote fix: a durable record still carrying no_history=true, NO
		// "wisp_plane" marker, plus a durable friend whose dependency edge
		// points at it.
		wild := `{"_type":"issue","id":"rtp-wisp-promo","title":"Promoted no-history wisp","status":"open","priority":2,"issue_type":"task","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","labels":["keepme"],"comments":[{"id":"11111111-2222-3333-4444-555555555555","issue_id":"rtp-wisp-promo","author":"tester","text":"survive me","created_at":"2026-08-01T10:00:00Z"}],"no_history":true}
{"_type":"issue","id":"rtp-frend","title":"Durable friend","status":"open","priority":2,"issue_type":"task","created_at":"2026-08-01T10:00:01Z","updated_at":"2026-08-01T10:00:01Z","dependencies":[{"issue_id":"rtp-frend","depends_on_id":"rtp-wisp-promo","type":"blocks","created_at":"2026-08-01T10:00:01Z","created_by":"tester"}]}
`
		wildPath := filepath.Join(dir, "wild.jsonl")
		if err := os.WriteFile(wildPath, []byte(wild), 0o644); err != nil {
			t.Fatal(err)
		}

		importOut := bdImport(t, bd, dir, "-i", wildPath)
		if strings.Contains(importOut, "Skipped dependency") {
			t.Errorf("import dropped a relation (promoted row re-planed into the wisps bucket?):\n%s", importOut)
		}

		exportA := bdExport(t, bd, dir)
		promo := recordByID(t, exportA, "rtp-wisp-promo")
		if promo == nil {
			t.Fatal("export missing rtp-wisp-promo")
		}
		// Durable plane: no "wisp_plane" marker; the stray flag itself is
		// preserved (clearing it would change the content hash and break
		// export→import→export byte identity for wild rows).
		if _, marked := promo["wisp_plane"]; marked {
			t.Errorf("promoted row re-exported with the wisps-plane marker; it must stay durable: %v", promo)
		}
		if noHist, _ := promo["no_history"].(bool); !noHist {
			t.Errorf("promoted row lost no_history on the round trip: %v", promo)
		}
		if dc, _ := promo["dependent_count"].(float64); int(dc) != 1 {
			t.Errorf("promoted row dependent_count = %v, want 1 (relation dropped?)", promo["dependent_count"])
		}
		friend := recordByID(t, exportA, "rtp-frend")
		if friend == nil {
			t.Fatal("export missing rtp-frend")
		}
		deps, _ := friend["dependencies"].([]any)
		if len(deps) != 1 {
			t.Fatalf("friend dependencies = %v, want the one edge onto the promoted row", friend["dependencies"])
		}
		if edge, ok := deps[0].(map[string]any); !ok || edge["depends_on_id"] != "rtp-wisp-promo" {
			t.Errorf("friend dependency edge = %v, want depends_on_id=rtp-wisp-promo", deps[0])
		}

		// Second leg: the first export imported into a fresh rig re-exports
		// byte-identically — the round trip is a fixed point.
		dir2, _, _ := bdInit(t, bd, "--prefix", "rtp")
		exportAPath := filepath.Join(dir2, "incoming.jsonl")
		if err := os.WriteFile(exportAPath, []byte(exportA), 0o644); err != nil {
			t.Fatal(err)
		}
		if out := bdImport(t, bd, dir2, "-i", exportAPath); strings.Contains(out, "Skipped dependency") {
			t.Errorf("second-leg import dropped a relation:\n%s", out)
		}
		if exportB := bdExport(t, bd, dir2); exportB != exportA {
			t.Errorf("round trip is not a fixed point:\n%s", firstStreamDiff(exportA, exportB))
		}
	})

	t.Run("no_history_wisp_keeps_plane", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "rtw")
		wispID := bdCreateSilent(t, bd, dir, "Genuine no-history wisp", "--no-history")

		exportA := bdExport(t, bd, dir)
		rec := recordByID(t, exportA, wispID)
		if rec == nil {
			t.Fatalf("export missing no-history wisp %s", wispID)
		}
		// An unpromoted no-history wisp lives in the wisps table, so export
		// must stamp the explicit plane marker — flags alone cannot tell it
		// apart from the promoted shape above.
		if marked, _ := rec["wisp_plane"].(bool); !marked {
			t.Fatalf("no-history wisp exported without the wisps-plane marker: %v", rec)
		}

		dir2, _, _ := bdInit(t, bd, "--prefix", "rtw")
		path := filepath.Join(dir2, "incoming.jsonl")
		if err := os.WriteFile(path, []byte(exportA), 0o644); err != nil {
			t.Fatal(err)
		}
		bdImport(t, bd, dir2, "-i", path)
		exportB := bdExport(t, bd, dir2)
		if exportB != exportA {
			t.Errorf("no-history wisp round trip is not a fixed point:\n%s", firstStreamDiff(exportA, exportB))
		}
		rec2 := recordByID(t, exportB, wispID)
		if rec2 == nil {
			t.Fatalf("re-export missing no-history wisp %s", wispID)
		}
		if marked, _ := rec2["wisp_plane"].(bool); !marked {
			t.Errorf("re-imported no-history wisp lost the wisps-plane marker (re-planed durable?): %v", rec2)
		}
	})

	t.Run("legacy_wisp_alias_still_means_ephemeral", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "rtl")
		// v0.35–v0.37 exports spelled "ephemeral" as "wisp" and predate
		// no_history; the marker parse must keep restoring Ephemeral for them.
		legacy := `{"_type":"issue","id":"rtl-wisp-leg","title":"Legacy ephemeral","status":"open","priority":2,"issue_type":"task","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","wisp":true}
`
		path := filepath.Join(dir, "legacy.jsonl")
		if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
			t.Fatal(err)
		}
		bdImport(t, bd, dir, "-i", path)
		out, err := bdRunWithFlockRetry(t, bd, dir, "show", "rtl-wisp-leg", "--json")
		if err != nil {
			t.Fatalf("bd show: %v\n%s", err, out)
		}
		var shown any
		if err := json.Unmarshal(out, &shown); err != nil {
			t.Fatalf("parse show --json: %v\n%s", err, out)
		}
		obj, _ := shown.(map[string]any)
		if list, ok := shown.([]any); ok && len(list) > 0 {
			obj, _ = list[0].(map[string]any)
		}
		if obj == nil {
			t.Fatalf("unexpected show --json shape: %s", out)
		}
		if eph, _ := obj["ephemeral"].(bool); !eph {
			t.Errorf("legacy wisp:true record imported with ephemeral=%v, want true", obj["ephemeral"])
		}
	})
}
