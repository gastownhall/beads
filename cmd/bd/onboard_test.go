package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOnboardCommand(t *testing.T) {
	t.Run("onboard output contains key sections", func(t *testing.T) {
		var buf bytes.Buffer
		if err := renderOnboardInstructions(&buf); err != nil {
			t.Fatalf("renderOnboardInstructions() error = %v", err)
		}
		output := buf.String()

		// Verify output contains expected sections
		expectedSections := []string{
			"bd Onboarding Instructions",
			"Update AGENTS.md",
			"Update CLAUDE.md",
			"BEGIN AGENTS.MD CONTENT",
			"END AGENTS.MD CONTENT",
			"Issue Tracking with bd (beads)",
			"Managing AI-Generated Planning Documents",
			"history/",
		}

		for _, section := range expectedSections {
			if !strings.Contains(output, section) {
				t.Errorf("Expected output to contain '%s', but it was missing", section)
			}
		}
	})

	t.Run("agents content includes slop management", func(t *testing.T) {
		// Verify the agentsContent constant includes the new slop management section
		if !strings.Contains(agentsContent, "Managing AI-Generated Planning Documents") {
			t.Error("agentsContent should contain 'Managing AI-Generated Planning Documents' section")
		}
		if !strings.Contains(agentsContent, "history/") {
			t.Error("agentsContent should mention the 'history/' directory")
		}
		if !strings.Contains(agentsContent, "PLAN.md") {
			t.Error("agentsContent should mention example files like 'PLAN.md'")
		}
		if !strings.Contains(agentsContent, "Do NOT clutter repo root with planning documents") {
			t.Error("agentsContent should include rule about not cluttering repo root")
		}
	})

	t.Run("agents content includes bd workflow", func(t *testing.T) {
		// Verify essential bd workflow content is present
		essentialContent := []string{
			"bd ready",
			"bd create",
			"bd update",
			"bd close",
			"discovered-from",
			"--json",
			"MCP Server",
		}

		for _, content := range essentialContent {
			if !strings.Contains(agentsContent, content) {
				t.Errorf("agentsContent should contain '%s'", content)
			}
		}
	})
}
