package doltversion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// warnRepeatInterval is how long a shown advisory suppresses identical
// advisories from later processes. One day keeps the warning present enough
// that an operator on an old dolt is reminded it is still old, without
// repeating on every single bd invocation, which review found trains
// operators to ignore it entirely.
const warnRepeatInterval = 24 * time.Hour

// probeCacheFile is the on-disk shape of the probe cache. One entry only:
// a machine has one resolved dolt binary at a time in practice, and the
// fingerprint check below makes a stale entry for a different binary
// harmless (it just misses).
type probeCacheFile struct {
	// RealPath/FileSize/FileModTimeUnixNano fingerprint the binary the
	// cached result was probed from — exactly the Identity fields whose
	// doc comment promises callers can cache on them.
	RealPath            string `json:"real_path"`
	FileSize            int64  `json:"file_size"`
	FileModTimeUnixNano int64  `json:"file_mod_time_unixnano"`
	// RawOutput and Version replay the probe result on a fingerprint hit.
	// Version is the canonical string form; empty when Unverifiable.
	RawOutput    string `json:"raw_output"`
	Version      string `json:"version,omitempty"`
	Unverifiable bool   `json:"unverifiable,omitempty"`
	// LastWarnedUnix is when a caller last showed the version advisory
	// (via MarkWarned), in Unix seconds. Zero means never shown.
	LastWarnedUnix int64 `json:"last_warned_unix,omitempty"`
}

// CachedProbeResult is what ProbeWithPolicyCached returns alongside its
// error: the probe outcome plus the cache/warning bookkeeping a CLI call
// site needs to decide whether to print the advisory this time.
type CachedProbeResult struct {
	Identity Identity
	Warning  *Warning
	// FromCache is true when the result was replayed from the cache file
	// without forking the binary.
	FromCache bool
	// WarnDue is true when Warning is non-nil and the advisory has not
	// been shown (per the cache file's stamp) within warnRepeatInterval.
	// With no usable cache file it is always true — fail open toward
	// showing the warning, never toward hiding it.
	WarnDue bool
}

// ProbeWithPolicyCached is ProbeWithPolicy with a cross-process cache in
// front of the fork. Every bd command in managed proxied-server mode runs
// this on its hot path, and each command is a fresh process, so an
// in-process memo cannot help — the cache lives in a small JSON file at
// cachePath, keyed by the binary's (real path, size, mtime) fingerprint.
// A fingerprint hit replays the previous result for the cost of a stat;
// any change to the binary on disk misses and re-probes.
//
// The cache is strictly best-effort and fails open in every direction: an
// empty cachePath, an unreadable/corrupt cache file, or a failed write all
// degrade to exactly the uncached ProbeWithPolicy behavior. Hard probe
// errors are never cached — a broken binary is re-probed (and re-fails,
// loudly) on every command.
func ProbeWithPolicyCached(ctx context.Context, path, cachePath string, now time.Time) (CachedProbeResult, error) {
	if cachePath == "" {
		id, warn, err := ProbeWithPolicy(ctx, path)
		return CachedProbeResult{Identity: id, Warning: warn, WarnDue: warn != nil}, err
	}

	if cached, ok := loadCacheEntry(cachePath); ok {
		if id, ok := fingerprintMatches(path, cached); ok {
			res := CachedProbeResult{Identity: id, FromCache: true}
			if cached.Unverifiable {
				res.Identity.RawOutput = cached.RawOutput
				res.Warning = &Warning{kind: warningUnverifiable, RawOutput: cached.RawOutput}
			} else {
				ver, err := ParseVersion(cached.RawOutput)
				if err != nil {
					// Cache content no longer parses (e.g. written by a
					// different beads version) — treat as a miss.
					return probeAndStore(ctx, path, cachePath, cached.LastWarnedUnix, now)
				}
				res.Identity.Version = ver
				res.Identity.RawOutput = cached.RawOutput
				res.Warning = CheckRecommended(res.Identity)
			}
			res.WarnDue = res.Warning != nil && warnDue(cached.LastWarnedUnix, now)
			return res, nil
		}
	}
	return probeAndStore(ctx, path, cachePath, 0, now)
}

// MarkWarned records in the cache file that the version advisory was shown
// at now, so later processes suppress it for warnRepeatInterval. Best
// effort: on any error the stamp is simply not recorded and the next
// process warns again, which is the safe direction.
func MarkWarned(cachePath string, now time.Time) {
	if cachePath == "" {
		return
	}
	entry, ok := loadCacheEntry(cachePath)
	if !ok {
		return
	}
	entry.LastWarnedUnix = now.Unix()
	writeCacheEntry(cachePath, entry)
}

// probeAndStore runs the real fork-and-parse probe and, on a cacheable
// outcome (parsed version or unverifiable-but-ran), persists it.
// lastWarned carries an existing warn stamp forward across a re-probe of
// what fingerprints as a changed binary at the same path — zero when there
// is nothing to carry.
func probeAndStore(ctx context.Context, path, cachePath string, lastWarned int64, now time.Time) (CachedProbeResult, error) {
	id, warn, err := ProbeWithPolicy(ctx, path)
	res := CachedProbeResult{Identity: id, Warning: warn}
	if err != nil {
		return res, err
	}
	entry := probeCacheFile{
		RealPath:            id.RealPath,
		FileSize:            id.FileSize,
		FileModTimeUnixNano: id.FileModTime.UnixNano(),
		RawOutput:           id.RawOutput,
		LastWarnedUnix:      lastWarned,
	}
	if warn != nil && warn.kind == warningUnverifiable {
		entry.Unverifiable = true
	} else {
		entry.Version = id.Version.String()
	}
	writeCacheEntry(cachePath, entry)
	res.WarnDue = warn != nil && warnDue(lastWarned, now)
	return res, nil
}

// fingerprintMatches stats path (following symlinks, the same resolution
// Probe applies) and reports whether it is byte-for-byte plausibly the
// binary entry was probed from. It also returns the fresh Identity file
// fields for the caller to build its result from — GivenPath is the path
// as asked for now, not as cached, so a symlink flip is reflected.
func fingerprintMatches(path string, entry probeCacheFile) (Identity, bool) {
	if err := validateExplicitPath(path); err != nil {
		return Identity{}, false
	}
	realPath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		realPath = resolved
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return Identity{}, false
	}
	id := Identity{
		GivenPath:   path,
		RealPath:    realPath,
		FileSize:    info.Size(),
		FileModTime: info.ModTime(),
	}
	ok := realPath == entry.RealPath &&
		info.Size() == entry.FileSize &&
		info.ModTime().UnixNano() == entry.FileModTimeUnixNano
	return id, ok
}

func warnDue(lastWarnedUnix int64, now time.Time) bool {
	if lastWarnedUnix <= 0 {
		return true
	}
	return now.Sub(time.Unix(lastWarnedUnix, 0)) >= warnRepeatInterval
}

func loadCacheEntry(cachePath string) (probeCacheFile, bool) {
	data, err := os.ReadFile(cachePath) //nolint:gosec // G304: cachePath is caller-controlled cache-file location (user cache dir), not request input

	if err != nil {
		return probeCacheFile{}, false
	}
	var entry probeCacheFile
	if err := json.Unmarshal(data, &entry); err != nil {
		return probeCacheFile{}, false
	}
	return entry, true
}

// writeCacheEntry persists entry atomically (temp file + rename) so a
// concurrent reader never sees a torn write; two concurrent writers
// last-write-wins, which is fine for a cache. Best effort throughout.
func writeCacheEntry(cachePath string, entry probeCacheFile) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(cachePath)+".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, cachePath); err != nil {
		_ = os.Remove(tmpName)
	}
}
