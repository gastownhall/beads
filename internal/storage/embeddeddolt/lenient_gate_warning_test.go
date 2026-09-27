//go:build cgo

package embeddeddolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// dataBehindFallbackReason is the literal behind
// schema.RemoteMigrateGateError.IsDataBehind; the schema package keeps its
// fallback-reason values unexported. Every fixture using it is guarded by an
// IsDataBehind check so a drift here fails loudly instead of silently
// downgrading the case to a blunt refusal.
const dataBehindFallbackReason = "data-behind"

func TestLenientGateWarningBody_ShapedDecisions(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		shared   bool
		want     string
		unwanted string
		skew     []int
	}{
		{
			name:     "adopt",
			decision: "adopt",
			want:     "The remote has already been migrated by another clone",
			unwanted: "designated migrator",
		},
		{
			name:     "adopt fast-forward",
			decision: "adopt-ff",
			want:     "strict ancestor of the remote's",
			unwanted: "designated migrator",
		},
		{
			name:     "fork skew",
			decision: "fork-skew",
			skew:     []int{42},
			want:     "The schema has forked",
			unwanted: "designated migrator",
		},
		{
			// #5920's shared-store stop is the fourth non-empty Decision. It
			// is the shaped arm least likely to be exercised by hand in
			// embedded mode, which is exactly why the helper's predicate
			// needs it pinned: its recovery is the consent command, not the
			// blunt bullets' `bd migrate --force`.
			name:     "shared no remote",
			decision: "shared-no-remote",
			shared:   true,
			want:     "served to multiple clients",
			unwanted: "designated migrator",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gateErr := &schema.RemoteMigrateGateError{
				CurrentVersion: 41,
				LatestVersion:  42,
				Pending:        1,
				Decision:       tc.decision,
				SkewVersions:   tc.skew,
				Shared:         tc.shared,
			}

			warning := lenientGateWarningBody(gateErr)
			if !strings.Contains(warning, tc.want) {
				t.Errorf("warning is missing shaped guidance %q; got:\n%s", tc.want, warning)
			}
			if strings.Contains(warning, tc.unwanted) {
				t.Errorf("warning still contains blunt guidance %q; got:\n%s", tc.unwanted, warning)
			}
		})
	}
}

func TestLenientGateWarningBody_BluntRefusalKeepsSharedGuidance(t *testing.T) {
	gateErr := &schema.RemoteMigrateGateError{
		CurrentVersion: 41,
		LatestVersion:  42,
		Pending:        1,
	}

	warning := lenientGateWarningBody(gateErr)
	if !strings.Contains(warning, "designated migrator") {
		t.Fatalf("blunt refusal lost shared migrate-or-adopt guidance:\n%s", warning)
	}
}

// TestLenientGateWarning_ComposedOutput pins the COMPOSED warning — body plus
// mode line — for every intent that can reach it, against each gate shape.
//
// The termination assertion is the reason this test exists. #6660 moved the
// shared guidance, which had been the only `\n` at the end of the two
// non-data-behind arms, to the front of the body; both arms shipped
// unterminated and the package stayed green, because the helper-level tests
// above never see the composed text and the only composed assertions
// (remote_sync_gate_test.go) are BEADS_TEST_EMBEDDED_DOLT-gated and
// Contains-only, so they are blind to both termination and ordering.
func TestLenientGateWarning_ComposedOutput(t *testing.T) {
	shapes := []struct {
		name           string
		gateErr        *schema.RemoteMigrateGateError
		wantDataBehind bool
		wantBody       string
	}{
		{
			name: "blunt refusal",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 41,
				LatestVersion:  42,
				Pending:        1,
			},
			wantBody: "designated migrator",
		},
		{
			name: "data-behind fallback",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 41,
				LatestVersion:  42,
				Pending:        1,
				FallbackReason: dataBehindFallbackReason,
			},
			wantDataBehind: true,
			wantBody:       schema.DataBehindRemedyCommand,
		},
		{
			name: "shaped decision",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 41,
				LatestVersion:  42,
				Pending:        1,
				Decision:       "adopt",
			},
			wantBody: "The remote has already been migrated by another clone",
		},
	}

	// openStrict is absent on purpose: toleratesGateRefusal never admits it,
	// so it cannot reach this renderer.
	intents := []struct {
		name     string
		intent   openIntent
		wantMode string
	}{
		{
			name:     "read-only command",
			intent:   openReadOnlyCommand,
			wantMode: "Read-only command: continuing on schema v41",
		},
		{
			name:     "working-set reconcile",
			intent:   openWorkingSetReconcile,
			wantMode: "Working-set reconcile command: continuing on schema v41",
		},
		{
			name:     "remote sync",
			intent:   openRemoteSync,
			wantMode: "Remote-sync command: continuing on schema v41",
		},
	}

	for _, shape := range shapes {
		if got := shape.gateErr.IsDataBehind(); got != shape.wantDataBehind {
			t.Fatalf("%s fixture: IsDataBehind() = %v, want %v — the fixture no longer builds the shape it names", shape.name, got, shape.wantDataBehind)
		}
		for _, intent := range intents {
			t.Run(shape.name+"/"+intent.name, func(t *testing.T) {
				got := lenientGateWarning(intent.intent, shape.gateErr)

				if !strings.HasSuffix(got, "\n") {
					t.Errorf("composed warning is not newline-terminated, so the command's own output is glued onto it; got:\n%q", got)
				}
				if !strings.HasPrefix(got, "Warning: ") {
					t.Errorf("composed warning does not open with the Warning: prefix; got:\n%s", got)
				}
				if !strings.Contains(got, intent.wantMode) {
					t.Errorf("composed warning is missing the mode line %q; got:\n%s", intent.wantMode, got)
				}

				if intent.intent == openRemoteSync {
					// This arm is the data-behind stop's own remedy running,
					// so it renders the one-line summary rather than the
					// body. What it must never do is name the migrate the
					// stop exists to prevent — which the data-behind body
					// itself mentions, so routing the body through here
					// would regress it silently.
					if strings.Contains(got, "bd migrate --force") {
						t.Errorf("remote-sync confirmation names `bd migrate --force`; got:\n%s", got)
					}
					return
				}
				if !strings.Contains(got, shape.wantBody) {
					t.Errorf("composed warning is missing the %s body text %q; got:\n%s", shape.name, shape.wantBody, got)
				}
			})
		}
	}
}
