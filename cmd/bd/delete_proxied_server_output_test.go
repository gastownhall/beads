package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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
			"Cascade mode enabled - will also delete all dependent issues",
			"bd delete bd-target --cascade --force",
		} {
			if !strings.Contains(out, required) {
				t.Errorf("cascade preview missing %q:\n%s", required, out)
			}
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
		if !strings.Contains(out, "has dependents not in deletion set; use --cascade to delete them or --force to orphan them") {
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
}
