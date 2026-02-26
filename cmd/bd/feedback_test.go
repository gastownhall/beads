//go:build cgo

package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

const defaultTitleLength = 255

func TestFormatFeedbackID(t *testing.T) {
	// Ensure config is initialized with defaults
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	t.Run("default shows full title", func(t *testing.T) {
		got := formatFeedbackID("bd-abc", "Add user authentication")
		want := "bd-abc — Add user authentication"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty title returns ID only", func(t *testing.T) {
		got := formatFeedbackID("bd-abc", "")
		if got != "bd-abc" {
			t.Errorf("got %q, want %q", got, "bd-abc")
		}
	})

	t.Run("title-length=0 hides title", func(t *testing.T) {
		config.Set("output.title-length", 0)
		t.Cleanup(func() { config.Set("output.title-length", defaultTitleLength) })

		got := formatFeedbackID("bd-abc", "Add user authentication")
		if got != "bd-abc" {
			t.Errorf("got %q, want %q", got, "bd-abc")
		}
	})

	t.Run("title-length truncates with ellipsis", func(t *testing.T) {
		config.Set("output.title-length", 10)
		t.Cleanup(func() { config.Set("output.title-length", defaultTitleLength) })

		got := formatFeedbackID("bd-abc", "Add user authentication")
		want := "bd-abc — Add user …"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("title within limit not truncated", func(t *testing.T) {
		config.Set("output.title-length", 50)
		t.Cleanup(func() { config.Set("output.title-length", defaultTitleLength) })

		got := formatFeedbackID("bd-abc", "Short title")
		want := "bd-abc — Short title"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestFormatFeedbackIDParen(t *testing.T) {
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	t.Run("default shows full title in parens", func(t *testing.T) {
		got := formatFeedbackIDParen("bd-abc", "Write tests")
		want := "bd-abc (Write tests)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty title returns ID only", func(t *testing.T) {
		got := formatFeedbackIDParen("bd-abc", "")
		if got != "bd-abc" {
			t.Errorf("got %q, want %q", got, "bd-abc")
		}
	})

	t.Run("title-length=0 hides title", func(t *testing.T) {
		config.Set("output.title-length", 0)
		t.Cleanup(func() { config.Set("output.title-length", defaultTitleLength) })

		got := formatFeedbackIDParen("bd-abc", "Write tests")
		if got != "bd-abc" {
			t.Errorf("got %q, want %q", got, "bd-abc")
		}
	})
}

func TestIssueTitleOrEmpty(t *testing.T) {
	t.Run("nil issue returns empty", func(t *testing.T) {
		got := issueTitleOrEmpty(nil)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("non-nil issue returns title", func(t *testing.T) {
		issue := &types.Issue{Title: "Fix bug"}
		got := issueTitleOrEmpty(issue)
		if got != "Fix bug" {
			t.Errorf("got %q, want %q", got, "Fix bug")
		}
	})
}

func TestApplyTitleConfig(t *testing.T) {
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	t.Run("unicode truncation is rune-safe", func(t *testing.T) {
		config.Set("output.title-length", 3)
		t.Cleanup(func() { config.Set("output.title-length", defaultTitleLength) })

		// 4-rune title: truncated to 2 runes + ellipsis (maxLen-1 reserves space for …)
		got := applyTitleConfig("日本語文")
		want := "日本…"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("negative values hide title", func(t *testing.T) {
		config.Set("output.title-length", -1)
		t.Cleanup(func() { config.Set("output.title-length", defaultTitleLength) })

		got := applyTitleConfig("Hidden title")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}
