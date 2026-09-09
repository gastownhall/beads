//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestExportImportLabelDefinitionsRoundTrip pins the JSONL transport half of
// the label vocabulary registry: export writes every definition as a
// "_type":"label-definition" record (always, sorted by label), and import
// applies them define-if-absent - so a workspace restored from its JSONL
// gets its registry back instead of keeping labels.vocabulary while losing
// every definition, a re-import is a no-op, and a case-insensitive collision
// keeps the live registry's spelling.
func TestExportImportLabelDefinitionsRoundTrip(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)
	saved := saveAndRestoreGlobals(t)
	_ = saved

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(beadsDir, "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStore(t, testDBPath)
	store = s
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	ctx := context.Background()
	rootCtx = ctx

	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"exp-ld-1", "Round trip issue", "", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	if err := s.DefineLabel(ctx, "tier:opus", "model tier", "tester"); err != nil {
		t.Fatalf("define tier:opus: %v", err)
	}
	if err := s.DefineLabel(ctx, "backend", "", "tester"); err != nil {
		t.Fatalf("define backend: %v", err)
	}

	exportFile := filepath.Join(tmpDir, "export.jsonl")
	exportOutput = exportFile
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false
	t.Cleanup(func() { exportOutput = "" })

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	lines := splitJSONL(data)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (1 issue + 2 label definitions), got %d", len(lines))
	}

	// The definition records follow the issues, sorted by label.
	//
	// COMPAT PIN (bda-t63c): default export now always contains non-issue
	// records, which is a deliberate contract change the PR body must flag.
	// What consumers may rely on: EVERY line carries a non-empty _type to
	// dispatch on, and with memories excluded (the default) the only
	// non-issue type is label-definition. A consumer that unmarshals every
	// default-export line as an issue was never guaranteed anything better.
	var defLabels []string
	for _, line := range lines {
		var rec map[string]interface{}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("parse line: %v", err)
		}
		typ, _ := rec["_type"].(string)
		if typ == "" {
			t.Fatalf("default-export line without a dispatchable _type: %s", line)
		}
		if typ != "issue" && typ != "label-definition" {
			t.Fatalf("default export (memories excluded) carried unexpected _type %q: %s", typ, line)
		}
		if rec["_type"] == "label-definition" {
			defLabels = append(defLabels, rec["label"].(string))
			if rec["label"] == "tier:opus" && rec["description"] != "model tier" {
				t.Errorf("tier:opus description not exported, got %v", rec["description"])
			}
		}
	}
	if len(defLabels) != 2 || defLabels[0] != "backend" || defLabels[1] != "tier:opus" {
		t.Fatalf("expected sorted label definitions [backend tier:opus], got %v", defLabels)
	}

	// Idempotent re-import: everything already defined, nothing written.
	res, err := importFromLocalJSONLFull(ctx, s, exportFile)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if res.LabelDefinitions != 0 {
		t.Fatalf("re-import must define nothing, got %d", res.LabelDefinitions)
	}

	// Case-insensitive collision: the live registry's spelling wins.
	if err := s.UndefineLabel(ctx, "backend"); err != nil {
		t.Fatalf("undefine backend: %v", err)
	}
	if err := s.DefineLabel(ctx, "BACKEND", "shouty", "tester"); err != nil {
		t.Fatalf("define BACKEND: %v", err)
	}
	res, err = importFromLocalJSONLFull(ctx, s, exportFile)
	if err != nil {
		t.Fatalf("collision import: %v", err)
	}
	if res.LabelDefinitions != 0 {
		t.Fatalf("collision import must define nothing, got %d", res.LabelDefinitions)
	}
	defs, err := s.ListLabelDefinitions(ctx)
	if err != nil {
		t.Fatalf("list definitions: %v", err)
	}
	foundShouty := false
	for _, d := range defs {
		if d.Label == "BACKEND" {
			foundShouty = true
		}
		if d.Label == "backend" {
			t.Fatalf("import overwrote the live registry's spelling: %v", defs)
		}
	}
	if !foundShouty {
		t.Fatalf("expected BACKEND to survive the collision import, got %v", defs)
	}

	// Restore into an emptied registry: both definitions come back with
	// their exported spelling, description and creator.
	if err := s.UndefineLabel(ctx, "BACKEND"); err != nil {
		t.Fatalf("undefine BACKEND: %v", err)
	}
	if err := s.UndefineLabel(ctx, "tier:opus"); err != nil {
		t.Fatalf("undefine tier:opus: %v", err)
	}
	res, err = importFromLocalJSONLFull(ctx, s, exportFile)
	if err != nil {
		t.Fatalf("restore import: %v", err)
	}
	if res.LabelDefinitions != 2 {
		t.Fatalf("restore import must define 2, got %d", res.LabelDefinitions)
	}
	defs, err = s.ListLabelDefinitions(ctx)
	if err != nil {
		t.Fatalf("list definitions after restore: %v", err)
	}
	byLabel := map[string]string{}
	for _, d := range defs {
		desc := ""
		if d.Description != nil {
			desc = *d.Description
		}
		byLabel[d.Label] = desc
		if d.CreatedBy == nil || *d.CreatedBy != "tester" {
			t.Errorf("expected created_by=tester restored for %s, got %v", d.Label, d.CreatedBy)
		}
	}
	if byLabel["tier:opus"] != "model tier" {
		t.Fatalf("description not restored: %v", byLabel)
	}
	if _, ok := byLabel["backend"]; !ok {
		t.Fatalf("backend not restored: %v", byLabel)
	}
}
