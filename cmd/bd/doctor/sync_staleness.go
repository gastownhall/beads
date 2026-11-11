package doctor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/pushstate"
	"github.com/steveyegge/beads/internal/storage"
)

// syncStalenessInputs is everything the Sync Staleness check learned about
// the inspected workspace. CheckFederationSyncStaleness gathers it from the
// store and the beads directory; evaluateSyncStaleness below turns it into
// the reported check. Keeping the aggregation pure means its every
// combination — recorded failures beside equal cached refs, disabled pushes
// beside peer warnings — is testable without a Dolt server (GH#6289).
type syncStalenessInputs struct {
	// HasRemotes reports whether any remote is configured at all.
	HasRemotes bool
	// OriginConfigured reports whether an "origin" remote exists.
	OriginConfigured bool
	// OriginSkipReason names the workspace setting that keeps every bd push
	// path off origin ("dolt.local-only", or "no-push" while dolt.auto-push
	// is off); empty means the leg runs. A skipped origin adds no findings,
	// but the check must still say it was skipped rather than silently
	// report a clean origin.
	OriginSkipReason string
	// OriginAutoPushDespiteNoPush reports that no-push is set but
	// dolt.auto-push still pushes to origin, so the leg ran anyway. The
	// check says why, because no-push refuses the 'bd dolt push' its
	// guidance names.
	OriginAutoPushDespiteNoPush bool
	// OriginStatus and OriginStatusErr are SyncStatus's cached-ref
	// comparison against origin: counts relative to the local tracking ref,
	// or the reason no comparison was possible.
	OriginStatus    *storage.SyncStatus
	OriginStatusErr error
	// PushState and PushStateErr are the parsed .beads/push-state.json: the
	// recorded success watermarks and any unresolved failed attempt. An
	// unreadable file is an error so the check can say local history cannot
	// be read; a missing file is neutral (never-pushed is not a failure).
	PushState    *pushstate.State
	PushStateErr error
	// PeerWarnings and TotalPeerBehind carry the per-peer findings the check
	// already produced before origin joined it, unchanged in wording.
	PeerWarnings    []string
	TotalPeerBehind int
}

// originCheckSkipReason reports the workspace setting that keeps every bd
// push path off origin in the inspected workspace, or "" when one can still
// push there and the origin leg must run:
//
//   - "dolt.local-only" stops bd dolt push, bd sync and auto-push alike.
//   - "no-push" stops bd dolt push and bd sync's push, but auto-push ignores
//     it, so it is a skip reason only while dolt.auto-push is off. With both
//     on, the leg runs, because a failed auto-push must not hide behind
//     no-push, and autoPushDespiteNoPush lets the check say why.
//
// Each setting is read the way bd resolves it inside that workspace: from its
// .beads/config.yaml, with a config.local.yaml beside it taking precedence
// (config.WorkspaceYamlSettingWithLocal). The process's global config is never
// consulted, because doctor inspects the workspace at path, which is not
// necessarily the one the running bd command operates in. User-level config
// and environment overrides are therefore invisible to it: a documented
// diagnostic limit, not a verdict. A setting that cannot be read counts as
// unset, so the leg runs rather than risk hiding a recorded failure (GH#6289).
func originCheckSkipReason(beadsDir string) (reason string, autoPushDespiteNoPush bool) {
	if workspaceSettingIsTrue(beadsDir, "dolt.local-only") {
		return "dolt.local-only", false
	}
	if !workspaceSettingIsTrue(beadsDir, "no-push") {
		return "", false
	}
	if workspaceSettingIsTrue(beadsDir, "dolt.auto-push") {
		return "", true
	}
	return "no-push", false
}

// workspaceSettingIsTrue reports whether beadsDir's own config sets key to a
// value strconv.ParseBool reads as true. That is viper's GetBool rule for a
// string, so a padded " true " stays false here as it does for bd. A missing,
// unreadable or unparseable value is false.
func workspaceSettingIsTrue(beadsDir, key string) bool {
	v, present, err := config.WorkspaceYamlSettingWithLocal(beadsDir, key)
	if err != nil || !present {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// originAutoPushNote is the detail line for a workspace whose origin leg ran
// despite no-push (originCheckSkipReason).
const originAutoPushNote = "origin checked despite no-push: true, because dolt.auto-push: true still pushes to origin (no-push stops only 'bd dolt push' and bd sync's push)"

// originFindings lists the origin leg's findings: an unreadable push record
// or a recorded failed attempt, then the cached-ref comparison. A recorded
// failure is reported even when the cached refs look equal, because the
// recording is positive evidence the last attempt did not land; equal cached
// refs are only the absence of local divergence.
func originFindings(in syncStalenessInputs) []string {
	var findings []string
	if in.PushStateErr != nil {
		findings = append(findings, "origin: local push history could not be read: "+in.PushStateErr.Error())
	} else if in.PushState != nil && in.PushState.LastError != "" {
		// A recorded, unresolved failure. Its timestamps may be equal to
		// last_push's (RFC3339 second precision) or missing entirely;
		// neither may hide the failure.
		attempt := in.PushState.LastAttempt
		if attempt == "" {
			attempt = "time unknown"
		}
		findings = append(findings, fmt.Sprintf(
			"origin: last push attempt at %s failed: %s (the remote may not hold this workspace's recent commits; retry 'bd dolt push' once the cause is resolved)",
			attempt, in.PushState.LastError))
	}
	st := in.OriginStatus
	switch {
	case in.OriginStatusErr != nil:
		findings = append(findings, fmt.Sprintf(
			"origin: comparison with cached remote refs unavailable: %v", in.OriginStatusErr))
	case st == nil:
		findings = append(findings, "origin: comparison with cached remote refs unavailable")
	case st.LocalAhead < 0 || st.LocalBehind < 0:
		// SyncStatus reports -1 both for a tracking ref that does not
		// exist yet and for a comparison query that failed, so the
		// finding names both causes.
		findings = append(findings, "origin: comparison with cached remote refs unavailable: "+
			"no cached origin tracking ref yet (a first 'bd dolt push' or 'bd dolt pull' creates it), or the comparison failed")
	case st.LocalAhead > 0 && st.LocalBehind > 0:
		findings = append(findings, fmt.Sprintf(
			"origin: local history and the cached origin tracking ref have diverged (local is %d commit(s) ahead and %d behind); pull ('bd dolt pull'), resolve, then push ('bd dolt push')",
			st.LocalAhead, st.LocalBehind))
	case st.LocalAhead > 0:
		findings = append(findings, fmt.Sprintf(
			"origin: local is %d commit(s) ahead of the cached origin tracking ref; check the remote received them or push again ('bd dolt push')",
			st.LocalAhead))
	case st.LocalBehind > 0:
		findings = append(findings, fmt.Sprintf(
			"origin: local is %d commit(s) behind the cached origin tracking ref; pull ('bd dolt pull')",
			st.LocalBehind))
	}
	return findings
}

// evaluateSyncStaleness aggregates the Sync Staleness findings (GH#6289).
// The check is offline by design: it reports what the workspace itself
// knows — push-state.json's recorded attempts and SyncStatus's comparison
// against cached tracking refs — and never claims to know the remote's
// current contents.
func evaluateSyncStaleness(in syncStalenessInputs) DoctorCheck {
	check := DoctorCheck{Name: "Sync Staleness", Category: CategoryFederation}
	if !in.HasRemotes {
		check.Status = StatusOK
		check.Message = "No peers configured"
		return check
	}

	originChecked := in.OriginConfigured && in.OriginSkipReason == ""
	var findings []string
	if originChecked {
		findings = originFindings(in)
	}
	originFound := len(findings) > 0
	findings = append(findings, in.PeerWarnings...)

	// Lines that frame the findings: why origin was checked or skipped
	// first, what the evidence is worth last.
	var lead, trail string
	switch {
	case originChecked:
		if in.OriginAutoPushDespiteNoPush {
			lead = originAutoPushNote + "\n"
		}
		trail = "\nCompared against cached origin refs and recorded push attempts; both are local evidence and can be stale."
	case in.OriginConfigured:
		lead = fmt.Sprintf("origin check skipped (%s: true)\n", in.OriginSkipReason)
	}

	if len(findings) > 0 {
		check.Status = StatusWarning
		switch {
		case originFound && len(in.PeerWarnings) > 0:
			check.Message = "Origin and peer sync staleness detected"
			check.Fix = "Address the origin findings above, then run 'bd federation sync' to synchronize with peers"
		case originFound:
			check.Message = "Origin sync staleness detected"
			check.Fix = "Follow the origin guidance above ('bd dolt push' / 'bd dolt pull' as the findings direct)"
		default:
			// Peer-only findings keep the check's established wording.
			check.Message = fmt.Sprintf("%d total commits behind peers", in.TotalPeerBehind)
			check.Fix = "Run 'bd federation sync' to synchronize with peers"
		}
		check.Detail = lead + strings.Join(findings, "\n") + trail
		return check
	}

	check.Status = StatusOK
	switch {
	case originChecked:
		check.Message = "No local sync staleness detected"
		check.Detail = lead + "origin's cached tracking ref matches local history and no failed push attempt is recorded. " +
			"Cached refs and recorded attempts are local evidence: they do not verify the remote's current contents."
	case in.OriginConfigured:
		check.Message = fmt.Sprintf("Origin check skipped (%s: true); no peer staleness detected", in.OriginSkipReason)
		check.Detail = fmt.Sprintf(
			"%s keeps this workspace from pushing, so Sync Staleness cannot judge origin sync from here; its recorded push state was left unread.",
			in.OriginSkipReason)
	default:
		check.Message = "Sync is up to date"
	}
	return check
}
