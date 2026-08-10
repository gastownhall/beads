package uimd

import (
	"os"
	"strings"
	"testing"
)

// TestRenderMarkdownStylesBodyContentRegression3881 is the focused guard for
// gastownhall/beads#3881: when color is supported, body markdown (headings,
// bold, inline code) must be rendered with ANSI styling, not passed through as
// plaintext. The glamour v1->v2 migration silently broke this by dropping the
// style option, leaving an empty StyleConfig that emitted unstyled text. This
// asserts both that ANSI SGR is present and that markdown syntax markers are
// consumed, so it cannot be satisfied by raw passthrough.
func TestRenderMarkdownStylesBodyContentRegression3881(t *testing.T) {
	withMarkdownEnv(t, map[string]string{
		"NO_COLOR":        "",
		"TERM":            "xterm-256color",
		"CLICOLOR_FORCE":  "1",
		"FORCE_HYPERLINK": "",
		"BD_AGENT_MODE":   "",
		"CLAUDE_CODE":     "",
	})

	out := RenderMarkdown("# Heading\n\nSome **bold** text and `code`.\n")

	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI SGR styling for rendered body, got %q", out)
	}
	if !strings.Contains(out, "Heading") {
		t.Fatalf("expected heading text in output, got %q", out)
	}
	for _, marker := range []string{"# ", "**", "`"} {
		if strings.Contains(out, marker) {
			t.Fatalf("expected markdown marker %q to be rendered away, got %q", marker, out)
		}
	}
}

func TestRenderMarkdownStripsEscapesWhenANSIUnsupported(t *testing.T) {
	withMarkdownEnv(t, map[string]string{
		"NO_COLOR":        "1",
		"TERM":            "dumb",
		"CLICOLOR_FORCE":  "",
		"FORCE_HYPERLINK": "",
		"BD_AGENT_MODE":   "",
		"CLAUDE_CODE":     "",
	})

	out := RenderMarkdown("# Title\n\n[link](https://example.com)\n\n| A | B |\n| - | - |\n| 1 | 2 |\n")
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "\x1b]8;") {
		t.Fatalf("expected no terminal escapes when ANSI is unsupported, got %q", out)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "example.com") {
		t.Fatalf("expected rendered markdown content, got %q", out)
	}
}

func TestRenderMarkdownStripsOSC8WhenHyperlinksUnsupported(t *testing.T) {
	withMarkdownEnv(t, map[string]string{
		"NO_COLOR":        "",
		"TERM":            "xterm-256color",
		"CLICOLOR_FORCE":  "1",
		"FORCE_HYPERLINK": "",
		"BD_AGENT_MODE":   "",
		"CLAUDE_CODE":     "",
	})

	out := RenderMarkdown("[link](https://example.com)")
	if strings.Contains(out, "\x1b]8;") {
		t.Fatalf("expected OSC 8 hyperlinks to be stripped, got %q", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI styling when color is forced, got %q", out)
	}
}

func TestRenderMarkdownKeepsOSC8WhenHyperlinksSupported(t *testing.T) {
	withMarkdownEnv(t, map[string]string{
		"NO_COLOR":        "",
		"TERM":            "xterm-256color",
		"CLICOLOR_FORCE":  "1",
		"FORCE_HYPERLINK": "1",
		"BD_AGENT_MODE":   "",
		"CLAUDE_CODE":     "",
	})

	out := RenderMarkdown("[link](https://example.com)")
	if !strings.Contains(out, "\x1b]8;") {
		t.Fatalf("expected OSC 8 hyperlink escapes, got %q", out)
	}
}

func TestRenderMarkdownCanKeepOSC8WithoutANSIColor(t *testing.T) {
	withMarkdownEnv(t, map[string]string{
		"NO_COLOR":        "",
		"TERM":            "xterm-256color",
		"CLICOLOR_FORCE":  "",
		"FORCE_HYPERLINK": "1",
		"BD_AGENT_MODE":   "",
		"CLAUDE_CODE":     "",
	})

	out := RenderMarkdown("[link](https://example.com)")
	if !strings.Contains(out, "\x1b]8;") {
		t.Fatalf("expected OSC 8 hyperlink escapes, got %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("expected no ANSI SGR styling, got %q", out)
	}
}

func TestRenderMarkdownReturnsRawMarkdownInAgentMode(t *testing.T) {
	withMarkdownEnv(t, map[string]string{
		"NO_COLOR":        "",
		"TERM":            "xterm-256color",
		"CLICOLOR_FORCE":  "1",
		"FORCE_HYPERLINK": "1",
		"BD_AGENT_MODE":   "1",
		"CLAUDE_CODE":     "",
	})

	input := "# Title\n\n[link](https://example.com)"
	if out := RenderMarkdown(input); out != input {
		t.Fatalf("agent mode should return raw markdown, got %q", out)
	}
}

// TestRenderMarkdownPreservesAngleBracketSpans is the regression guard for
// ra-peoy7: RenderMarkdown must never delete tag-shaped "<...>" spans from
// body text (goldmark parses them as raw HTML, and glamour's ANSI renderer
// sanitizes those nodes down to nothing). Multi-line unclosed tags are the
// worst case — the deletion used to swallow every line up to the next ">".
func TestRenderMarkdownPreservesAngleBracketSpans(t *testing.T) {
	withMarkdownEnv(t, map[string]string{
		"NO_COLOR":        "1",
		"TERM":            "dumb",
		"CLICOLOR_FORCE":  "",
		"FORCE_HYPERLINK": "",
		"BD_AGENT_MODE":   "",
		"CLAUDE_CODE":     "",
	})

	t.Run("intra-line tag-shaped and comparison-operator spans", func(t *testing.T) {
		input := "LINE1: run foo <PLACEHOLDER> bar\n" +
			"LINE2: literal a<b and c>d\n" +
			"LINE3: html-ish <em>text</em> tail\n"
		out := RenderMarkdown(input)
		for _, want := range []string{
			"LINE1: run foo <PLACEHOLDER> bar",
			"LINE2: literal a<b and c>d",
			"LINE3: html-ish <em>text</em> tail",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("expected rendered output to contain %q, got %q", want, out)
			}
		}
	})

	t.Run("unclosed tag-shaped token does not eat following lines", func(t *testing.T) {
		input := "P: start <unclosed\n" +
			"Q: middle line one\n" +
			"R: middle line two\n" +
			"S: end > tail\n"
		out := RenderMarkdown(input)
		for _, want := range []string{
			"P: start <unclosed",
			"Q: middle line one",
			"R: middle line two",
			"S: end > tail",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("expected rendered output to contain %q, got %q", want, out)
			}
		}
	})

	t.Run("non-tag-shaped angle brackets stay intact (control)", func(t *testing.T) {
		input := "E1: <_foo> and <123> and < b > survive already"
		out := RenderMarkdown(input)
		if !strings.Contains(out, "<_foo>") || !strings.Contains(out, "<123>") {
			t.Fatalf("expected already-preserved spans to remain intact, got %q", out)
		}
	})
}

func withMarkdownEnv(t *testing.T, values map[string]string) {
	t.Helper()

	keys := []string{
		"BD_GIT_HOOK",
		"NO_COLOR",
		"CLICOLOR",
		"CLICOLOR_FORCE",
		"FORCE_HYPERLINK",
		"TERM",
		"TERM_PROGRAM",
		"WT_SESSION",
		"KITTY_WINDOW_ID",
		"WEZTERM_EXECUTABLE",
		"KONSOLE_VERSION",
		"DOMTERM",
		"GHOSTTY_RESOURCES_DIR",
		"VTE_VERSION",
		"BD_AGENT_MODE",
		"CLAUDE_CODE",
	}
	orig := make(map[string]string, len(keys))
	for _, key := range keys {
		orig[key] = os.Getenv(key)
		os.Unsetenv(key)
	}
	t.Cleanup(func() {
		for _, key := range keys {
			if orig[key] == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, orig[key])
			}
		}
	})

	for key, value := range values {
		if value == "" {
			os.Unsetenv(key)
		} else {
			os.Setenv(key, value)
		}
	}
}
