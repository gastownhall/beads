package versioncontrolops

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// TestResolveCommitter pins the per-field identity rule both store modes
// share (#7628): explicit value, else GIT_AUTHOR_*, else beads/beads@local,
// where a value Dolt's --author parser cannot carry counts as unset.
func TestResolveCommitter(t *testing.T) {
	tests := []struct {
		name               string
		envName, envEmail  string
		argName, argEmail  string
		wantName, wantMail string
	}{
		{name: "defaults", wantName: "beads", wantMail: "beads@local"},
		{name: "env", envName: "Env Author", envEmail: "env@example.com", wantName: "Env Author", wantMail: "env@example.com"},
		{name: "explicit wins over env", envName: "Env Author", envEmail: "env@example.com",
			argName: "Arg Author", argEmail: "arg@example.com", wantName: "Arg Author", wantMail: "arg@example.com"},
		{name: "fields resolve independently", envEmail: "env@example.com", argName: "Arg Author",
			wantName: "Arg Author", wantMail: "env@example.com"},
		{name: "env name fills in beside an explicit email", envName: "Env Author", argEmail: "arg@example.com",
			wantName: "Env Author", wantMail: "arg@example.com"},
		// Emptiness, not blankness, falls through: a whitespace-only value is
		// a resolved value, never trimmed or skipped, matching the server rule.
		{name: "whitespace env value is kept", envName: " ", envEmail: " ", wantName: " ", wantMail: " "},
		// Dolt's parser fails a name ending in ')' and cuts any other ')',
		// so such a value falls back, field by field.
		{name: "env name ending in a paren falls back", envName: "Jane Doe (CI)", envEmail: "jane@example.com",
			wantName: "beads", wantMail: "jane@example.com"},
		{name: "env name with an inner paren falls back", envName: "Jane (CI) Doe", wantName: "beads", wantMail: "beads@local"},
		{name: "an open paren alone is kept", envName: "Jane (CI", wantName: "Jane (CI", wantMail: "beads@local"},
		{name: "env email with a paren falls back", envName: "Env Author", envEmail: "jane)x@example.com",
			wantName: "Env Author", wantMail: "beads@local"},
		{name: "env email with an angle bracket falls back", envEmail: "a>b@example.com", wantName: "beads", wantMail: "beads@local"},
		{name: "env email with an opening angle bracket falls back", envEmail: "a <b@example.com", wantName: "beads", wantMail: "beads@local"},
		{name: "an unusable explicit value falls through to env", envName: "Env Author", envEmail: "env@example.com",
			argName: "Arg (bot)", argEmail: "arg)@example.com", wantName: "Env Author", wantMail: "env@example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			captureCommitterWarnings(t)
			t.Setenv("GIT_AUTHOR_NAME", tc.envName)
			t.Setenv("GIT_AUTHOR_EMAIL", tc.envEmail)
			got := ResolveCommitter(tc.argName, tc.argEmail)
			if got.Name != tc.wantName || got.Email != tc.wantMail {
				t.Fatalf("ResolveCommitter(%q, %q) = %+v, want {%s %s}", tc.argName, tc.argEmail, got, tc.wantName, tc.wantMail)
			}
			if want := tc.wantName + " <" + tc.wantMail + ">"; got.Author() != want {
				t.Fatalf("Author() = %q, want %q", got.Author(), want)
			}
			// Every resolved identity must come back from Dolt's own parse
			// unchanged.
			if name, email, ok := parseDoltAuthor(got.Author()); !ok || name != got.Name || email != got.Email {
				t.Fatalf("Dolt parses Author() %q as (%q, %q, ok=%t), want (%q, %q)", got.Author(), name, email, ok, got.Name, got.Email)
			}
		})
	}
}

// TestParseDoltAuthorMatchesTheRejectedShapes proves the copy of Dolt's parser
// below reproduces what the rejected characters guard against, so the
// round-trip assertion above is checking something.
func TestParseDoltAuthorMatchesTheRejectedShapes(t *testing.T) {
	for _, tc := range []struct {
		author, wantName, wantEmail string
		wantOK                      bool
	}{
		{author: "Jane Doe <jane@example.com>", wantName: "Jane Doe", wantEmail: "jane@example.com", wantOK: true},
		{author: "Jane Doe (CI) <jane@example.com>", wantOK: false},
		{author: "Jane (CI) Doe <jane@example.com>", wantName: " Doe", wantEmail: "jane@example.com", wantOK: true},
		{author: "Jane <jane)x@example.com>", wantName: "Jane", wantEmail: "jane", wantOK: true},
		{author: "Jane <a>b@example.com>", wantName: "Jane", wantEmail: "ab@example.com", wantOK: true},
	} {
		name, email, ok := parseDoltAuthor(tc.author)
		if ok != tc.wantOK || name != tc.wantName || email != tc.wantEmail {
			t.Errorf("parseDoltAuthor(%q) = (%q, %q, %t), want (%q, %q, %t)", tc.author, name, email, ok, tc.wantName, tc.wantEmail, tc.wantOK)
		}
	}
}

// TestResolveCommitterWarnsOncePerSource pins the warning a skipped value
// prints: it names the variable, the character and the identity used
// instead, and a later resolution in the same process stays quiet.
func TestResolveCommitterWarnsOncePerSource(t *testing.T) {
	out := captureCommitterWarnings(t)
	t.Setenv("GIT_AUTHOR_NAME", "Jane Doe (CI)")
	t.Setenv("GIT_AUTHOR_EMAIL", "jane@example.com")

	ResolveCommitter("", "")
	want := `Warning: GIT_AUTHOR_NAME "Jane Doe (CI)" contains ')', which Dolt cannot store in a commit author; Dolt commits use "beads" instead` + "\n"
	if got := out.String(); got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}

	ResolveCommitter("", "")
	if got := out.String(); got != want {
		t.Fatalf("a second resolution warned again: %q", got)
	}

	t.Setenv("GIT_AUTHOR_EMAIL", "jane>x@example.com")
	ResolveCommitter("", "")
	if got := strings.TrimPrefix(out.String(), want); !strings.HasPrefix(got, `Warning: GIT_AUTHOR_EMAIL "jane>x@example.com" contains '>'`) ||
		!strings.HasSuffix(got, `Dolt commits use "beads@local" instead`+"\n") {
		t.Fatalf("email warning = %q, want one naming GIT_AUTHOR_EMAIL, '>' and beads@local", got)
	}
}

// captureCommitterWarnings points the warning at a buffer and forgets which
// sources already warned, for the duration of the test.
func captureCommitterWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := committerWarningOut
	committerWarningOut = &buf
	committerWarned.Clear()
	t.Cleanup(func() {
		committerWarningOut = prev
		committerWarned.Clear()
	})
	return &buf
}

// doltAuthorPattern is a copy of the pattern Dolt's cli.ParseAuthor reads
// DOLT_COMMIT's --author with (cmd/dolt/cli/arg_parser_helpers.go).
var doltAuthorPattern = regexp.MustCompile(`(?m)([^)]+) \<([^)]+)`)

// parseDoltAuthor parses author the way cli.ParseAuthor does.
func parseDoltAuthor(author string) (name, email string, ok bool) {
	m := doltAuthorPattern.FindStringSubmatch(author)
	if len(m) != 3 {
		return "", "", false
	}
	return m[1], strings.ReplaceAll(m[2], ">", ""), true
}
