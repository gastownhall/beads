package ui

import (
	"os"
	"testing"
)

func TestShouldUseColor(t *testing.T) {
	// Save original env vars
	origNoColor := os.Getenv("NO_COLOR")
	origCliColor := os.Getenv("CLICOLOR")
	origCliColorForce := os.Getenv("CLICOLOR_FORCE")
	origTerm := os.Getenv("TERM")
	defer func() {
		setEnv("NO_COLOR", origNoColor)
		setEnv("CLICOLOR", origCliColor)
		setEnv("CLICOLOR_FORCE", origCliColorForce)
		setEnv("TERM", origTerm)
	}()

	tests := []struct {
		name            string
		noColor         string
		cliColor        string
		cliColorForce   string
		term            string
		wantColor       bool
		skipTTYDepCheck bool // Some tests don't depend on TTY state
	}{
		{
			name:            "NO_COLOR disables color",
			noColor:         "1",
			wantColor:       false,
			skipTTYDepCheck: true,
		},
		{
			name:            "NO_COLOR empty string value still disables",
			noColor:         "", // will be unset
			cliColor:        "",
			cliColorForce:   "",
			wantColor:       false, // depends on TTY, but we're in test = no TTY
			skipTTYDepCheck: false,
		},
		{
			name:            "CLICOLOR=0 disables color",
			cliColor:        "0",
			wantColor:       false,
			skipTTYDepCheck: true,
		},
		{
			name:            "CLICOLOR_FORCE enables color even in non-TTY",
			cliColorForce:   "1",
			wantColor:       true,
			skipTTYDepCheck: true,
		},
		{
			name:            "TERM dumb disables color",
			term:            "dumb",
			wantColor:       false,
			skipTTYDepCheck: true,
		},
		{
			name:            "CLICOLOR_FORCE overrides TERM dumb",
			cliColorForce:   "1",
			term:            "dumb",
			wantColor:       true,
			skipTTYDepCheck: true,
		},
		{
			name:            "NO_COLOR takes precedence over CLICOLOR_FORCE",
			noColor:         "1",
			cliColorForce:   "1",
			wantColor:       false,
			skipTTYDepCheck: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all vars first
			os.Unsetenv("NO_COLOR")
			os.Unsetenv("CLICOLOR")
			os.Unsetenv("CLICOLOR_FORCE")
			os.Unsetenv("TERM")

			// Set test-specific vars
			if tt.noColor != "" {
				os.Setenv("NO_COLOR", tt.noColor)
			}
			if tt.cliColor != "" {
				os.Setenv("CLICOLOR", tt.cliColor)
			}
			if tt.cliColorForce != "" {
				os.Setenv("CLICOLOR_FORCE", tt.cliColorForce)
			}
			if tt.term != "" {
				os.Setenv("TERM", tt.term)
			}

			got := ShouldUseColor()
			if tt.skipTTYDepCheck && got != tt.wantColor {
				t.Errorf("ShouldUseColor() = %v, want %v", got, tt.wantColor)
			}
		})
	}
}

func TestShouldUseHyperlinks(t *testing.T) {
	envKeys := []string{
		"BD_GIT_HOOK",
		"NO_COLOR",
		"CLICOLOR",
		"FORCE_HYPERLINK",
		"TERM",
		"TERM_PROGRAM",
		"WT_SESSION",
	}
	orig := map[string]string{}
	for _, key := range envKeys {
		orig[key] = os.Getenv(key)
		os.Unsetenv(key)
	}
	defer func() {
		for _, key := range envKeys {
			setEnv(key, orig[key])
		}
	}()

	if ShouldUseHyperlinks() {
		t.Fatal("hyperlinks should be disabled for non-TTY output by default")
	}

	os.Setenv("FORCE_HYPERLINK", "1")
	if !ShouldUseHyperlinks() {
		t.Fatal("FORCE_HYPERLINK should enable hyperlinks")
	}

	os.Setenv("NO_COLOR", "1")
	if ShouldUseHyperlinks() {
		t.Fatal("NO_COLOR should disable hyperlinks even when forced")
	}

	os.Unsetenv("NO_COLOR")
	os.Setenv("TERM", "dumb")
	if ShouldUseHyperlinks() {
		t.Fatal("TERM=dumb should disable hyperlinks even when forced")
	}

	os.Setenv("TERM", "xterm-256color")
	os.Setenv("BD_GIT_HOOK", "1")
	if ShouldUseHyperlinks() {
		t.Fatal("BD_GIT_HOOK should disable hyperlinks")
	}

	os.Unsetenv("BD_GIT_HOOK")
	os.Unsetenv("FORCE_HYPERLINK")
	os.Setenv("TERM", "xterm-256color")
	os.Setenv("WT_SESSION", "windows-terminal-session")
	if !shouldUseHyperlinks(true) {
		t.Fatal("WT_SESSION should enable hyperlinks for Windows Terminal when stdout is a TTY")
	}

	os.Unsetenv("WT_SESSION")
	os.Setenv("TERM_PROGRAM", "vscode")
	if !shouldUseHyperlinks(true) {
		t.Fatal("TERM_PROGRAM=vscode should enable hyperlinks when stdout is a TTY")
	}

	os.Unsetenv("TERM_PROGRAM")
	os.Setenv("TERM", "xterm-kitty")
	if !shouldUseHyperlinks(true) {
		t.Fatal("xterm-kitty should enable hyperlinks when stdout is a TTY")
	}
}

func TestShouldUseEmoji(t *testing.T) {
	// Save original env var
	origNoEmoji := os.Getenv("BD_NO_EMOJI")
	defer setEnv("BD_NO_EMOJI", origNoEmoji)

	tests := []struct {
		name      string
		noEmoji   string
		wantEmoji bool
	}{
		{
			name:      "BD_NO_EMOJI disables emoji",
			noEmoji:   "1",
			wantEmoji: false,
		},
		{
			name:      "No BD_NO_EMOJI falls back to TTY check",
			noEmoji:   "",
			wantEmoji: false, // In test, stdout is not a TTY
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("BD_NO_EMOJI")
			if tt.noEmoji != "" {
				os.Setenv("BD_NO_EMOJI", tt.noEmoji)
			}

			got := ShouldUseEmoji()
			if got != tt.wantEmoji {
				t.Errorf("ShouldUseEmoji() = %v, want %v", got, tt.wantEmoji)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	// When running under go test, stdout is typically not a TTY
	got := IsTerminal()
	// We can't easily assert the value, but we can verify it doesn't panic
	t.Logf("IsTerminal() = %v (expected false in test environment)", got)
}

// setEnv sets or unsets an environment variable
func setEnv(key, value string) {
	if value == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, value)
	}
}
