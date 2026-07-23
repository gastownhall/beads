package main

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/steveyegge/beads/internal/ui"
)

func TestApplyNoColorFlag(t *testing.T) {
	savedFlag := noColorFlag
	savedStyle := ui.AccentStyle
	savedColor := ui.ColorAccent
	t.Cleanup(func() {
		noColorFlag = savedFlag
		ui.AccentStyle = savedStyle
		ui.ColorAccent = savedColor
	})

	// Seed a colored style so we can observe it being cleared.
	ui.ColorAccent = lipgloss.Color("#ff0000")
	ui.AccentStyle = lipgloss.NewStyle().Foreground(ui.ColorAccent)

	t.Run("flag unset leaves styles untouched", func(t *testing.T) {
		noColorFlag = false
		applyNoColorFlag()
		if _, ok := ui.ColorAccent.(lipgloss.NoColor); ok {
			t.Error("colors disabled even though --no-color was not set")
		}
	})

	t.Run("flag set disables colors", func(t *testing.T) {
		noColorFlag = true
		applyNoColorFlag()
		if _, ok := ui.ColorAccent.(lipgloss.NoColor); !ok {
			t.Errorf("ColorAccent not reset to NoColor, got %T", ui.ColorAccent)
		}
		if out := ui.AccentStyle.Render("hi"); strings.ContainsRune(out, '\x1b') {
			t.Errorf("AccentStyle still emits ANSI after --no-color: %q", out)
		}
	})
}
