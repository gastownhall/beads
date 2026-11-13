//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCrossRigDepTreeRendersForeignLeaves pins GH#5784 AC-7: the
// dependency tree drops bare cross-prefix edges with no trace (the walk reads
// the hydrated graph, which only holds local rows), so a source blocked by a
// foreign issue showed nothing under it — indistinguishable from no
// dependency at all. The tree now hangs a leaf for every bare foreign edge,
// any type: a readable target renders its real row with a view-only external
// marker, an unreadable one renders an unresolved leaf naming the stored ID
// and the reason, and the walk never recurses into the foreign rig (the
// foreign issue's own dependencies stay out) and invents no reverse edges.
// Local edges render byte-for-byte as before.
func TestEmbeddedCrossRigDepTreeRendersForeignLeaves(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig tree tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gt")

	targetDir := filepath.Join(dir, "hq-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "hq")

	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	writeRoutes := func(path string) {
		t.Helper()
		if err := os.WriteFile(routesPath, []byte(`{"prefix":"hq-","path":"`+path+`"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeRoutes("hq-repo")

	foreignBlocker := bdCreate(t, bd, targetDir, "foreign blocker")
	foreignRelated := bdCreate(t, bd, targetDir, "foreign related target")
	grandchild := bdCreate(t, bd, targetDir, "foreign grandchild under the blocker")
	local := bdCreate(t, bd, dir, "local dep")
	source := bdCreate(t, bd, dir, "source with foreign deps")

	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, local.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (local): %v\n%s", err, out)
	}
	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, foreignBlocker.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (cross-prefix): %v\n%s", err, out)
	}
	// A non-blocking foreign edge rides along too: every type renders.
	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, foreignRelated.ID, "--type", "related"); err != nil {
		t.Fatalf("bd link (cross-prefix, related): %v\n%s", err, out)
	}
	// The foreign blocker has a dependency of its own in its rig: the leaf
	// must render without the walk descending into it.
	if out, err := bdRunWithFlockRetry(t, bd, targetDir, "link", foreignBlocker.ID, grandchild.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (foreign rig): %v\n%s", err, out)
	}

	t.Run("human tree renders foreign leaves without recursing", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "tree", source.ID)
		if err != nil {
			t.Fatalf("bd dep tree: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, local.ID) {
			t.Errorf("tree must still render the local dep %s:\n%s", local.ID, stdout)
		}
		if !strings.Contains(stdout, foreignBlocker.ID) {
			t.Errorf("tree must render the foreign blocker %s:\n%s", foreignBlocker.ID, stdout)
		}
		if !strings.Contains(stdout, foreignRelated.ID) {
			t.Errorf("tree must render the related foreign target %s:\n%s", foreignRelated.ID, stdout)
		}
		if strings.Count(stdout, "(external)") < 2 {
			t.Errorf("every foreign leaf carries an (external) marker:\n%s", stdout)
		}
		if strings.Contains(stdout, grandchild.ID) {
			t.Errorf("the walk must not recurse into the foreign rig; %s must not appear:\n%s", grandchild.ID, stdout)
		}
	})

	t.Run("json tree carries the external view flag", func(t *testing.T) {
		stdout, _, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "tree", source.ID, "--json")
		if err != nil {
			t.Fatalf("bd dep tree --json: %v\n%s", err, stdout)
		}
		var nodes []map[string]any
		if err := json.Unmarshal([]byte(stdout), &nodes); err != nil {
			t.Fatalf("bd dep tree --json output is not a node array: %v\n%s", err, stdout)
		}
		byID := make(map[string]map[string]any, len(nodes))
		for _, node := range nodes {
			byID[node["id"].(string)] = node
		}
		foreignNode, ok := byID[foreignBlocker.ID]
		if !ok {
			t.Fatalf("--json output missing the foreign leaf %s:\n%s", foreignBlocker.ID, stdout)
		}
		if foreignNode["external"] != true {
			t.Errorf("the foreign leaf must carry the view-only external flag:\n%s", stdout)
		}
		if foreignNode["title"] != "foreign blocker" {
			t.Errorf("a readable foreign leaf carries the target's real row, got title %v:\n%s", foreignNode["title"], stdout)
		}
		if _, ok := byID[grandchild.ID]; ok {
			t.Errorf("--json output must not recurse into the foreign rig; %s must be absent:\n%s", grandchild.ID, stdout)
		}
		localNode, ok := byID[local.ID]
		if !ok {
			t.Fatalf("--json output missing the local dep %s:\n%s", local.ID, stdout)
		}
		if external, ok := localNode["external"]; ok {
			t.Errorf("the local leaf must stay unmarked, got external=%v:\n%s", external, stdout)
		}
	})

	t.Run("an unrouted target renders an unresolved leaf", func(t *testing.T) {
		// The route now names a rig that does not exist: both foreign targets
		// become unreadable at read time, the stored edges stay.
		writeRoutes("gone-rig")
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "tree", source.ID)
		if err != nil {
			t.Fatalf("an unrouted foreign target must not fail the tree: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, foreignBlocker.ID) {
			t.Errorf("the unresolved leaf must name the stored ID %s:\n%s", foreignBlocker.ID, stdout)
		}
		if !strings.Contains(stdout, "unresolved: ") {
			t.Errorf("the unresolved leaf must carry an unresolved marker with its reason:\n%s", stdout)
		}
		if !strings.Contains(stdout, local.ID) {
			t.Errorf("the local dep must render unchanged:\n%s", stdout)
		}
		if strings.Contains(stdout, grandchild.ID) {
			t.Errorf("an unresolved foreign leaf has no children; %s must not appear:\n%s", grandchild.ID, stdout)
		}

		// --json: the view-only unresolved flag and the reason travel too.
		jsonOut, _, err := bdRunWithFlockRetryBuffers(t, bd, dir, "dep", "tree", source.ID, "--json")
		if err != nil {
			t.Fatalf("bd dep tree --json (unrouted): %v\n%s", err, jsonOut)
		}
		var nodes []map[string]any
		if err := json.Unmarshal([]byte(jsonOut), &nodes); err != nil {
			t.Fatalf("bd dep tree --json output is not a node array: %v\n%s", err, jsonOut)
		}
		var unresolved *map[string]any
		for i := range nodes {
			if nodes[i]["id"] == foreignBlocker.ID {
				unresolved = &nodes[i]
			}
		}
		if unresolved == nil {
			t.Fatalf("--json output missing the unresolved leaf %s:\n%s", foreignBlocker.ID, jsonOut)
		}
		if reason, _ := (*unresolved)["unresolved"].(string); reason == "" {
			t.Errorf("the unresolved leaf must carry the view-only unresolved reason:\n%s", jsonOut)
		}
		if (*unresolved)["external"] != true {
			t.Errorf("the unresolved leaf must carry the view-only external flag:\n%s", jsonOut)
		}
	})
}
