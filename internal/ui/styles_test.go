package ui

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderBasicStyles(t *testing.T) {
	t.Run("semantic wrappers", func(t *testing.T) {
		cases := []struct {
			name string
			got  string
			want string
		}{
			{"pass", RenderPass("ok"), PassStyle.Render("ok")},
			{"warn", RenderWarn("careful"), WarnStyle.Render("careful")},
			{"fail", RenderFail("boom"), FailStyle.Render("boom")},
			{"muted", RenderMuted("note"), MutedStyle.Render("note")},
			{"accent", RenderAccent("info"), AccentStyle.Render("info")},
			{"category", RenderCategory("mixed Case"), CategoryStyle.Render("MIXED CASE")},
			{"separator", RenderSeparator(), MutedStyle.Render(SeparatorLight)},
			{"pass icon", RenderPassIcon(), PassStyle.Render(IconPass)},
			{"warn icon", RenderWarnIcon(), WarnStyle.Render(IconWarn)},
			{"fail icon", RenderFailIcon(), FailStyle.Render(IconFail)},
			{"skip icon", RenderSkipIcon(), MutedStyle.Render(IconSkip)},
			{"info icon", RenderInfoIcon(), AccentStyle.Render(IconInfo)},
			{"bold", RenderBold("bold"), BoldStyle.Render("bold")},
			{"command", RenderCommand("bd prime"), CommandStyle.Render("bd prime")},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if tc.got != tc.want {
					t.Fatalf("%s mismatch: got %q want %q", tc.name, tc.got, tc.want)
				}
			})
		}
	})
}

func TestRenderStatusAndPriority(t *testing.T) {
	statusCases := []struct {
		status string
		want   string
	}{
		{"open", StatusOpenStyle.Render("open")},
		{"in_progress", StatusInProgressStyle.Render("in_progress")},
		{"blocked", StatusBlockedStyle.Render("blocked")},
		{"pinned", StatusPinnedStyle.Render("pinned")},
		{"hooked", StatusHookedStyle.Render("hooked")},
		{"closed", StatusClosedStyle.Render("closed")},
		{"custom", StatusOpenStyle.Render("custom")},
	}
	for _, tc := range statusCases {
		if got := RenderStatus(tc.status); got != tc.want {
			t.Fatalf("status %s mismatch: got %q want %q", tc.status, got, tc.want)
		}
	}

	// RenderPriority now includes the priority icon (●)
	priorityCases := []struct {
		priority int
		want     string
	}{
		{0, PriorityP0Style.Render(PriorityIcon + " P0")},
		{1, PriorityP1Style.Render(PriorityIcon + " P1")},
		{2, PriorityP2Style.Render(PriorityIcon + " P2")},
		{3, PriorityP3Style.Render(PriorityIcon + " P3")},
		{4, PriorityP4Style.Render(PriorityIcon + " P4")},
		{5, PriorityIcon + " P5"},
	}
	for _, tc := range priorityCases {
		if got := RenderPriority(tc.priority); got != tc.want {
			t.Fatalf("priority %d mismatch: got %q want %q", tc.priority, got, tc.want)
		}
	}

	// RenderPriorityCompact returns just "P0" without icon
	if got := RenderPriorityCompact(0); !strings.Contains(got, "P0") {
		t.Fatalf("compact priority should contain P0, got %q", got)
	}

	if got := RenderPriorityForStatus(0, "closed"); got != "P0" {
		t.Fatalf("closed priority should be plain text, got %q", got)
	}
	if got := RenderPriorityForStatus(1, "open"); got != RenderPriority(1) {
		t.Fatalf("open priority should use styling")
	}
}

func TestRenderTypeVariants(t *testing.T) {
	cases := []struct {
		issueType string
		want      string
	}{
		{"bug", TypeBugStyle.Render("bug")},
		{"feature", TypeFeatureStyle.Render("feature")},
		{"task", TypeTaskStyle.Render("task")},
		{"epic", TypeEpicStyle.Render("epic")},
		{"chore", TypeChoreStyle.Render("chore")},
		// Gas Town types (agent, role, rig) have been removed - they now fall through to default
		{"agent", "agent"},  // Falls through to default (no styling)
		{"role", "role"},    // Falls through to default (no styling)
		{"rig", "rig"},      // Falls through to default (no styling)
		{"custom", "custom"},
	}
	for _, tc := range cases {
		if got := RenderType(tc.issueType); got != tc.want {
			t.Fatalf("type %s mismatch: got %q want %q", tc.issueType, got, tc.want)
		}
	}

	if got := RenderTypeForStatus("bug", "closed"); got != "bug" {
		t.Fatalf("closed type should be plain, got %q", got)
	}
	if got := RenderTypeForStatus("bug", "open"); got != RenderType("bug") {
		t.Fatalf("open type should be styled")
	}
}

func TestRenderIssueCompact(t *testing.T) {
	open := RenderIssueCompact("bd-1", 0, "bug", "in_progress", "ship it")
	wantOpen := fmt.Sprintf("%s [%s] [%s] %s - %s",
		RenderID("bd-1"),
		RenderPriority(0),
		RenderType("bug"),
		RenderStatus("in_progress"),
		"ship it",
	)
	if open != wantOpen {
		t.Fatalf("open issue line mismatch: got %q want %q", open, wantOpen)
	}

	closed := RenderIssueCompact("bd-2", 2, "task", "closed", "done")
	raw := fmt.Sprintf("%s [P%d] [%s] %s - %s", "bd-2", 2, "task", "closed", "done")
	if closed != StatusClosedStyle.Render(raw) {
		t.Fatalf("closed issue line should be dimmed: got %q", closed)
	}
}

func TestRenderClosedUtilities(t *testing.T) {
	line := "bd-42 closed"
	if got := RenderClosedLine(line); got != StatusClosedStyle.Render(line) {
		t.Fatalf("closed line mismatch: got %q", got)
	}

	if got := RenderID("bd-5"); got != IDStyle.Render("bd-5") {
		t.Fatalf("RenderID mismatch")
	}
}

func TestRenderCommandAndCategoryAreUppercaseSafe(t *testing.T) {
	got := RenderCategory(" already upper ")
	if !strings.Contains(got, " ALREADY UPPER ") {
		t.Fatalf("category should uppercase input, got %q", got)
	}

	cmd := RenderCommand("bd prime")
	if !strings.Contains(cmd, "bd prime") {
		t.Fatalf("command output missing text: %q", cmd)
	}
}

func TestIsAgentMode(t *testing.T) {
	// Test default (no env vars) - t.Setenv automatically restores after test
	t.Setenv("BD_AGENT_MODE", "")
	t.Setenv("CLAUDE_CODE", "")
	if IsAgentMode() {
		t.Fatal("expected false with no env vars")
	}

	// Test BD_AGENT_MODE=1
	t.Setenv("BD_AGENT_MODE", "1")
	t.Setenv("CLAUDE_CODE", "")
	if !IsAgentMode() {
		t.Fatal("expected true with BD_AGENT_MODE=1")
	}

	// Test CLAUDE_CODE auto-detection
	t.Setenv("BD_AGENT_MODE", "")
	t.Setenv("CLAUDE_CODE", "something")
	if !IsAgentMode() {
		t.Fatal("expected true with CLAUDE_CODE set")
	}
}
