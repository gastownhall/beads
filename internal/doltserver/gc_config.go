package doltserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
)

// MinDoltVersionForArchiveLevelConfig is the earliest Dolt release known to
// accept the auto_gc_behavior.archive_level key in its YAML sql-server
// config (servercfg.AutoGCBehaviorYAMLConfig.ArchiveLevel_ carries
// minver:"1.52.1" in the pinned dolthub/dolt/go module).
//
// This matters because Dolt's YAML config loader parses with
// yaml.UnmarshalStrict (see servercfg/yaml_config.go), so an older external
// dolt binary whose own compiled-in YAMLConfig struct predates this field
// will fail to parse a config file that sets it — the unknown key is a hard
// parse error, not a silently-ignored one, and `dolt sql-server` refuses to
// start. See gastownhall/beads#4986.
const MinDoltVersionForArchiveLevelConfig = "1.52.1"

// archiveLevelSupportCache memoizes the `dolt version` output that
// SupportsArchiveLevelConfig and RequireDoltBinaryVersion both judge,
// keyed by archiveLevelCacheKey (path + size + mtime, not path alone) for
// the lifetime of the process. `dolt version` does not change between two
// probes of the SAME on-disk binary, and Start()/the UOW provider can be
// invoked repeatedly against the same resolved binary (retries, multiple
// projects sharing one dolt, tests), so this avoids forking a
// `dolt version` subprocess on every call — including a second fork for the
// version floor right after the archive-level probe. Keying by file
// identity (not just the path string) lets an in-place dolt
// upgrade/downgrade at that same path invalidate the cache naturally on its
// next probe, rather than keeping a stale verdict for the rest of the
// process's lifetime.
var archiveLevelSupportCache sync.Map // map[archiveLevelCacheKey]doltVersionOutput

// doltVersionOutput is one memoized `dolt version` run: its stdout, and
// whether it ran successfully.
type doltVersionOutput struct {
	out string
	ok  bool
}

// archiveLevelCacheKey identifies a specific on-disk binary, not just a
// path: two different binaries can share a path over time (a package
// manager upgrade replaces the file at the same location), and size+mtime
// is enough to distinguish them without hashing the whole binary.
type archiveLevelCacheKey struct {
	path    string
	size    int64
	modTime int64 // info.ModTime().UnixNano()
}

// SupportsArchiveLevelConfig probes doltBin (an absolute path or a PATH
// lookup result) for a version new enough to safely accept
// auto_gc_behavior.archive_level in a YAML sql-server config. It fails
// closed: any error running or parsing `dolt version` returns false, so
// callers fall back to config generation that omits the key rather than
// risk a refuse-to-start on an older external dolt.
//
// Results are memoized per archiveLevelCacheKey (see doc comment) for the
// process lifetime. If os.Stat(doltBin) itself fails, the probe runs
// uncached — there is no stable identity to key on, and the exec below
// will fail closed anyway. Use ResetArchiveLevelSupportCacheForTest to
// force-clear the cache in a test that cannot otherwise change a stub's
// file identity between subtests.
func SupportsArchiveLevelConfig(doltBin string) bool {
	out, ok := probeDoltVersionOutput(doltBin)
	return ok && doltVersionAtLeast(out, MinDoltVersionForArchiveLevelConfig)
}

// RequireDoltBinaryVersion refuses doltBin when its `dolt version` reports a
// version below doltversion.RequiredMin, so Start does not launch a
// sql-server bd would then refuse to use. The error names the binary, its
// version, the minimum and how to upgrade.
//
// A version it cannot determine (`dolt version` fails or prints something
// that is not a version) is not refused, matching doltversion's policy: the
// floor acts on a dolt shown to be too old. Start launches such a binary as
// before, and the server-mode open checks the running server's
// dolt_version() itself.
func RequireDoltBinaryVersion(doltBin string) error {
	out, ok := probeDoltVersionOutput(doltBin)
	if !ok {
		return nil
	}
	v, err := doltversion.ParseVersion(out)
	if err != nil {
		return nil
	}
	return doltversion.CheckRequired(v, fmt.Sprintf("dolt binary %q", doltBin), false)
}

// doltVersionProbeTimeout bounds probeDoltVersionOutput's `dolt version`,
// as doltversion.Probe bounds its own: the probe gates Start and every CLI
// clone and transfer, so a wedged binary or wrapper script must fail it
// (and so go unrefused, the command itself then reporting the binary)
// rather than hang the command past BEADS_CLI_TRANSFER_TIMEOUT, which only
// starts after it. doltVersionProbeWaitDelay is the cleanup slop after the
// kill. The timeout is a variable only so a test can shorten it.
var doltVersionProbeTimeout = 10 * time.Second

const doltVersionProbeWaitDelay = 2 * time.Second

// probeDoltVersionOutput runs `doltBin version`, memoized per
// archiveLevelCacheKey (see archiveLevelSupportCache), within
// doltVersionProbeTimeout. If os.Stat(doltBin) fails the probe runs
// uncached: there is no stable identity to key on. A timed-out probe is
// cached as failed, like any other failed probe.
func probeDoltVersionOutput(doltBin string) (string, bool) {
	key, hasKey := archiveLevelCacheKeyFor(doltBin)
	if hasKey {
		if cached, found := archiveLevelSupportCache.Load(key); found {
			c := cached.(doltVersionOutput)
			return c.out, c.ok
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), doltVersionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, doltBin, "version") //nolint:gosec // G204: doltBin is caller-resolved (PATH lookup or config), not user-request input
	// WaitDelay keeps a grandchild holding the stdout pipe from blocking
	// Output past the timeout after the kill.
	cmd.WaitDelay = doltVersionProbeWaitDelay
	out, err := cmd.Output()
	res := doltVersionOutput{out: string(out), ok: err == nil}

	if hasKey {
		archiveLevelSupportCache.Store(key, res)
	}
	return res.out, res.ok
}

// archiveLevelCacheKeyFor stats doltBin and returns its cache key. The
// second return is false when doltBin cannot be stat'd (e.g. it does not
// exist), signaling the caller should not attempt to cache the result.
func archiveLevelCacheKeyFor(doltBin string) (archiveLevelCacheKey, bool) {
	info, err := os.Stat(doltBin)
	if err != nil {
		return archiveLevelCacheKey{}, false
	}
	return archiveLevelCacheKey{
		path:    doltBin,
		size:    info.Size(),
		modTime: info.ModTime().UnixNano(),
	}, true
}

// ResetArchiveLevelSupportCacheForTest clears the memoization cache. Tests
// that probe SupportsArchiveLevelConfig against a stub binary path that
// gets rewritten between subtests (or reused across independent test
// binaries in the same package) must call this to avoid a stale result
// from a different test.
func ResetArchiveLevelSupportCacheForTest() {
	archiveLevelSupportCache.Range(func(key, _ any) bool {
		archiveLevelSupportCache.Delete(key)
		return true
	})
}

// doltVersionAtLeast parses the first line of `dolt version` output (e.g.
// "dolt version 1.52.3\n", possibly followed by extra lines such as
// "database storage format: ..." when run inside a Dolt repo) and reports
// whether the trailing version token is >= minVer, using numeric
// dotted-segment comparison. Returns false (fail closed) if the version
// cannot be parsed as a dotted sequence of non-negative integers.
func doltVersionAtLeast(versionOutput, minVer string) bool {
	firstLine := versionOutput
	if idx := strings.IndexByte(versionOutput, '\n'); idx >= 0 {
		firstLine = versionOutput[:idx]
	}
	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return false
	}

	got := strings.Split(fields[len(fields)-1], ".")
	want := strings.Split(minVer, ".")

	for i := range want {
		var g, w int
		var err error
		if i < len(got) {
			if g, err = strconv.Atoi(got[i]); err != nil {
				return false
			}
		}
		if w, err = strconv.Atoi(want[i]); err != nil {
			return false
		}
		if g != w {
			return g > w
		}
	}
	return true
}
