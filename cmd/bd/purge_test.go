package main

import (
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/issueops"
)

// TestParseOlderThan pins --older-than on both `bd purge` and `bd prune`.
// Day-denominated values keep their meaning; hour (and finer) values are taken
// exactly rather than floored to whole days — "36h" used to become 24h and
// sweep rows younger than the caller asked for.
func TestParseOlderThan(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		// Unchanged day semantics.
		{"7", 7 * day, false},
		{"30", 30 * day, false},
		{"7d", 7 * day, false},
		{"30d", 30 * day, false},
		{"2w", 14 * day, false},
		{"1w", 7 * day, false},
		{"7D", 7 * day, false},
		{"2W", 14 * day, false},
		// Hour precision, no longer floored to days.
		{"48h", 48 * time.Hour, false},
		{"168h", 7 * day, false},
		{"36h", 36 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"36H", 36 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		// Refusals.
		{"", 0, true},
		{"0", 0, true},
		{"-1", 0, true},
		{"0d", 0, true},
		{"0h", 0, true},
		{"-36h", 0, true},
		{"abc", 0, true},
		{"7x", 0, true},
		{"d", 0, true},
		// Out of range must be refused, never wrapped: 213504d used to
		// overflow to 25m26s and select almost every closed row.
		{"213504d", 0, true},
		{"213504", 0, true},
		{"30501w", 0, true},
		{"106752d", 0, true},
		{"9223372036854775807d", 0, true},
		{"99999999999h", 0, true},
		// The largest representable day count still parses.
		{"106751d", 106751 * day, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseOlderThan(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseOlderThan(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseOlderThan(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

// TestPurgeConfirmHint pins the --force command an unconfirmed purge or prune
// suggests. It is pasted as printed, so it has to keep every narrowing flag the
// preview honoured — --exclude-label above all: dropping it deleted the very
// beads the preview had just reported as skipped.
func TestPurgeConfirmHint(t *testing.T) {
	purge := purgeScope{cmdName: "purge", tier: issueops.SweepEphemeral}
	plane := purgeScope{cmdName: "purge", tier: issueops.SweepWispsPlane}
	prune := purgeScope{cmdName: "prune", tier: issueops.SweepDurable}
	tests := []struct {
		name               string
		scope              purgeScope
		olderThan, pattern string
		labels             []string
		want               string
	}{
		{"bare", purge, "", "", nil, "bd purge --force"},
		{"prune", prune, "7d", "", nil, "bd prune --force --older-than 7d"},
		{"exclude label", purge, "", "", []string{"my:message"}, "bd purge --force --exclude-label my:message"},
		{"every flag", plane, "168h", "*", []string{"keep:me", "my:message"},
			"bd purge --force --wisps-plane --older-than 168h --pattern * --exclude-label keep:me,my:message"},
		// The guard's own normalization: padding trimmed, empties and repeats dropped.
		{"normalized", purge, "", "", []string{" my:message ", "", "my:message"}, "bd purge --force --exclude-label my:message"},
		{"only empties", purge, "", "", []string{"", " "}, "bd purge --force"},
		// CSV-encoded for the flag, then quoted for the shell.
		{"space", purge, "", "", []string{"my label"}, `bd purge --force --exclude-label 'my label'`},
		{"comma", purge, "", "", []string{"a,b", "c"}, `bd purge --force --exclude-label '"a,b",c'`},
		{"single quote", purge, "", "", []string{"it's"}, `bd purge --force --exclude-label 'it'\''s'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := purgeConfirmHint(tt.scope, tt.olderThan, tt.pattern, tt.labels); got != tt.want {
				t.Errorf("purgeConfirmHint = %s\nwant              %s", got, tt.want)
			}
		})
	}
}

// TestPurgeConfirmHintLabelsRoundTrip reads the hint back the way a pasted one
// is read — split by a real shell, then parsed by the flag's own parser — and
// wants every label back whole. Labels are free text: a hint the shell splits,
// expands or globs protects a fragment, and the rest becomes an argument the
// command ignores.
func TestPurgeConfirmHintLabelsRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hint is POSIX shell syntax")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	// The flag set below stands in for purgeCmd's; the real flag's type is
	// what decides how its value is split.
	if got := purgeCmd.Flags().Lookup("exclude-label").Value.Type(); got != "stringSlice" {
		t.Fatalf("--exclude-label is a %s; this test parses it as a stringSlice", got)
	}
	scope := purgeScope{cmdName: "purge", tier: issueops.SweepEphemeral}
	for _, labels := range [][]string{
		{"keep:me"},
		{"keep:me", "my:message"},
		{"my label"},
		{"a,b", "c"},
		{"it's"},
		{`say "hi"`},
		{"$HOME"},
		{"*"},
		{"~"},
		{"#c"},
		{"a;b"},
		{"x|y"},
		{"ünïcode"},
	} {
		hint := purgeConfirmHint(scope, "7d", "", labels)
		out, err := exec.Command("sh", "-c", "for w in "+hint+`; do printf '%s\0' "$w"; done`).Output()
		if err != nil {
			t.Fatalf("sh rejected the hint %s: %v", hint, err)
		}
		words := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if len(words) < 2 || words[0] != "bd" || words[1] != "purge" {
			t.Fatalf("the hint %s split into %q", hint, words)
		}
		flags := pflag.NewFlagSet("purge", pflag.ContinueOnError)
		flags.Bool("force", false, "")
		flags.String("older-than", "", "")
		got := flags.StringSlice("exclude-label", nil, "")
		if err := flags.Parse(words[2:]); err != nil {
			t.Fatalf("the hint %s: parsing %q: %v", hint, words[2:], err)
		}
		if !slices.Equal(*got, labels) || flags.NArg() != 0 {
			t.Errorf("the hint %s read back as --exclude-label %q plus stray arguments %q; want %q and none",
				hint, *got, flags.Args(), labels)
		}
	}
}
