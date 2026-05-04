package main

import (
	"path/filepath"
	"regexp"
	"testing"
)

func TestNormalizeProveTitleStripsPrefixAndPriority(t *testing.T) {
	got := normalizeProveTitle("PhoenixVisualizer-64d P1 Studio audit catalog", regexp.MustCompile(`^phoenixvisualizer-[a-z0-9.]+\s*`))
	if got != "studio audit catalog" {
		t.Fatalf("normalizeProveTitle = %q, want %q", got, "studio audit catalog")
	}
}

func TestDuplicateScorePrefersNearIdenticalTitles(t *testing.T) {
	score := duplicateScore("PV window god-class decomposition", "PV window god class decomposition", nil)
	if score <= 0.95 {
		t.Fatalf("duplicateScore = %.3f, want > 0.95", score)
	}
}

func TestBuildRecommendationForLikelyCompleted(t *testing.T) {
	rec := buildRecommendation("likely_completed", "open", 0.91, nil, []string{"Open issue has strong completion language in notes/comments."})
	if rec.Action != "review_for_close" {
		t.Fatalf("action = %q, want review_for_close", rec.Action)
	}
	if rec.Confidence != 0.91 {
		t.Fatalf("confidence = %.2f, want 0.91", rec.Confidence)
	}
}

func TestBuildPathEvidenceRejectsAbsoluteAndEscapingPaths(t *testing.T) {
	root := t.TempDir()
	evidence := buildPathEvidence([]string{
		`Absolute C:\Windows\system32\kernel32.dll should be ignored, as should ../escape.md and docs/inside.md`,
	}, []string{"docs/"}, root, 12)
	if len(evidence) == 0 {
		t.Fatal("expected at least one repo-local path evidence entry")
	}
	for _, item := range evidence {
		if filepath.IsAbs(item.PathText) {
			t.Fatalf("path_text should not be absolute: %q", item.PathText)
		}
		rel, err := filepath.Rel(root, item.ResolvedPath)
		if err != nil {
			t.Fatalf("filepath.Rel failed: %v", err)
		}
		if rel == ".." || (len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator)) {
			t.Fatalf("resolved path escaped root: %q", item.ResolvedPath)
		}
		if item.PathText == "docs/inside.md" {
			if got := filepath.Clean(item.ResolvedPath); got != filepath.Join(root, "docs", "inside.md") {
				t.Fatalf("resolved_path = %q", got)
			}
		}
	}
}
