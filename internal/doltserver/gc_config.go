package doltserver

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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

// archiveLevelSupportCache memoizes SupportsArchiveLevelConfig, keyed by
// archiveLevelCacheKey (path + size + mtime, not path alone) for the
// lifetime of the process. `dolt version` does not change between two
// probes of the SAME on-disk binary, and Start()/the UOW provider can be
// invoked repeatedly against the same resolved binary (retries, multiple
// projects sharing one dolt, tests), so this avoids forking a
// `dolt version` subprocess on every call. Keying by file identity (not
// just the path string) lets an in-place dolt upgrade/downgrade at that
// same path invalidate the cache naturally on its next probe, rather than
// keeping a stale verdict for the rest of the process's lifetime.
var archiveLevelSupportCache sync.Map // map[archiveLevelCacheKey]bool

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
	key, hasKey := archiveLevelCacheKeyFor(doltBin)
	if hasKey {
		if cached, ok := archiveLevelSupportCache.Load(key); ok {
			return cached.(bool)
		}
	}

	out, err := exec.Command(doltBin, "version").Output() //nolint:gosec // G204: doltBin is caller-resolved (PATH lookup or config), not user-request input
	supported := err == nil && doltVersionAtLeast(string(out), MinDoltVersionForArchiveLevelConfig)

	if hasKey {
		archiveLevelSupportCache.Store(key, supported)
	}
	return supported
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
