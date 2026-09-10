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

// TestRenderMarkdownPreservesQuotedGlobs is the regression guard for bead
// bodies whose text contains "*" or "_" in a both-flanking position -- a quoted
// shell glob being the common case. Those runs used to pair with each other
// across the paragraph (goldmark joins consecutive lines into one paragraph),
// so the span between two globs was rendered as emphasis: with color the
// asterisks were consumed and silently vanished from the command, without color
// glamour's notty style wrote literal "**" into the middle of the text.
//
// The globs MUST stay quoted here. An unquoted "-name *.captured" is
// space-preceded, therefore opening-only under CommonMark's flanking rules, and
// can never pair -- it does not reproduce the bug and would make this test pass
// against the unfixed renderer.
func TestRenderMarkdownPreservesQuotedGlobs(t *testing.T) {
	colorEnv := map[string]string{
		"NO_COLOR": "", "TERM": "xterm-256color", "CLICOLOR_FORCE": "1",
		"FORCE_HYPERLINK": "", "BD_AGENT_MODE": "", "CLAUDE_CODE": "",
	}
	noColorEnv := map[string]string{
		"NO_COLOR": "1", "TERM": "dumb", "CLICOLOR_FORCE": "",
		"FORCE_HYPERLINK": "", "BD_AGENT_MODE": "", "CLAUDE_CODE": "",
	}

	bodies := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name: "quoted globs on separate lines",
			input: "L1: find /tmp -name '*.captured' -exec stat -f '%z' {} +\n" +
				"L2: find /tmp -name '*.captured' | grep -c '^512'\n",
			want: []string{
				"L1: find /tmp -name '*.captured' -exec stat -f '%z' {} +",
				"L2: find /tmp -name '*.captured' | grep -c '^512'",
			},
		},
		{
			name:  "two quoted globs on one line",
			input: "find /tmp -name '*.captured' -o -name '*.log' -print\n",
			want:  []string{"find /tmp -name '*.captured' -o -name '*.log' -print"},
		},
		{
			name:  "unspaced multiplication on separate lines",
			input: "set y=a*b now\nset z=c*d now\n",
			want:  []string{"set y=a*b now", "set z=c*d now"},
		},
		{
			name:  "SQL underscore wildcards between punctuation",
			input: "one: WHERE a LIKE '%_%' here\ntwo: WHERE b LIKE '%_%' here\n",
			want:  []string{"one: WHERE a LIKE '%_%' here", "two: WHERE b LIKE '%_%' here"},
		},
	}

	for _, mode := range []struct {
		name string
		env  map[string]string
	}{{"color", colorEnv}, {"nocolor", noColorEnv}} {
		for _, body := range bodies {
			t.Run(mode.name+"/"+body.name, func(t *testing.T) {
				withMarkdownEnv(t, mode.env)

				out := stripSGR(RenderMarkdown(body.input))
				for _, want := range body.want {
					if !strings.Contains(out, want) {
						t.Fatalf("expected rendered output to contain %q, got %q", want, out)
					}
				}
				if strings.Contains(out, "**") {
					t.Fatalf("expected no literal emphasis markers in output, got %q", out)
				}
			})
		}
	}

	// Authored emphasis must keep working: the fix hides only delimiter runs
	// that can open AND close, which "**bold**" and "*italic*" cannot.
	t.Run("color/authored emphasis still renders", func(t *testing.T) {
		withMarkdownEnv(t, colorEnv)

		out := RenderMarkdown("Some **bold** and *italic* with '*.glob' too\n")
		if !strings.Contains(out, "\x1b[") {
			t.Fatalf("expected ANSI styling for authored emphasis, got %q", out)
		}
		plain := stripSGR(out)
		if strings.Contains(plain, "**bold**") || strings.Contains(plain, "*italic*") {
			t.Fatalf("expected emphasis markers to be consumed, got %q", plain)
		}
		if !strings.Contains(plain, "bold") || !strings.Contains(plain, "italic") {
			t.Fatalf("expected emphasized words to survive, got %q", plain)
		}
		if !strings.Contains(plain, "'*.glob'") {
			t.Fatalf("expected glob to survive alongside emphasis, got %q", plain)
		}
	})

	// Code spans and fences already protected globs; the sentinel round-trip
	// must not leave anything behind in them.
	t.Run("nocolor/code spans and fences are unchanged", func(t *testing.T) {
		withMarkdownEnv(t, noColorEnv)

		out := RenderMarkdown("run `find -name '*.captured'` now\n\n```\nrm '*.log'\n```\n")
		for _, want := range []string{"find -name '*.captured'", "rm '*.log'"} {
			if !strings.Contains(out, want) {
				t.Fatalf("expected code content %q intact, got %q", want, out)
			}
		}
		if strings.ContainsAny(out, string(asteriskSentinel)+string(underscoreSentinel)) {
			t.Fatalf("sentinel leaked into rendered output: %q", out)
		}
		if strings.Contains(out, `\*`) {
			t.Fatalf("backslash leaked into code content: %q", out)
		}
	})
}

// TestRenderMarkdownKnownEmphasisTrades pins the two shapes that neutralizing
// both-flanking delimiter runs deliberately costs. Neither is a bug to be fixed
// later: for a stored-plain-text bead body, showing the characters that were
// stored is the better outcome in both. They are pinned so that a future change
// to the flanking predicate cannot widen the trade without a test failing.
func TestRenderMarkdownKnownEmphasisTrades(t *testing.T) {
	colorEnv := map[string]string{
		"NO_COLOR": "", "TERM": "xterm-256color", "CLICOLOR_FORCE": "1",
		"FORCE_HYPERLINK": "", "BD_AGENT_MODE": "", "CLAUDE_CODE": "",
	}
	noColorEnv := map[string]string{
		"NO_COLOR": "1", "TERM": "dumb", "CLICOLOR_FORCE": "",
		"FORCE_HYPERLINK": "", "BD_AGENT_MODE": "", "CLAUDE_CODE": "",
	}

	// An authored closer flanked by punctuation on both sides is itself
	// both-flanking, so it is neutralized and the pair no longer forms. This
	// changes the color path only -- the notty style already rendered this
	// shape literally, so no-color output is unchanged from before the fix.
	t.Run("color/both-flanking authored closer renders literally", func(t *testing.T) {
		withMarkdownEnv(t, colorEnv)

		out := stripSGR(RenderMarkdown(`see *"quoted"*, next` + "\n"))
		if !strings.Contains(out, `see *"quoted"*, next`) {
			t.Fatalf("expected the stored text verbatim, got %q", out)
		}
	})

	// An opener-only delimiter is untouched, so emphasis whose closer is not
	// both-flanking still renders -- the trade above is not a blanket loss.
	t.Run("color/ordinary emphasis is unaffected alongside it", func(t *testing.T) {
		withMarkdownEnv(t, colorEnv)

		out := stripSGR(RenderMarkdown(`*"q"*, and *italic* too` + "\n"))
		if !strings.Contains(out, `*"q"*`) {
			t.Fatalf("expected the both-flanking pair literal, got %q", out)
		}
		if strings.Contains(out, "*italic*") {
			t.Fatalf("expected ordinary emphasis still consumed, got %q", out)
		}
	})

	// A backslash escape reaching for a both-flanking delimiter keeps its
	// backslash: the sentinel replaces the very character the escape targets,
	// so goldmark never sees an escape sequence to process.
	for _, mode := range []struct {
		name string
		env  map[string]string
	}{{"color", colorEnv}, {"nocolor", noColorEnv}} {
		t.Run(mode.name+"/escaped both-flanking delimiter keeps its backslash", func(t *testing.T) {
			withMarkdownEnv(t, mode.env)

			out := stripSGR(RenderMarkdown(`glob \*.captured here` + "\n"))
			if !strings.Contains(out, `glob \*.captured here`) {
				t.Fatalf("expected the backslash to remain visible, got %q", out)
			}
		})

		// The same escape where the delimiter is NOT both-flanking is
		// processed exactly as before, which bounds the trade above.
		t.Run(mode.name+"/escaped opener-only delimiter is unaffected", func(t *testing.T) {
			withMarkdownEnv(t, mode.env)

			out := stripSGR(RenderMarkdown(`a \*literal\* b` + "\n"))
			if !strings.Contains(out, "a *literal* b") {
				t.Fatalf("expected the backslash consumed as before, got %q", out)
			}
		})
	}
}

// stripSGR removes ANSI SGR sequences so assertions compare visible characters.
func stripSGR(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
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
