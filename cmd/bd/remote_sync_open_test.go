package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestIsRemoteSyncCommand pins which command gets the #6575 narrow lenient
// open. It is exactly `bd dolt pull` — the command the data-behind refusal
// prescribes, which before this fix was blocked by the refusal that prescribed
// it (the #4566 deadlock shape, one refusal over).
//
// The negative cases matter as much as the positive one: `bd sync` also pushes
// and can write issue rows, and a write against a stale schema is the hazard
// the gate exists for, so it is deliberately NOT enrolled. A bare `pull` with
// no `dolt` parent must not match either, or any future `bd <x> pull` would
// inherit an exemption nobody reviewed.
func TestIsRemoteSyncCommand(t *testing.T) {
	build := func(parent, child string) *cobra.Command {
		c := &cobra.Command{Use: child}
		if parent != "" {
			p := &cobra.Command{Use: parent}
			p.AddCommand(c)
		}
		return c
	}
	cases := []struct {
		parent, child string
		want          bool
	}{
		{parent: "dolt", child: "pull", want: true},
		{parent: "dolt", child: "push", want: false},
		{parent: "dolt", child: "commit", want: false},
		{parent: "vc", child: "pull", want: false},
		{parent: "", child: "pull", want: false},
		{parent: "", child: "sync", want: false},
	}
	for _, c := range cases {
		name := c.parent + " " + c.child
		t.Run(strings.TrimSpace(name), func(t *testing.T) {
			if got := isRemoteSyncCommand(build(c.parent, c.child)); got != c.want {
				t.Errorf("isRemoteSyncCommand(%q) = %v, want %v", name, got, c.want)
			}
		})
	}
}

// TestIsRemoteSyncCommandMatchesTheRealPullCommand guards the classification
// against the real command tree rather than a hand-built stand-in, so renaming
// or re-parenting `bd dolt pull` cannot silently drop the exemption and
// re-wedge the refused cohort.
func TestIsRemoteSyncCommandMatchesTheRealPullCommand(t *testing.T) {
	if !isRemoteSyncCommand(doltPullCmd) {
		t.Error("isRemoteSyncCommand(doltPullCmd) = false; the data-behind refusal's own remedy would be blocked again (#6575)")
	}
	if isRemoteSyncCommand(doltPushCmd) {
		t.Error("isRemoteSyncCommand(doltPushCmd) = true; only the pull may open past the refusal")
	}
	if isRemoteSyncCommand(doltCommitCmd) {
		t.Error("isRemoteSyncCommand(doltCommitCmd) = true; the commit has its own #4566 classification")
	}
}

// TestHandleRemoteMigrateGateJSON_DataBehind covers the review's F4: the
// agent-facing block for the #6575 stop used to carry only an opaque
// `fallback_reason: "data-behind"` while observed/expected/options stayed the
// blunt migrate-or-adopt default — so an agent reading the documented contract
// surfaced `bd migrate --force` (the bug) followed by a `bd dolt push` that is
// guaranteed to be rejected non-fast-forward while the clone is still behind,
// plus a `bd bootstrap` that no-ops. The prose remedy existed only in text
// --json never emitted.
func TestHandleRemoteMigrateGateJSON_DataBehind(t *testing.T) {
	capture := func(t *testing.T, gate *schema.RemoteMigrateGateError) map[string]interface{} {
		t.Helper()
		origStderr := os.Stderr
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatal(pipeErr)
		}
		os.Stderr = w
		defer func() { os.Stderr = origStderr }()
		handleRemoteMigrateGateJSON(gate)
		_ = w.Close()
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		var parsed map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, buf.String())
		}
		return parsed
	}

	for _, tc := range []struct {
		name      string
		diverged  bool
		wantShape string
	}{
		{name: "fast-forward shape", diverged: false, wantShape: "fast-forward"},
		{name: "diverged shape", diverged: true, wantShape: "diverged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &schema.RemoteMigrateGateError{
				CurrentVersion: 66, LatestVersion: 67, Pending: 1,
				FallbackReason: "data-behind",
				DataDiverged:   tc.diverged,
			}
			parsed := capture(t, gate)
			obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
			if !ok {
				t.Fatalf("remote_migrate_gate missing or wrong type: %T", parsed["remote_migrate_gate"])
			}
			// The machine-readable reason an agent matches on is unchanged.
			if got, _ := obj["fallback_reason"].(string); got != "data-behind" {
				t.Errorf("fallback_reason = %v, want \"data-behind\"", obj["fallback_reason"])
			}
			if _, ok := obj["decision"]; ok {
				t.Errorf("this is a blunt stop and must not carry a decision key, got %v", obj["decision"])
			}
			if got, _ := obj["data_behind_shape"].(string); got != tc.wantShape {
				t.Errorf("data_behind_shape = %v, want %q", obj["data_behind_shape"], tc.wantShape)
			}
			// observed/expected must describe THIS stop, not the
			// designated-migrator decision that does not apply to it.
			observed, _ := obj["observed"].(string)
			if !strings.Contains(observed, "BEHIND") {
				t.Errorf("observed = %q, want the behind-the-remote framing", observed)
			}
			expected, _ := obj["expected"].(string)
			if !strings.Contains(expected, schema.DataBehindRemedyCommand) {
				t.Errorf("expected = %q, want the pull-first remedy", expected)
			}
			if strings.Contains(expected, "designated clone migrates") {
				t.Errorf("expected = %q, still the blunt migrate-or-adopt framing", expected)
			}
			// The docs pointer is part of the same payload and was the one
			// field the observed/expected rewrite left behind: the default
			// anchor's ordering rule says `bd dolt pull` is refused on every
			// pending-migration open, contradicting the option above. Pin it
			// so the next rewrite of this arm cannot drop the pointer again.
			if got, _ := obj["docs"].(string); got != dataBehindDocsURL {
				t.Errorf("docs = %v, want the data-behind anchor %q", obj["docs"], dataBehindDocsURL)
			}
			// options must be the single measured remedy, and must not hand an
			// agent either of the two dead ends.
			rawOpts, ok := obj["options"].([]interface{})
			if !ok || len(rawOpts) != 1 {
				t.Fatalf("options = %v, want exactly one (the pull)", obj["options"])
			}
			o, _ := rawOpts[0].(map[string]interface{})
			if id, _ := o["id"].(string); id != "pull-first" {
				t.Errorf("option id = %v, want \"pull-first\"", o["id"])
			}
			cmds, _ := o["commands"].([]interface{})
			if len(cmds) != 1 || cmds[0] != schema.DataBehindRemedyCommand {
				t.Errorf("option commands = %v, want [%q]", cmds, schema.DataBehindRemedyCommand)
			}
			if o["when"] == nil || o["risk"] == nil {
				t.Errorf("option missing when/risk: %v", o)
			}
			// The top-level hint stays the non-runnable directive, and must
			// name the remedy rather than the escape hatch.
			hint, _ := parsed["hint"].(string)
			if hint != gate.AgentDirective() {
				t.Errorf("hint = %q, want the agent directive", hint)
			}
			if !strings.Contains(hint, schema.DataBehindRemedyCommand) {
				t.Errorf("hint = %q, want it to name the remedy", hint)
			}
			if hint == gate.EscapeHint() {
				t.Errorf("hint must not be the runnable escape command %q", gate.EscapeHint())
			}
			// Nothing anywhere in the block may offer the migrate/push pair or
			// the no-op bootstrap for this state.
			blob, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"bd bootstrap", "bd migrate --force"} {
				if strings.Contains(string(blob), banned) {
					t.Errorf("the data-behind block offers %q, which was measured not to work from this state:\n%s", banned, blob)
				}
			}
		})
	}

	// A shared, data-behind refusal carries the #5920 consent verb in its
	// second option, and that verb is target-scoped: under --global the
	// project-scoped `bd migrate schema` would consent the WRONG database and
	// leave the refusal in place. This stop reaches the renderer through the
	// default (empty-Decision) arm, so the retarget cannot be keyed on
	// Decision alone the way shared-no-remote's is.
	t.Run("shared under --global retargets the consent verb", func(t *testing.T) {
		origGlobal := globalFlag
		globalFlag = true
		defer func() { globalFlag = origGlobal }()

		gate := &schema.RemoteMigrateGateError{
			CurrentVersion: 66, LatestVersion: 67, Pending: 1,
			FallbackReason: "data-behind",
			Shared:         true,
		}
		parsed := capture(t, gate)
		obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
		if !ok {
			t.Fatalf("remote_migrate_gate missing or wrong type: %T", parsed["remote_migrate_gate"])
		}
		rawOpts, ok := obj["options"].([]interface{})
		if !ok || len(rawOpts) != 2 {
			t.Fatalf("options = %v, want the pull plus the shared consent step", obj["options"])
		}
		consent, _ := rawOpts[1].(map[string]interface{})
		cmds, _ := consent["commands"].([]interface{})
		// The forced form, not the bare one: this stop is remote-backed by
		// construction and the bare verb's consent is never read there.
		if len(cmds) != 1 || cmds[0] != schema.SharedConsentCommandForcedGlobal {
			t.Errorf("consent option commands = %v, want [%q] under --global", cmds, schema.SharedConsentCommandForcedGlobal)
		}
		// The pull is target-agnostic and must NOT be rewritten.
		pull, _ := rawOpts[0].(map[string]interface{})
		pullCmds, _ := pull["commands"].([]interface{})
		if len(pullCmds) != 1 || pullCmds[0] != schema.DataBehindRemedyCommand {
			t.Errorf("pull option commands = %v, want [%q] unchanged", pullCmds, schema.DataBehindRemedyCommand)
		}
	})
}

// TestHandleRemoteMigrateGateJSON_HumanDecisionRequired pins the one arm of
// this payload where human_decision_required is false, and — more importantly
// — the three where it must stay true.
//
// The field was hard-coded true, which made the data-behind payload contradict
// itself: the single option it carries is annotated `when: "always, for this
// stop"` and `risk: "none — a pure fast-forward"`, and AgentDirective says the
// same, yet an agent keying on this field stopped to ask a human to approve an
// unconditional, riskless step. The cost of that stall is the operator
// reaching for `bd migrate --force` instead — the #6575 wedge.
//
// The two narrowings are the substance of the finding: a diverged pull MERGES
// and can need conflict resolution (and a `--strategy ours|theirs` choice), and
// a shared store carries a SECOND option whose precondition is operator
// confirmation that every co-resident client is upgraded (#5920), which this
// process cannot observe. Either one flipping to false would be worse than the
// contradiction it replaced.
func TestHandleRemoteMigrateGateJSON_HumanDecisionRequired(t *testing.T) {
	capture := func(t *testing.T, gate *schema.RemoteMigrateGateError) map[string]interface{} {
		t.Helper()
		origStderr := os.Stderr
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatal(pipeErr)
		}
		os.Stderr = w
		defer func() { os.Stderr = origStderr }()
		handleRemoteMigrateGateJSON(gate)
		_ = w.Close()
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		var parsed map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, buf.String())
		}
		return parsed
	}

	for _, tc := range []struct {
		name string
		gate *schema.RemoteMigrateGateError
		want bool
		why  string
	}{
		{
			name: "data-behind, fast-forward, not shared",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, FallbackReason: "data-behind"},
			want: false,
			why:  "the single option is an unconditional, riskless, non-destructive pull",
		},
		{
			name: "data-behind, diverged",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, FallbackReason: "data-behind", DataDiverged: true},
			want: true,
			why:  "the pull merges and can need conflict resolution / a strategy choice",
		},
		{
			name: "data-behind, shared",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, FallbackReason: "data-behind", Shared: true},
			want: true,
			why:  "the second option needs operator confirmation that every co-resident client is upgraded (#5920)",
		},
		{
			name: "data-behind, diverged and shared",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, FallbackReason: "data-behind", DataDiverged: true, Shared: true},
			want: true,
			why:  "both narrowings apply",
		},
		{
			name: "blunt remote-backed stop",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1},
			want: true,
			why:  "only ONE clone may migrate a shared remote — a coordination decision",
		},
		{
			name: "adopt",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, Decision: "adopt"},
			want: true,
			why:  "adoption is destructive",
		},
		{
			name: "fork-skew",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, Decision: "fork-skew"},
			want: true,
			why:  "picking a canonical clone discards the others' unpushed work",
		},
		{
			name: "shared-no-remote",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, Decision: "shared-no-remote", Shared: true},
			want: true,
			why:  "other clients' binary versions are not observable from this process",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := capture(t, tc.gate)
			obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
			if !ok {
				t.Fatalf("remote_migrate_gate missing or wrong type: %T", parsed["remote_migrate_gate"])
			}
			got, ok := obj["human_decision_required"].(bool)
			if !ok {
				t.Fatalf("human_decision_required missing or not a bool: %T", obj["human_decision_required"])
			}
			if got != tc.want {
				t.Errorf("human_decision_required = %v, want %v — %s", got, tc.want, tc.why)
			}
			// The contradiction is only resolved if the rest of the payload
			// still says what it said: a false here has to come with the
			// unconditional, riskless single option it is claiming.
			if !tc.want {
				rawOpts, ok := obj["options"].([]interface{})
				if !ok || len(rawOpts) != 1 {
					t.Fatalf("human_decision_required=false with options = %v; it may only be false for the single-option pull", obj["options"])
				}
				o, _ := rawOpts[0].(map[string]interface{})
				if risk, _ := o["risk"].(string); !strings.HasPrefix(risk, "none") {
					t.Errorf("human_decision_required=false but option risk = %q; want a risk-free option", risk)
				}
				if when, _ := o["when"].(string); !strings.HasPrefix(when, "always") {
					t.Errorf("human_decision_required=false but option when = %q; want an unconditional option", when)
				}
			}
		})
	}
}
