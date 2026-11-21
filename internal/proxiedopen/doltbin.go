package proxiedopen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
)

// doltVersionWarnOnce ensures the dolt-version advisory (unverifiable
// version) is printed at most once per process, even though
// ResolveAndProbeDolt can run repeatedly against the same resolved binary
// (retries, multiple UOW providers opened in one command, or bd init
// --proxied-server's own preflight followed by the UOW provider it opens a
// few lines later). Repeating the same advisory on every call would just be
// noise once the operator has seen it. Cross-process dedup (each bd
// invocation is a new process) is handled separately by the probe cache's
// warn stamp — see doltversion.ProbeWithPolicyCached / MarkWarned.
var doltVersionWarnOnce sync.Once

// doltProbeCachePath is where the cross-process dolt probe cache lives: the
// binary being probed is machine-scoped, so the cache is too (user cache
// dir, not the workspace). Empty on any error, which makes the cached probe
// fall back to uncached behavior — never a reason to fail a command.
func doltProbeCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "beads", "dolt-probe.json")
}

// ResolveAndProbeDolt resolves the external dolt binary (env override >
// sidecar > PATH) and hardened-probes it. A binary below
// doltversion.RequiredMin is refused with the probe's *BelowMinimumError,
// which names the version, the minimum and how to upgrade; one whose version
// does not parse prints an advisory to advisory at most once per process via
// doltVersionWarnOnce and is used anyway.
//
// A nil advisory suppresses the advisory (errors still return). It is a
// parameter rather than a read of any process-wide flag because the callers
// decide quiet differently: the bd CLI gates it on --quiet and --json (and
// bd init defines its own local --quiet that shadows the persistent one),
// while a library embedder of the Go SDK prints nothing at all.
//
// bd init --proxied-server's own preflight and the managed proxied-server
// open both need exactly this resolve+probe+warn sequence. A single
// non-quiet `bd init --proxied-server` invocation runs the preflight and then
// opens a UOW provider a few lines later, so before this was shared, an
// old/unverifiable dolt printed the identical advisory twice and forked
// `dolt version` twice. errPrefix is used to keep call-site-specific error
// context (e.g. "bd init --proxied-server" vs
// "newManagedProxiedServerUOWProvider") in the wrapped error. The probed
// identity is deliberately not returned: its only consumer (the
// auto_gc_behavior.archive_level gate) was removed upstream from
// proxied-server mode, and PR-2's revalidation consumer will re-widen the
// signature when it lands.
func ResolveAndProbeDolt(ctx context.Context, errPrefix string, advisory io.Writer) (doltBin string, err error) {
	doltBin, doltSrc, err := doltversion.Resolve(doltversion.ResolveOptions{
		EnvValue: doltversion.ReadEnvOverride(),
		// SidecarValue is a hook point only in this PR — the clone-local
		// sidecar setting itself lands in PR-2.
		SidecarValue: "",
	})
	if err != nil {
		return "", fmt.Errorf(
			"%s: resolving dolt binary (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			errPrefix, doltSrc, err,
		)
	}
	// Cached: a fingerprint hit (same real path, size, mtime as the last
	// successful probe) replays the result for the cost of a stat instead of
	// forking `dolt version` on every command — this call sits on the
	// store-open hot path of essentially every bd command in managed
	// proxied-server mode.
	cachePath := doltProbeCachePath()
	res, err := doltversion.ProbeWithPolicyCached(ctx, doltBin, cachePath, time.Now())
	if err != nil {
		if errors.Is(err, doltversion.ErrBelowRequiredMin) {
			// The binary works; it is too old. The error carries its own
			// upgrade advice, so the generic install hint would only
			// repeat it less precisely.
			return doltBin, fmt.Errorf("%s: %w (source: %s)", errPrefix, err, doltSrc)
		}
		if errors.Is(err, context.Canceled) {
			// The probe was killed because OUR context was canceled (user
			// hit Ctrl-C, outer operation gave up) — dolt itself was never
			// shown to be broken, so the install hint would mislead.
			return doltBin, fmt.Errorf(
				"%s: probing dolt binary %q (source: %s): %w",
				errPrefix, doltBin, doltSrc, err,
			)
		}
		return doltBin, fmt.Errorf(
			"%s: probing dolt binary %q (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			errPrefix, doltBin, doltSrc, err,
		)
	}
	// WarnDue adds the cross-process gate: the advisory repeats at most once
	// per day (per the probe cache's stamp), not on every invocation. The
	// stamp is only written when the advisory was actually printed.
	if res.Warning != nil && advisory != nil && res.WarnDue {
		doltVersionWarnOnce.Do(func() {
			fmt.Fprintf(advisory, "Warning: %s\n", res.Warning.Message())
			doltversion.MarkWarned(cachePath, time.Now())
		})
	}
	return doltBin, nil
}
