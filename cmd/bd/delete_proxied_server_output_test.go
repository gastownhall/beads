package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
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
		res: domain.DeleteIssuesResult{DeletedCount: 1, DependenciesCount: 2},
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
