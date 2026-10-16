package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newCtxTestCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "probe"}
	addCtxFlags(cmd.Flags())
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%q): %v", args, err)
	}
	return cmd
}

// TestCtxFromFlags pins the flag-to-filter contract: no flag means nil (the
// predicate off), any flag means a non-nil slice (even when empty), tokens are
// normalized and merged across --ctx and --ctx-file, and a bad file is an
// error rather than a silently empty context.
func TestCtxFromFlags(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctxFile := filepath.Join(dir, "ctx")
	if err := os.WriteFile(ctxFile, []byte("# the box\nhost=studio\n\n  gpu  \n# trailing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyFile, []byte("# nothing declared\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		args    []string
		want    []string
		wantNil bool
	}{
		{name: "no flag: undeclared", args: nil, wantNil: true},
		{name: "--ctx '' declares an empty context", args: []string{"--ctx", ""}, want: []string{}},
		{name: "empty file declares an empty context", args: []string{"--ctx-file", emptyFile}, want: []string{}},
		{name: "comma and repeat both split", args: []string{"--ctx", "a=1,b", "--ctx", "c"}, want: []string{"a=1", "b", "c"}},
		{name: "file merges after the flag tokens, trimmed, comments dropped", args: []string{"--ctx", "os=darwin", "--ctx-file", ctxFile}, want: []string{"os=darwin", "host=studio", "gpu"}},
		{name: "duplicates collapse", args: []string{"--ctx", "gpu,gpu", "--ctx-file", ctxFile}, want: []string{"gpu", "host=studio"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ctxFromFlags(newCtxTestCmd(t, tc.args...))
			if err != nil {
				t.Fatalf("ctxFromFlags: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil (undeclared), got %#v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want a non-nil slice %q, got nil", tc.want)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ctx = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("missing file is an error, not an empty context", func(t *testing.T) {
		t.Parallel()
		got, err := ctxFromFlags(newCtxTestCmd(t, "--ctx-file", filepath.Join(dir, "absent")))
		if err == nil || !strings.Contains(err.Error(), "--ctx-file") {
			t.Fatalf("want a --ctx-file error, got ctx=%#v err=%v", got, err)
		}
	})
}

// TestCtxModeConflict pins that a declared context is refused on the `bd ready`
// arms that do not run the ready query (--gated, --mol, --explain) and accepted
// everywhere else, and that an undeclared one never conflicts.
func TestCtxModeConflict(t *testing.T) {
	// Not parallel: captureStderr swaps the process-wide os.Stderr.

	if err := ctxModeConflict(false, true, true, "mol-1"); err != nil {
		t.Fatalf("undeclared context must never conflict: %v", err)
	}
	if err := ctxModeConflict(true, false, false, ""); err != nil {
		t.Fatalf("plain ready with a context must be accepted: %v", err)
	}
	for name, tc := range map[string]struct {
		gated, explain bool
		molID          string
		wantFlag       string
	}{
		"gated":   {gated: true, wantFlag: "--gated"},
		"mol":     {molID: "mol-1", wantFlag: "--mol"},
		"explain": {explain: true, wantFlag: "--explain"},
	} {
		// HandleErrorRespectJSON prints the message and returns an exit
		// error, so the flag name is on stderr, not in err.Error().
		var err error
		stderr := captureStderr(t, func() { err = ctxModeConflict(true, tc.gated, tc.explain, tc.molID) })
		if err == nil || !strings.Contains(stderr, tc.wantFlag) {
			t.Errorf("%s: want a conflict naming %s, got err=%v stderr=%q", name, tc.wantFlag, err, stderr)
		}
	}
}
