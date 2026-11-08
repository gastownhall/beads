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
	// OriginSkipReason names the workspace setting that keeps the origin leg
	// from running ("no-push" or "dolt.local-only"); empty means the leg
	// runs. A skipped origin adds no findings, but the check must still say
	// it was skipped rather than silently report a clean origin.
	OriginSkipReason string
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

// originCheckSkipReason reports the workspace setting that keeps bd from
// pushing in the inspected workspace — "no-push" or "dolt.local-only" — or ""
// when pushes are allowed. It reads the workspace's own config.yaml through
// config.GetStringFromDir rather than the process's global config, because
// doctor inspects the workspace at path, which is not necessarily the one
// the running bd command operates in. An env-only override is therefore
// invisible to it: a documented diagnostic limit, not a verdict (GH#6289).
func originCheckSkipReason(beadsDir string) string {
	for _, key := range []string{"no-push", "dolt.local-only"} {
		if v, err := strconv.ParseBool(config.GetStringFromDir(beadsDir, key)); err == nil && v {
			return key
		}
	}
	return ""
}

// evaluateSyncStaleness aggregates the Sync Staleness findings (GH#6289).
// The check is offline by design: it reports what the workspace itself
// knows — push-state.json's recorded attempts and SyncStatus's comparison
// against cached tracking refs — and never claims to know the remote's
// current contents. A recorded failure warns even when the cached refs look
// equal, because the recording is positive evidence the last attempt did
// not land; equal cached refs are only the absence of local divergence.
func evaluateSyncStaleness(in syncStalenessInputs) DoctorCheck {
	check := DoctorCheck{Name: "Sync Staleness", Category: CategoryFederation}
	if !in.HasRemotes {
		check.Status = StatusOK
		check.Message = "No peers configured"
		return check
	}

	originChecked := in.OriginConfigured && in.OriginSkipReason == ""
	var findings []string
	originFindings := false

	if originChecked {
		if in.PushStateErr != nil {
			findings = append(findings, "origin: local push history could not be read: "+in.PushStateErr.Error())
			originFindings = true
		} else if in.PushState != nil && in.PushState.LastError != "" {
			// A recorded, unresolved failure. Its timestamps may be equal
			// to last_push's (RFC3339 second precision) or missing
			// entirely; neither may hide the failure.
			attempt := in.PushState.LastAttempt
			if attempt == "" {
				attempt = "time unknown"
			}
			findings = append(findings, fmt.Sprintf(
				"origin: last push attempt at %s failed: %s (the remote may not hold this workspace's recent commits; retry 'bd dolt push' once the cause is resolved)",
				attempt, in.PushState.LastError))
			originFindings = true
		}
		switch {
		case in.OriginStatusErr != nil:
			findings = append(findings, fmt.Sprintf(
				"origin: comparison with cached remote refs unavailable: %v", in.OriginStatusErr))
			originFindings = true
		case in.OriginStatus == nil:
			findings = append(findings, "origin: comparison with cached remote refs unavailable")
			originFindings = true
		case in.OriginStatus.LocalAhead < 0 || in.OriginStatus.LocalBehind < 0:
			findings = append(findings, "origin: comparison with cached remote refs unavailable "+
				"(no cached origin tracking ref yet; push or pull once to create it)")
			originFindings = true
		case in.OriginStatus.LocalAhead > 0 && in.OriginStatus.LocalBehind > 0:
			findings = append(findings, fmt.Sprintf(
				"origin: local history and the cached origin tracking ref have diverged (local is %d commit(s) ahead and %d behind); pull ('bd dolt pull'), resolve, then push ('bd dolt push')",
				in.OriginStatus.LocalAhead, in.OriginStatus.LocalBehind))
			originFindings = true
		case in.OriginStatus.LocalAhead > 0:
			findings = append(findings, fmt.Sprintf(
				"origin: local is %d commit(s) ahead of the cached origin tracking ref; check the remote received them or push again ('bd dolt push')",
				in.OriginStatus.LocalAhead))
			originFindings = true
		case in.OriginStatus.LocalBehind > 0:
			findings = append(findings, fmt.Sprintf(
				"origin: local is %d commit(s) behind the cached origin tracking ref; pull ('bd dolt pull')",
				in.OriginStatus.LocalBehind))
			originFindings = true
		}
	}
	findings = append(findings, in.PeerWarnings...)

	if len(findings) > 0 {
		check.Status = StatusWarning
		var detail string
		switch {
		case originFindings && len(in.PeerWarnings) > 0:
			check.Message = "Origin and peer sync staleness detected"
			check.Fix = "Address the origin findings above, then run 'bd federation sync' to synchronize with peers"
		case originFindings:
			check.Message = "Origin sync staleness detected"
			check.Fix = "Follow the origin guidance above ('bd dolt push' / 'bd dolt pull' as the findings direct)"
		default:
			// Peer-only findings keep the check's established wording.
			check.Message = fmt.Sprintf("%d total commits behind peers", in.TotalPeerBehind)
			check.Fix = "Run 'bd federation sync' to synchronize with peers"
		}
		detail = strings.Join(findings, "\n")
		if originChecked {
			detail += "\nCompared against cached origin refs and recorded push attempts; both are local evidence and can be stale."
		}
		if in.OriginConfigured && !originChecked {
			detail = fmt.Sprintf("origin check skipped (%s: true)\n", in.OriginSkipReason) + detail
		}
		check.Detail = detail
		return check
	}

	check.Status = StatusOK
	switch {
	case originChecked:
		check.Message = "No local sync staleness detected"
		check.Detail = "origin's cached tracking ref matches local history and no failed push attempt is recorded. " +
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
