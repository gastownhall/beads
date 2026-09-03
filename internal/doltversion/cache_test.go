package doltversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCountingVersionStub writes a stub that prints a version line AND
// appends one byte to countFile per invocation, so tests can prove whether
// the binary was actually forked or the result came from the cache.
//
// The batch variant leads with the redirection: cmd.exe includes any space
// before a mid-line `>>` in the text being written (`set /p =x >> f`
// appends "x ", two bytes), which silently doubled forkCount on Windows.
func writeCountingVersionStub(t *testing.T, dir, name, line, countFile string) string {
	t.Helper()
	return writeExecStub(t, dir, name,
		"#!/bin/sh\nprintf x >> '"+countFile+"'\necho '"+line+"'\n",
		"@>>\""+countFile+"\" <nul set /p =x\r\n@echo "+line+"\r\n")
}

func forkCount(t *testing.T, countFile string) int {
	t.Helper()
	data, err := os.ReadFile(countFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read count file: %v", err)
	}
	return len(data)
}

func TestCachedProbeHitSkipsFork(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	stub := writeCountingVersionStub(t, dir, "dolt", "dolt version 2.1.0", countFile)
	cachePath := filepath.Join(dir, "cache", "dolt-probe.json")
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	first, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, now)
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	if first.FromCache {
		t.Error("first probe: FromCache = true, want false")
	}
	if got := forkCount(t, countFile); got != 1 {
		t.Fatalf("after first probe: fork count = %d, want 1", got)
	}

	second, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, now)
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if !second.FromCache {
		t.Error("second probe: FromCache = false, want true (fingerprint unchanged)")
	}
	if got := forkCount(t, countFile); got != 1 {
		t.Errorf("after second probe: fork count = %d, want still 1 (cache hit must not fork)", got)
	}
	if second.Identity.Version.String() != "2.1.0" {
		t.Errorf("cached Version = %v, want 2.1.0", second.Identity.Version)
	}
	if second.Warning != nil {
		t.Errorf("cached Warning = %v, want nil for a recent version", second.Warning)
	}
}

func TestCachedProbeMissOnBinaryChange(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	stub := writeCountingVersionStub(t, dir, "dolt", "dolt version 2.1.0", countFile)
	cachePath := filepath.Join(dir, "dolt-probe.json")
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	if _, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, now); err != nil {
		t.Fatalf("first probe: %v", err)
	}

	// Replace the stub with different content (size changes) — the
	// fingerprint must miss and the new binary must be re-probed.
	stub2 := writeCountingVersionStub(t, dir, "dolt", "dolt version 2.2.0-different", countFile)
	if stub2 != stub {
		t.Fatalf("rewrite landed at %q, want same path %q", stub2, stub)
	}

	res, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, now)
	if err != nil {
		t.Fatalf("probe after binary change: %v", err)
	}
	if res.FromCache {
		t.Error("probe after binary change: FromCache = true, want false")
	}
	if got := forkCount(t, countFile); got != 2 {
		t.Errorf("fork count = %d, want 2 (changed binary must be re-probed)", got)
	}
	if res.Identity.Version.String() != "2.2.0-different" {
		t.Errorf("Version = %v, want 2.2.0-different (from the NEW binary)", res.Identity.Version)
	}
}

func TestCachedProbeCorruptCacheFailsOpen(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	stub := writeCountingVersionStub(t, dir, "dolt", "dolt version 2.1.0", countFile)
	cachePath := filepath.Join(dir, "dolt-probe.json")
	if err := os.WriteFile(cachePath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}

	res, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, time.Now())
	if err != nil {
		t.Fatalf("probe with corrupt cache: %v", err)
	}
	if res.FromCache {
		t.Error("FromCache = true, want false (corrupt cache must be ignored)")
	}
	if res.Identity.Version.String() != "2.1.0" {
		t.Errorf("Version = %v, want 2.1.0", res.Identity.Version)
	}
	// The successful probe must have repaired the cache file.
	if _, ok := loadCacheEntry(cachePath); !ok {
		t.Error("cache file not rewritten after probe over corrupt cache")
	}
}

func TestCachedProbeHardErrorNotCached(t *testing.T) {
	dir := t.TempDir()
	stub := writeExecStub(t, dir, "dolt-fails", "#!/bin/sh\nexit 1\n", "@exit /b 1\r\n")
	cachePath := filepath.Join(dir, "dolt-probe.json")

	for i := 0; i < 2; i++ {
		_, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, time.Now())
		if err == nil {
			t.Fatalf("call %d: want error from failing binary, got nil", i+1)
		}
		if !errors.Is(err, ErrProbeFailed) {
			t.Errorf("call %d: error = %v, want wrapping ErrProbeFailed", i+1, err)
		}
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Errorf("cache file exists after hard probe errors; hard errors must not be cached (stat err: %v)", err)
	}
}

func TestCachedProbeUnverifiableIsCachedWithWarning(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	stub := writeCountingVersionStub(t, dir, "dolt", "definitely not a version", countFile)
	cachePath := filepath.Join(dir, "dolt-probe.json")
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	first, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, now)
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	if first.Warning == nil {
		t.Fatal("first probe: Warning = nil, want unverifiable warning")
	}

	second, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, now)
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if !second.FromCache {
		t.Error("second probe: FromCache = false, want true")
	}
	if second.Warning == nil {
		t.Fatal("second probe: Warning = nil, want unverifiable warning replayed from cache")
	}
	if second.Warning.RawOutput != "definitely not a version" {
		t.Errorf("cached Warning.RawOutput = %q", second.Warning.RawOutput)
	}
	if got := forkCount(t, countFile); got != 1 {
		t.Errorf("fork count = %d, want 1", got)
	}
}

func TestCachedProbeWarnStampDedupes(t *testing.T) {
	dir := t.TempDir()
	stub := writeVersionEchoStub(t, dir, "dolt-old", "dolt version 1.52.1")
	cachePath := filepath.Join(dir, "dolt-probe.json")
	base := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	first, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, base)
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	if first.Warning == nil || !first.WarnDue {
		t.Fatalf("first probe: Warning=%v WarnDue=%v, want warning due (never warned)", first.Warning, first.WarnDue)
	}
	MarkWarned(cachePath, base)

	soon, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if soon.Warning == nil {
		t.Fatal("second probe: Warning = nil, want warning still present")
	}
	if soon.WarnDue {
		t.Error("second probe 1h later: WarnDue = true, want false (stamped within interval)")
	}

	later, err := ProbeWithPolicyCached(context.Background(), stub, cachePath, base.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("third probe: %v", err)
	}
	if !later.WarnDue {
		t.Error("third probe 25h later: WarnDue = false, want true (interval elapsed)")
	}
}

func TestCachedProbeEmptyCachePathUncached(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	stub := writeCountingVersionStub(t, dir, "dolt-old", "dolt version 1.52.1", countFile)

	for i := 0; i < 2; i++ {
		res, err := ProbeWithPolicyCached(context.Background(), stub, "", time.Now())
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if res.FromCache {
			t.Errorf("call %d: FromCache = true, want false with empty cachePath", i+1)
		}
		if res.Warning == nil || !res.WarnDue {
			t.Errorf("call %d: Warning=%v WarnDue=%v, want warning always due without a cache", i+1, res.Warning, res.WarnDue)
		}
	}
	if got := forkCount(t, countFile); got != 2 {
		t.Errorf("fork count = %d, want 2 (no cache, every call forks)", got)
	}
	// MarkWarned with empty path must be a no-op, not a panic.
	MarkWarned("", time.Now())
}
