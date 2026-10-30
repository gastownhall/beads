package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

// TestUpdateAgentFileQuickReferenceFollowsRemote pins that a new AGENTS.md
// written by `bd init` lists `bd dolt push` in its Quick Reference exactly
// when its managed section does: only with a Dolt remote configured and the
// workspace not declared no-push. A custom --agents-template is the user's
// own text and is written as given.
func TestUpdateAgentFileQuickReferenceFollowsRemote(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     agents.RenderOpts
		wantPush bool
	}{
		{name: "remote", opts: agents.RenderOpts{HasRemote: true}, wantPush: true},
		{name: "no-remote", opts: agents.RenderOpts{}, wantPush: false},
		{name: "no-push", opts: agents.RenderOpts{HasRemote: true, NoPush: true}, wantPush: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "AGENTS.md")
			if err := updateAgentFile(path, false, "", agents.ProfileMinimal, tc.opts); err != nil {
				t.Fatalf("updateAgentFile: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			content := string(data)
			begin := strings.Index(content, "<!-- BEGIN BEADS INTEGRATION")
			if begin < 0 {
				t.Fatalf("AGENTS.md has no managed section:\n%s", content)
			}
			if got := listsDoltPush(content[:begin]); got != tc.wantPush {
				t.Errorf("Quick Reference lists `bd dolt push` = %v, want %v", got, tc.wantPush)
			}
			if got := listsDoltPush(content[begin:]); got != tc.wantPush {
				t.Errorf("managed section lists `bd dolt push` = %v, want %v", got, tc.wantPush)
			}
		})
	}

	t.Run("custom-template", func(t *testing.T) {
		dir := t.TempDir()
		template := "# Ours\n\n```bash\nbd dolt push   # our own reminder\n```\n"
		templatePath := filepath.Join(dir, "template.md")
		if err := os.WriteFile(templatePath, []byte(template), 0o644); err != nil {
			t.Fatalf("write template: %v", err)
		}
		path := filepath.Join(dir, "AGENTS.md")
		if err := updateAgentFile(path, false, templatePath, agents.ProfileMinimal, agents.RenderOpts{}); err != nil {
			t.Fatalf("updateAgentFile: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(data) != template {
			t.Errorf("custom template was not written as given:\n got: %q\nwant: %q", data, template)
		}
	})
}

// listsDoltPush reports whether any line of s is a `bd dolt push` command.
func listsDoltPush(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "bd dolt push") {
			return true
		}
	}
	return false
}
