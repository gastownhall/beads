package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestOutputDeleteProxiedPreviewIsPayloadBlind(t *testing.T) {
	titleMarker := "PROXIED_OUTPUT_TITLE_MARKER"
	descriptionMarker := "PROXIED_OUTPUT_DESCRIPTION_MARKER"
	notesMarker := "PROXIED_OUTPUT_NOTES_MARKER"
	payloadMarker := "PROXIED_OUTPUT_PAYLOAD_MARKER"
	result := deletePreviewResult{
		preview: domain.DeletePreview{
			Issues: map[string]*types.Issue{
				"test-target": {
					ID:          "test-target",
					Title:       titleMarker,
					Description: descriptionMarker,
					Notes:       notesMarker,
					Metadata:    json.RawMessage(`{"marker":"` + payloadMarker + `"}`),
				},
			},
			ConnectedIssues: map[string]*types.Issue{
				"test-connected": {
					ID:          "test-connected",
					Title:       titleMarker,
					Description: descriptionMarker,
					Notes:       notesMarker,
					Metadata:    json.RawMessage(`{"marker":"` + payloadMarker + `"}`),
				},
			},
		},
		res: issueops.DeleteResult{Deleted: 1, Dependencies: 2},
	}
	markers := []string{titleMarker, descriptionMarker, notesMarker, payloadMarker}

	t.Run("quiet dry-run emits nothing", func(t *testing.T) {
		in := &deleteInput{ids: []string{"test-target"}, force: true, dryRun: true, quiet: true}
		out := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, result) })
		if out != "" {
			t.Fatalf("proxied quiet preview produced output: %s", out)
		}
	})

	t.Run("JSON takes precedence without payload", func(t *testing.T) {
		in := &deleteInput{ids: []string{"test-target"}, force: true, dryRun: true, quiet: true, jsonOutput: true}
		out := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, result) })
		for _, marker := range markers {
			if strings.Contains(out, marker) {
				t.Fatalf("proxied JSON preview leaked %q: %s", marker, out)
			}
		}
		start := strings.Index(out, "{")
		if start < 0 {
			t.Fatalf("proxied JSON preview produced no JSON: %s", out)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out[start:]), &got); err != nil {
			t.Fatalf("parse proxied JSON preview: %v\nraw: %s", err, out[start:])
		}
		if _, ok := got["would_delete"]; !ok {
			t.Fatalf("proxied JSON preview missing would_delete: %v", got)
		}
	})
}

func TestOutputDeleteProxiedPreviewExactContracts(t *testing.T) {
	result := deletePreviewResult{
		preview: domain.DeletePreview{
			Issues: map[string]*types.Issue{
				"bd-target": {ID: "bd-target", Title: "Target"},
			},
			ConnectedIssues: map[string]*types.Issue{
				"bd-zulu":  {ID: "bd-zulu", Title: "Zulu"},
				"bd-alpha": {ID: "bd-alpha", Title: "Alpha"},
			},
		},
		res: issueops.DeleteResult{Deleted: 3, Dependencies: 4, Labels: 2, Events: 5},
	}

	t.Run("JSON includes the complete preview contract with sorted connections and takes precedence over quiet", func(t *testing.T) {
		in := &deleteInput{ids: []string{"bd-target"}, force: true, dryRun: true, quiet: true, jsonOutput: true}
		out := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, result) })
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("parse preview JSON: %v\nraw: %s", err, out)
		}
		want := map[string]any{
			"schema_version":       float64(1),
			"would_delete":         float64(3),
			"dependencies_removed": float64(4),
			"labels_removed":       float64(2),
			"events_removed":       float64(5),
			"ids":                  []any{"bd-target"},
			"not_found":            nil,
			"connected":            []any{"bd-alpha", "bd-zulu"},
			"dry_run":              true,
			"cascade":              false,
			"would_orphan":         float64(0),
			"would_delete_ids":     nil,
			"would_orphan_ids":     nil,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("preview JSON: got %#v, want %#v", got, want)
		}
	})

	t.Run("prose renders preview counts, sorted reference candidates, and dry-run marker", func(t *testing.T) {
		in := &deleteInput{ids: []string{"bd-target"}, dryRun: true}
		out := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, result) })
		for _, required := range []string{
			"Issues to delete (1):", "bd-target: Target", "3 issue(s) total", "4 dependency link(s)",
			"2 label(s)", "5 event(s)", "Connected issues (text references may be rewritten):",
			"bd-alpha: Alpha", "bd-zulu: Zulu", "(Dry-run mode - no changes made)",
		} {
			if !strings.Contains(out, required) {
				t.Errorf("prose preview missing %q:\n%s", required, out)
			}
		}
		if strings.Index(out, "bd-alpha: Alpha") > strings.Index(out, "bd-zulu: Zulu") {
			t.Errorf("prose connected issue order is not sorted:\n%s", out)
		}
		if strings.Contains(out, "Cascade") {
			t.Errorf("prose preview without --cascade must not mention cascade:\n%s", out)
		}
	})

	t.Run("prose warns about cascade only when cascade was requested and proceed hint carries the flag", func(t *testing.T) {
		in := &deleteInput{ids: []string{"bd-target"}, cascade: true}
		out := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, result) })
		for _, required := range []string{
			"Cascade mode enabled - will also delete parent-child descendants; other dependents survive",
			"bd delete bd-target --cascade --force",
		} {
			if !strings.Contains(out, required) {
				t.Errorf("cascade preview missing %q:\n%s", required, out)
			}
		}
	})

	t.Run("cascade prose lists every row the closure deletes and every row it orphans", func(t *testing.T) {
		cascadeResult := result
		cascadeResult.res = issueops.DeleteResult{
			Deleted:    4,
			DeletedIDs: []string{"bd-target", "bd-target.1", "bd-target.1.1", "bd-target.2"},
			Orphaned:   []string{"bd-blocked", "bd-found", "bd-related"},
		}
		in := &deleteInput{ids: []string{"bd-target"}, cascade: true, dryRun: true}
		out := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, cascadeResult) })
		// One entry per deleted row, titled when the read hydrated it and by
		// id alone when it did not: only bd-target has a title here.
		entries := make(map[string]int)
		for _, line := range strings.Split(out, "\n") {
			if fields := strings.Fields(line); strings.HasPrefix(line, "  ") && len(fields) > 0 {
				entries[strings.TrimSuffix(fields[0], ":")]++
			}
		}
		for _, id := range cascadeResult.res.DeletedIDs {
			if entries[id] != 1 {
				t.Errorf("cascade preview lists %s %d times, want once:\n%s", id, entries[id], out)
			}
		}
		for _, entry := range []string{"  bd-target: Target\n", "  bd-target.1: \n", "  bd-target.1.1: \n", "  bd-target.2: \n"} {
			if !strings.Contains(out, entry) {
				t.Errorf("cascade preview missing entry %q:\n%s", entry, out)
			}
		}
		if !strings.Contains(out, "Issues to delete (4):") {
			t.Errorf("cascade preview heading does not count the 4-row closure:\n%s", out)
		}
		if !strings.Contains(out, "4 issue(s) total") {
			t.Errorf("cascade preview does not count the 4-row closure:\n%s", out)
		}
		if !strings.Contains(out, "Would orphan 3 issue(s): bd-blocked, bd-found, bd-related") {
			t.Errorf("cascade preview does not name every orphan:\n%s", out)
		}

		in.jsonOutput = true
		jsonOut := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, cascadeResult) })
		var got map[string]any
		if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
			t.Fatalf("parse cascade preview JSON: %v\nraw: %s", err, jsonOut)
		}
		if want := []any{"bd-target", "bd-target.1", "bd-target.1.1", "bd-target.2"}; !reflect.DeepEqual(got["would_delete_ids"], want) {
			t.Errorf("would_delete_ids: got %#v, want %#v", got["would_delete_ids"], want)
		}
		if want := []any{"bd-blocked", "bd-found", "bd-related"}; !reflect.DeepEqual(got["would_orphan_ids"], want) {
			t.Errorf("would_orphan_ids: got %#v, want %#v", got["would_orphan_ids"], want)
		}
		if got["would_delete"] != float64(4) || got["would_orphan"] != float64(3) {
			t.Errorf("cascade preview counts: would_delete %#v, would_orphan %#v; want 4 and 3", got["would_delete"], got["would_orphan"])
		}
	})

	t.Run("refusal renders the blocking error instead of counts, JSON carries it as error", func(t *testing.T) {
		blockedResult := result
		blockedResult.res = issueops.DeleteResult{Orphaned: []string{"bd-dependent"}}
		// The ROLE's refusal, whose message is byte-identical to the domain's
		// DeleteBlockedError this test was written against — the two were
		// arrived at independently and say the same thing.
		blockedResult.blocked = &issueops.DependentsOutsideRequestError{
			IssueID: "bd-target", Dependents: []string{"bd-dependent"},
		}

		in := &deleteInput{ids: []string{"bd-target"}}
		out := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, blockedResult) })
		if !strings.Contains(out, "has dependents not in deletion set; use --force to orphan them, or --cascade to also delete its parent-child descendants (other dependents are orphaned)") {
			t.Errorf("refusal prose missing classic message:\n%s", out)
		}
		if strings.Contains(out, "Would remove:") {
			t.Errorf("refusal prose must not render counts:\n%s", out)
		}

		in = &deleteInput{ids: []string{"bd-target"}, jsonOutput: true}
		jsonOut := captureStdout(t, func() error { return outputDeleteProxiedPreview(in, blockedResult) })
		var got map[string]any
		if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
			t.Fatalf("parse refusal JSON: %v\nraw: %s", err, jsonOut)
		}
		if errMsg, _ := got["error"].(string); !strings.Contains(errMsg, "has dependents not in deletion set") {
			t.Errorf("refusal JSON error field: got %#v", got["error"])
		}
		if got["would_orphan"] != float64(1) {
			t.Errorf("refusal JSON would_orphan: got %#v, want 1", got["would_orphan"])
		}
	})
}

func TestRenderDeleteProxiedResultExactContracts(t *testing.T) {
	// No orphans on the base result: the subtests below assert the WITHOUT-orphans
	// contract, and the orphan case copies this and adds its own.
	res := issueops.DeleteResult{Deleted: 3, Dependencies: 4, Labels: 2, Events: 5, ReferencesUpdated: 1}

	t.Run("JSON includes the complete final aggregate", func(t *testing.T) {
		in := &deleteInput{ids: []string{"bd-target", "bd-dependent"}, jsonOutput: true}
		out := captureStdout(t, func() error {
			renderDeleteProxiedResult(in, res)
			return nil
		})
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("parse final JSON: %v\nraw: %s", err, out)
		}
		want := map[string]any{
			"schema_version":       float64(1),
			"deleted":              []any{"bd-target", "bd-dependent"},
			"deleted_count":        float64(3),
			"dependencies_removed": float64(4),
			"labels_removed":       float64(2),
			"events_removed":       float64(5),
			"references_updated":   float64(1),
			"orphaned_issues":      nil,
			"deleted_ids":          nil,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("final JSON: got %#v, want %#v", got, want)
		}
	})

	t.Run("prose includes all aggregate counts and reference updates", func(t *testing.T) {
		out := captureStdout(t, func() error {
			renderDeleteProxiedResult(&deleteInput{}, res)
			return nil
		})
		for _, required := range []string{
			"Deleted 3 issue(s)", "Removed 4 dependency link(s)", "Removed 2 label(s)",
			"Removed 5 event(s)", "Updated text references in 1 issue(s)",
		} {
			if !strings.Contains(out, required) {
				t.Errorf("final prose missing %q:\n%s", required, out)
			}
		}
		if strings.Contains(out, "Orphaned") {
			t.Errorf("final prose without orphans must not warn about orphans:\n%s", out)
		}
		if strings.Contains(out, "Deleted:") {
			t.Errorf("final prose without --cascade must not list the deleted rows:\n%s", out)
		}
	})

	t.Run("cascade prose names every deleted row", func(t *testing.T) {
		cascadeRes := res
		cascadeRes.DeletedIDs = []string{"bd-target", "bd-target.1", "bd-target.2"}
		out := captureStdout(t, func() error {
			renderDeleteProxiedResult(&deleteInput{ids: []string{"bd-target"}, cascade: true}, cascadeRes)
			return nil
		})
		if want := "  Deleted: bd-target, bd-target.1, bd-target.2\n"; !strings.Contains(out, want) {
			t.Errorf("cascade final prose missing %q:\n%s", want, out)
		}
	})

	t.Run("orphaned issues surface in JSON and prose (force without cascade)", func(t *testing.T) {
		orphanRes := res
		orphanRes.Orphaned = []string{"bd-orphan-a", "bd-orphan-b"}

		in := &deleteInput{ids: []string{"bd-target"}, jsonOutput: true}
		out := captureStdout(t, func() error {
			renderDeleteProxiedResult(in, orphanRes)
			return nil
		})
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("parse orphan JSON: %v\nraw: %s", err, out)
		}
		if want := []any{"bd-orphan-a", "bd-orphan-b"}; !reflect.DeepEqual(got["orphaned_issues"], want) {
			t.Errorf("orphaned_issues: got %#v, want %#v", got["orphaned_issues"], want)
		}

		prose := captureStdout(t, func() error {
			renderDeleteProxiedResult(&deleteInput{}, orphanRes)
			return nil
		})
		if !strings.Contains(prose, "Orphaned 2 issue(s): bd-orphan-a, bd-orphan-b") {
			t.Errorf("orphan prose missing warning:\n%s", prose)
		}
	})

	t.Run("JSON names every deleted row while deleted stays the requested ids", func(t *testing.T) {
		cascadeRes := res
		cascadeRes.DeletedIDs = []string{"bd-target", "bd-target.1", "bd-target.2"}

		in := &deleteInput{ids: []string{"bd-target"}, cascade: true, force: true, jsonOutput: true}
		out := captureStdout(t, func() error {
			renderDeleteProxiedResult(in, cascadeRes)
			return nil
		})
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("parse cascade JSON: %v\nraw: %s", err, out)
		}
		if want := []any{"bd-target", "bd-target.1", "bd-target.2"}; !reflect.DeepEqual(got["deleted_ids"], want) {
			t.Errorf("deleted_ids: got %#v, want %#v", got["deleted_ids"], want)
		}
		if want := []any{"bd-target"}; !reflect.DeepEqual(got["deleted"], want) {
			t.Errorf("deleted: got %#v, want the requested ids %#v", got["deleted"], want)
		}
	})
}

// TestDeleteAndCleanupHelpNameTheContainmentCascade pins the words a user reads
// before running --cascade (GH#6301): it deletes parent-child descendants only,
// and every other dependent survives orphaned. The old "recursively delete all
// dependents" text described the wider cascade this one replaced.
func TestDeleteAndCleanupHelpNameTheContainmentCascade(t *testing.T) {
	const want = "parent-child descendants; other dependents survive"
	for _, cmd := range []*cobra.Command{deleteCmd, cleanupCmd} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("%s help does not say %q:\n%s", cmd.Name(), want, cmd.Long)
		}
		flag := cmd.Flags().Lookup("cascade")
		if flag == nil {
			t.Fatalf("%s has no --cascade flag", cmd.Name())
		}
		if !strings.Contains(flag.Usage, want) {
			t.Errorf("%s --cascade usage = %q, want it to contain %q", cmd.Name(), flag.Usage, want)
		}
		if strings.Contains(strings.ToLower(cmd.Long+flag.Usage), "all dependent") {
			t.Errorf("%s still describes the cascade as deleting all dependents", cmd.Name())
		}
	}
}
