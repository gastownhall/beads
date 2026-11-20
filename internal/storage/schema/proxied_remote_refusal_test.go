package schema

import (
	"strings"
	"testing"
)

// bluntRemoteBody is the blunt #4515 body server and embedded mode print,
// pinned byte for byte: bd-5hl9dg rewords it for proxied-server workspaces
// only, and the other topologies must keep reading exactly this.
const bluntRemoteBody = "\n" +
	"  This database syncs with a remote. Applying schema migrations on more than\n" +
	"  one clone independently forks the schema so `bd dolt pull` can no longer\n" +
	"  merge — the break is silent and unrecoverable.\n" +
	"\n" +
	"  Choose one:\n" +
	"    • You are the designated migrator (only ONE machine should be): migrate,\n" +
	"      then publish the migrated database to the remote:\n" +
	"        bd migrate --force\n" +
	"        (or BD_ALLOW_REMOTE_MIGRATE=1 bd migrate in scripted/CI use)\n" +
	"        bd dolt push\n" +
	"    • Another machine has already migrated: adopt its database instead of\n" +
	"      migrating here — re-clone from the remote so you receive the migrated\n" +
	"      schema:\n" +
	"        bd bootstrap\n" +
	"      Re-cloning replaces your local database: any local issues you have not\n" +
	"      pushed are LOST. Push first (`bd dolt push`) or save a copy\n" +
	"      (`bd export --all -o backup.jsonl`) before re-cloning.\n" +
	"\n" +
	"  Several machines on one remote? Only ONE migrates. Get every other clone\n" +
	"  fully in sync on its current binary, then after the migrator pushes run\n" +
	"  `bd dolt pull` on each before upgrading it — its upgrade then has nothing\n" +
	"  to migrate and needs no re-clone.\n"

func bluntRemoteRefusal(shared, proxied bool, reason string) *RemoteMigrateGateError {
	return &RemoteMigrateGateError{
		CurrentVersion: 66,
		LatestVersion:  74,
		Pending:        8,
		Shared:         shared,
		Proxied:        proxied,
		FallbackReason: reason,
	}
}

// TestBluntRemoteRefusalUnchangedOffProxied pins the blunt refusal server and
// embedded mode print. It passes before and after bd-5hl9dg: that change
// must not move a byte of it.
func TestBluntRemoteRefusalUnchangedOffProxied(t *testing.T) {
	for _, tc := range []struct {
		name   string
		shared bool
		reason string
	}{
		{name: "server mode (shared store)", shared: true, reason: fallbackReasonSharedStore},
		{name: "embedded (unreadable remote state)", reason: fallbackReasonUnreadableState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := bluntRemoteRefusal(tc.shared, false, tc.reason)
			want := "refusing to auto-apply 8 pending schema migrations to a remote-backed database (v66 -> v74): migrating clones independently forks the schema (#4259)\n" +
				bluntRemoteBody + e.fallbackReasonNote()
			if got := e.UserMessage(); got != want {
				t.Errorf("UserMessage changed off proxied-server mode.\n got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// TestBluntRemoteRefusalProxied covers bd-5hl9dg: on a proxied-server
// workspace whose database has a Dolt remote (the `bd init --proxied-server`
// default in a repository with a git origin), the blunt refusal used to offer
// `bd migrate --force`, `BD_ALLOW_REMOTE_MIGRATE=1 bd migrate`,
// `bd dolt push` and `bd dolt pull`, which the proxied front door refuses,
// and `bd bootstrap`, which creates a stray local database there. It has to
// name what that workspace can run instead.
func TestBluntRemoteRefusalProxied(t *testing.T) {
	msg := bluntRemoteRefusal(true, true, fallbackReasonSharedStore).UserMessage()

	for _, want := range []string{
		"proxied-server mode",
		"\n        " + SharedConsentCommandForced + "\n",
		"ONE clone of that remote",
		"library client",
		"server host",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("proxied blunt refusal missing %q:\n%s", want, msg)
		}
	}
	// None of these may appear as a command to run. Each is a line the
	// ordinary body prints on its own; the proxied body only names them in
	// prose, as refused or as run on the server host.
	for _, refused := range []string{
		"\n        bd migrate --force\n",
		"=1 bd migrate in scripted/CI use",
		// Refused without --force by the proxied front door (#6656).
		AllowRemoteMigrateEnv + "=1 " + SharedConsentCommand,
		"\n        bd dolt push\n",
		"\n        bd bootstrap\n",
		"Push first (`bd dolt push`)",
		"run\n  `bd dolt pull` on each",
	} {
		if strings.Contains(msg, refused) {
			t.Errorf("proxied blunt refusal still prescribes %q, which this workspace cannot run:\n%s", refused, msg)
		}
	}
	// The decision and its one-line summary are unchanged: only the guidance
	// is reworded.
	if !strings.HasPrefix(msg, bluntRemoteRefusal(true, false, fallbackReasonSharedStore).Error()+"\n") {
		t.Errorf("proxied refusal changed its summary line:\n%s", msg)
	}
	if !strings.HasSuffix(msg, bluntRemoteRefusal(true, true, fallbackReasonSharedStore).fallbackReasonNote()) {
		t.Errorf("proxied refusal dropped the smart gate note:\n%s", msg)
	}
}

// TestRemoteRefusalEscapeHint pins the escape command off proxied-server mode
// (unchanged by bd-5hl9dg) and checks that a proxied workspace is given the
// one migrate verb its front door lets through. The proxied front door refuses
// the bare `bd migrate` on every arm, so the forced two-word verb replaces it
// on every remote-backed arm; shared-no-remote already did this.
func TestRemoteRefusalEscapeHint(t *testing.T) {
	arms := []struct {
		name string
		e    RemoteMigrateGateError
	}{
		{name: "blunt (shared store)", e: RemoteMigrateGateError{Shared: true, FallbackReason: fallbackReasonSharedStore}},
		{name: "blunt (embedded)", e: RemoteMigrateGateError{FallbackReason: fallbackReasonUnreadableState}},
		{name: "adopt", e: RemoteMigrateGateError{Shared: true, Decision: gateDecisionAdopt}},
		{name: "adopt-ff", e: RemoteMigrateGateError{Shared: true, Decision: gateDecisionAdoptFastForward}},
		{name: "fork-skew", e: RemoteMigrateGateError{Shared: true, Decision: gateDecisionForkSkew, SkewVersions: []int{70}}},
		{name: "data-behind", e: RemoteMigrateGateError{Shared: true, FallbackReason: fallbackReasonDataBehind}},
	}
	for _, arm := range arms {
		t.Run(arm.name+"/off proxied", func(t *testing.T) {
			e := arm.e
			if got := e.EscapeHint(); got != "bd migrate --force" {
				t.Errorf("EscapeHint = %q, want %q (unchanged off proxied)", got, "bd migrate --force")
			}
		})
		t.Run(arm.name+"/proxied", func(t *testing.T) {
			e := arm.e
			e.Proxied = true
			if got := e.EscapeHint(); got != SharedConsentCommandForced {
				t.Errorf("EscapeHint = %q, want %q", got, SharedConsentCommandForced)
			}
		})
	}
}
