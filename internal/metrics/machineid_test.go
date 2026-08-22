package metrics

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCachedMachineIDReusesCacheWithoutRecomputing seeds the on-disk cache
// with a sentinel and asserts cachedMachineID returns it verbatim — proving
// the cached path never reaches the (slow, forking) platform probe, which
// could not produce the sentinel.
func TestCachedMachineIDReusesCacheWithoutRecomputing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	sentinel := strings.Repeat("ab12", 16) // 64 chars, valid shape
	dir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, machineIDCacheName), []byte(sentinel+"\n"), 0o600); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	if got := cachedMachineID(AppName); got != sentinel {
		t.Errorf("cachedMachineID = %q, want cached sentinel %q", got, sentinel)
	}
}

// TestCachedMachineIDComputesAndPersistsOnMiss exercises the cold path: no
// cache file, so the ID is computed once and written to ~/.beads/machine-id
// (0600) for every later invocation to reuse.
func TestCachedMachineIDComputesAndPersistsOnMiss(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	first := cachedMachineID(AppName)
	if first == "" {
		t.Fatalf("cachedMachineID returned empty ID")
	}

	path := filepath.Join(home, ".beads", machineIDCacheName)
	if !validMachineID(first) {
		// The platform probe failed (returns "invalid" in sandboxed CI);
		// a failure must NOT be cached, so the next run retries.
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("invalid probe result was cached at %s (stat err=%v)", path, err)
		}
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != first {
		t.Errorf("cache content = %q, want %q", got, first)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat cache: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("cache perms = %o, want 0600", perm)
		}
	}

	// Second call returns the identical ID (now from cache).
	if second := cachedMachineID(AppName); second != first {
		t.Errorf("second cachedMachineID = %q, want %q", second, first)
	}
}

// TestReadCachedMachineIDRejectsGarbage: a corrupt, oversized, multi-token, or
// probe-failure ("invalid") cache must read as a miss so the ID is recomputed,
// never fed into every event's distinct_id.
func TestReadCachedMachineIDRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "whitespace-only", content: " \n\t\n"},
		{name: "probe-failure-marker", content: "invalid\n"},
		{name: "embedded-space", content: "abc def\n"},
		{name: "multi-line", content: "abc123\nxyz789\n"},
		{name: "control-chars", content: "abc\x01def\n"},
		{name: "non-ascii", content: "abcédef\n"},
		{name: "oversized", content: strings.Repeat("a", maxMachineIDLen+1) + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "cache-"+tc.name)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := readCachedMachineID(path); got != "" {
				t.Errorf("readCachedMachineID(%q content=%q) = %q, want miss", tc.name, tc.content, got)
			}
		})
	}

	// Positive control: a well-formed single-token cache is accepted, with
	// surrounding whitespace trimmed.
	path := filepath.Join(dir, "cache-good")
	if err := os.WriteFile(path, []byte("  deadbeef42\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readCachedMachineID(path); got != "deadbeef42" {
		t.Errorf("readCachedMachineID(good) = %q, want %q", got, "deadbeef42")
	}
}

// TestInitDisabledDoesNotTouchMachineID: a disabled invocation (the
// BD_DISABLE_METRICS / DO_NOT_TRACK path — and every `bd --version`) must not
// compute, read, or write a machine ID at all. The observable half of that
// contract is that no cache file appears.
func TestInitDisabledDoesNotTouchMachineID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	path := filepath.Join(home, ".beads", machineIDCacheName)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("disabled Init created machine-id cache at %s (stat err=%v)", path, err)
	}
}

// TestWriteMachineIDCacheAtomicReplace: writing over an existing cache goes
// through temp-file + rename, so no reader can observe a truncated ID and no
// tmp litter survives.
func TestWriteMachineIDCacheAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, machineIDCacheName)
	if err := os.WriteFile(path, []byte("oldvalue\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	writeMachineIDCache(path, "newvalue")

	if got := readCachedMachineID(path); got != "newvalue" {
		t.Errorf("after replace, cache = %q, want %q", got, "newvalue")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file litter left behind: %s", e.Name())
		}
	}
}
