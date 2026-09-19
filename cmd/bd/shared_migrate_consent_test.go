package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// captureNoticeStderr runs fn with os.Stderr redirected and returns what it
// wrote.
func captureNoticeStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

// TestIsSchemaMigrateVerbScope pins which invocations count as consent to
// migrate a shared database (gastownhall/beads#5920). Only `bd migrate schema`
// does. Bare `bd migrate` reconciles version/repo-id metadata and never
// applies a schema migration itself, and its flag modes are further still from
// schema work — `bd migrate --update-repo-id` is repo-fingerprint surgery, and
// treating it as consent would let a repo-ID update promote the schema for
// every co-resident client as a side effect.
func TestIsSchemaMigrateVerbScope(t *testing.T) {
	for _, tt := range []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{name: "migrate schema consents", cmd: migrateSchemaCmd, want: true},
		{name: "bare migrate does not", cmd: migrateCmd, want: false},
		{name: "migrate sync does not", cmd: migrateSyncCmd, want: false},
		{name: "an unrelated command does not", cmd: rootCmd, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSchemaMigrateVerb(tt.cmd); got != tt.want {
				t.Fatalf("isSchemaMigrateVerb(%q) = %t, want %t", tt.cmd.Name(), got, tt.want)
			}
		})
	}
}

// TestNoticeSharedMigrateRefusalPerDecision covers the one-shot version-bump
// notice. It fires once per upgrade (.local_version is consumed in the same
// pre-run), so the remedy it names has to be right for THIS refusal — the gate
// returns one error type for several decisions whose correct actions differ,
// and the migrate verb does not unlock the remote-backed ones at all.
func TestNoticeSharedMigrateRefusalPerDecision(t *testing.T) {
	// The notice self-suppresses under --json, so if jsonOutput arrives here
	// already true, every case below reads as "the notice printed nothing" —
	// which is exactly how a leak from an earlier test in the package
	// presents, and is how this test failed in full-package runs while passing
	// under a narrow -run. Pin it, so a failure here can only ever be about
	// this code.
	pinJSONOutput(t, false)

	for _, tt := range []struct {
		decision string
		want     string
		absent   string
	}{
		{decision: "shared-no-remote", want: "bd migrate schema"},
		{decision: "adopt", want: "bd bootstrap", absent: "bd migrate schema"},
		{decision: "adopt-ff", want: "bd bootstrap", absent: "bd migrate schema"},
		{decision: "fork-skew", want: "bd doctor", absent: "bd migrate schema"},
		{decision: "", want: "bd migrate", absent: "bd migrate schema"},
	} {
		name := tt.decision
		if name == "" {
			name = "blunt stop"
		}
		t.Run(name, func(t *testing.T) {
			out := captureNoticeStderr(t, func() {
				noticeSharedMigrateRefusal(&schema.RemoteMigrateGateError{
					CurrentVersion: 65, LatestVersion: 66, Pending: 1,
					Decision: tt.decision,
				})
			})
			if !strings.Contains(out, tt.want) {
				t.Errorf("notice for %q missing %q:\n%s", tt.decision, tt.want, out)
			}
			if tt.absent != "" && strings.Contains(out, tt.absent) {
				t.Errorf("notice for %q must not prescribe %q (the verb does not unlock this arm):\n%s",
					tt.decision, tt.absent, out)
			}
		})
	}

	// --json puts a machine-readable gate block on this same stream a moment
	// later; prose prepended to it makes the documented contract unparseable.
	t.Run("suppressed in json mode", func(t *testing.T) {
		pinJSONOutput(t, true)
		out := captureNoticeStderr(t, func() {
			noticeSharedMigrateRefusal(&schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 66, Pending: 1,
				Decision: "shared-no-remote",
			})
		})
		if out != "" {
			t.Errorf("notice must be silent in --json mode, got:\n%s", out)
		}
	})

	t.Run("global flag names the global remedy", func(t *testing.T) {
		orig := globalFlag
		globalFlag = true
		defer func() { globalFlag = orig }()
		out := captureNoticeStderr(t, func() {
			noticeSharedMigrateRefusal(&schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 66, Pending: 1,
				Decision: "shared-no-remote",
			})
		})
		if !strings.Contains(out, schema.SharedConsentCommandGlobal) {
			t.Errorf("notice under --global must name %q:\n%s", schema.SharedConsentCommandGlobal, out)
		}
	})

	t.Run("an untyped error prints nothing", func(t *testing.T) {
		out := captureNoticeStderr(t, func() { noticeSharedMigrateRefusal(io.EOF) })
		if out != "" {
			t.Errorf("only a gate refusal should produce a notice, got:\n%s", out)
		}
	})
}

// TestSharedRefusalPrescribesAWorkingVerb closes the loop the review flagged:
// the shared-store refusal names a command, and that command must actually run
// wherever the refusal can appear. In proxied-server mode `bd migrate schema`
// used to hard-refuse, so the only remedy the gate offered there was a dead
// end — worse, the root pre-run would have consumed the consent on the open
// before RunE ever printed "not supported".
//
// The pairing is checked structurally rather than by driving a proxied
// workspace: the command the gate prescribes IS SharedConsentCommand, and the
// verb that grants consent for it is migrateSchemaCmd.
func TestSharedRefusalPrescribesAWorkingVerb(t *testing.T) {
	refusal := &schema.RemoteMigrateGateError{
		CurrentVersion: 65, LatestVersion: 66, Pending: 1,
		Decision: "shared-no-remote", Shared: true,
	}

	if got := refusal.EscapeHint(); got != schema.SharedConsentCommand {
		t.Fatalf("EscapeHint = %q, want %q", got, schema.SharedConsentCommand)
	}
	opts := refusal.Options()
	if len(opts) != 1 || len(opts[0].Commands) != 1 || opts[0].Commands[0] != schema.SharedConsentCommand {
		t.Fatalf("Options = %+v, want the single %q remedy", opts, schema.SharedConsentCommand)
	}
	if !strings.Contains(refusal.UserMessage(), schema.SharedConsentCommand) {
		t.Errorf("UserMessage must name the remedy:\n%s", refusal.UserMessage())
	}

	// The prescribed command is `bd <path of migrateSchemaCmd>`, and that is
	// exactly the command isSchemaMigrateVerb grants consent for.
	wantPath := "bd " + strings.TrimPrefix(migrateSchemaCmd.CommandPath(), rootCmd.Name()+" ")
	if wantPath != schema.SharedConsentCommand {
		t.Fatalf("the gate prescribes %q but the consenting verb is %q", schema.SharedConsentCommand, wantPath)
	}
	if !isSchemaMigrateVerb(migrateSchemaCmd) {
		t.Fatal("the command the gate prescribes must be the one that grants consent")
	}
}

// TestPrintGlobalDatabaseConsentHint pins the one fact the gate's own block
// cannot know: which database the invocation targeted. Under --global the open
// hits `beads_global`, so the block's unflagged `bd migrate schema` would
// migrate the project database and leave the refusal in place.
func TestPrintGlobalDatabaseConsentHint(t *testing.T) {
	orig := globalFlag
	defer func() { globalFlag = orig }()

	globalFlag = false
	var off bytes.Buffer
	printGlobalDatabaseConsentHint(&off)
	if off.Len() != 0 {
		t.Errorf("no hint without --global, got:\n%s", off.String())
	}

	globalFlag = true
	var on bytes.Buffer
	printGlobalDatabaseConsentHint(&on)
	if !strings.Contains(on.String(), schema.SharedConsentCommandGlobal) {
		t.Errorf("hint must name %q:\n%s", schema.SharedConsentCommandGlobal, on.String())
	}
}
