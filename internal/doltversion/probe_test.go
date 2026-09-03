package doltversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// skipOnWindows remains only for the tests whose subject genuinely does
// not exist on Windows: the executable-bit check (validateExplicitPath
// deliberately skips it there) and symlink creation (a privileged
// operation on default Windows configurations). Behavioral probe tests
// run on Windows via writeExecStub's .cmd stubs.
func skipOnWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skipf("%s; skipping on windows", why)
	}
}

// resolveSymlinks maps an expected path through filepath.EvalSymlinks, the
// same resolution Probe applies when deriving RealPath. Needed because
// t.TempDir itself can sit behind a symlink — on macOS /var/folders is a
// symlink to /private/var/folders — so comparing RealPath against the raw
// TempDir path fails there even though Probe resolved correctly.
func resolveSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

// TestCapBufferTruncates exercises capBuffer directly (rather than only
// indirectly through TestProbeHugeOutputCapped, which only proves the
// probe still parses a valid first line past a flood of output — it
// doesn't itself assert that the cap was actually enforced). Write must
// still report success for every byte offered past the cap (see
// capBuffer's doc comment: the child process must never see a short
// write), even though the buffer itself stops growing at limit.
func TestCapBufferTruncates(t *testing.T) {
	const limit = 16
	var c capBuffer
	c.limit = limit

	payload := make([]byte, limit*3)
	for i := range payload {
		payload[i] = 'x'
	}

	n, err := c.Write(payload)
	if err != nil {
		t.Fatalf("Write: unexpected error %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write returned n = %d, want %d (full write reported even though truncated internally)", n, len(payload))
	}
	if c.buf.Len() != limit {
		t.Errorf("buf.Len() = %d, want %d (cap not enforced)", c.buf.Len(), limit)
	}

	// A second write past the now-full buffer must still report success
	// for all bytes offered, and must not grow the buffer further.
	n2, err := c.Write([]byte("more"))
	if err != nil {
		t.Fatalf("second Write: unexpected error %v", err)
	}
	if n2 != 4 {
		t.Errorf("second Write returned n = %d, want 4", n2)
	}
	if c.buf.Len() != limit {
		t.Errorf("buf.Len() after second write = %d, want unchanged %d", c.buf.Len(), limit)
	}
}

func TestProbeNormalOutput(t *testing.T) {
	dir := t.TempDir()
	stub := writeVersionEchoStub(t, dir, "dolt", "dolt version 1.52.3")

	id, err := Probe(context.Background(), stub)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if id.GivenPath != stub {
		t.Errorf("GivenPath = %q, want %q", id.GivenPath, stub)
	}
	if id.RealPath != resolveSymlinks(t, stub) {
		t.Errorf("RealPath = %q, want %q", id.RealPath, resolveSymlinks(t, stub))
	}
	if id.Version.String() != "1.52.3" {
		t.Errorf("Version = %v, want 1.52.3", id.Version)
	}
	if id.RawOutput != "dolt version 1.52.3" {
		t.Errorf("RawOutput = %q", id.RawOutput)
	}
	if id.FileSize == 0 {
		t.Error("FileSize = 0, want nonzero")
	}
	if id.FileModTime.IsZero() {
		t.Error("FileModTime is zero")
	}
}

func TestProbeGarbageOutput(t *testing.T) {
	dir := t.TempDir()
	stub := writeVersionEchoStub(t, dir, "dolt", "this is not a version")

	_, err := Probe(context.Background(), stub)
	if err == nil {
		t.Fatal("Probe with garbage output: want error, got nil")
	}
	if !errors.Is(err, ErrUnparseableVersion) {
		t.Errorf("Probe error = %v, want wrapping ErrUnparseableVersion", err)
	}
}

func TestProbeTimeout(t *testing.T) {
	dir := t.TempDir()
	// Sleeps far longer than the test's own deadline below; if Probe's
	// kill-on-timeout didn't work, this test would hang for the full sleep.
	// (The batch variant sleeps via ping's per-attempt one-second cadence,
	// the portable cmd.exe idiom — `timeout` refuses non-interactive use.)
	stub := writeExecStub(t, dir, "dolt",
		"#!/bin/sh\nsleep 30\necho 'dolt version 1.52.3'\n",
		"@ping -n 31 127.0.0.1 > nul\r\n@echo dolt version 1.52.3\r\n")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Probe(ctx, stub)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Probe against sleeping stub: want error, got nil")
	}
	if !errors.Is(err, ErrProbeFailed) {
		t.Errorf("Probe error = %v, want wrapping ErrProbeFailed", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("Probe took %v, want well under the stub's 30s sleep (proves the kill works)", elapsed)
	}
}

func TestProbeHugeOutputCapped(t *testing.T) {
	dir := t.TempDir()
	// First line is a valid version; the script then floods stdout with
	// far more than probeOutputCap bytes to prove the cap holds and
	// parsing of the (already-captured) first line still succeeds.
	posix := "#!/bin/sh\n" +
		"echo 'dolt version 1.52.3'\n" +
		"yes 'garbage filler line to inflate output well past the cap' | head -c 2000000\n"
	batch := "@echo dolt version 1.52.3\r\n" +
		"@for /L %%i in (1,1,3000) do @echo garbage filler line to inflate output well past the cap\r\n"
	stub := writeExecStub(t, dir, "dolt", posix, batch)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id, err := Probe(ctx, stub)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if id.Version.String() != "1.52.3" {
		t.Errorf("Version = %v, want 1.52.3", id.Version)
	}
}

func TestProbeNonExecutableRegularFile(t *testing.T) {
	skipOnWindows(t, "the executable-bit check is deliberately Unix-only")
	dir := t.TempDir()
	stub := writeStub(t, dir, "dolt", "#!/bin/sh\necho 'dolt version 1.52.3'\n", false)

	_, err := Probe(context.Background(), stub)
	if err == nil {
		t.Fatal("Probe against non-executable file: want error, got nil")
	}
	if !errors.Is(err, ErrNotExecutable) {
		t.Errorf("Probe error = %v, want wrapping ErrNotExecutable", err)
	}
}

// TestProbeDirectory documents the chosen taxonomy for "path points at a
// directory, not a file": this package treats it as ErrNotExecutable (a
// directory is not a regular, runnable file) rather than ErrNotFound (the
// path does exist). Either taxonomy is defensible per the design doc; this
// is the one this package implements.
func TestProbeDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "adir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := Probe(context.Background(), sub)
	if err == nil {
		t.Fatal("Probe against a directory: want error, got nil")
	}
	if !errors.Is(err, ErrNotExecutable) {
		t.Errorf("Probe error = %v, want wrapping ErrNotExecutable (documented choice)", err)
	}
}

func TestProbeSymlink(t *testing.T) {
	skipOnWindows(t, "symlink creation is a privileged operation on default Windows configurations")
	dir := t.TempDir()
	real := writeStub(t, dir, "real-dolt", "#!/bin/sh\necho 'dolt version 1.52.3'\n", true)
	link := filepath.Join(dir, "dolt-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	id, err := Probe(context.Background(), link)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if id.GivenPath != link {
		t.Errorf("GivenPath = %q, want %q", id.GivenPath, link)
	}
	if id.RealPath != resolveSymlinks(t, real) {
		t.Errorf("RealPath = %q, want %q (symlink target)", id.RealPath, resolveSymlinks(t, real))
	}
	if id.GivenPath == id.RealPath {
		t.Error("GivenPath and RealPath should differ for a symlink")
	}
}

func TestProbeMissingPath(t *testing.T) {
	dir := t.TempDir()
	_, err := Probe(context.Background(), filepath.Join(dir, "does-not-exist"))
	if err == nil {
		t.Fatal("Probe against missing path: want error, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Probe error = %v, want wrapping ErrNotFound", err)
	}
}

func TestProbeNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	stub := writeExecStub(t, dir, "dolt",
		"#!/bin/sh\necho 'boom' >&2\nexit 1\n",
		"@echo boom 1>&2\r\n@exit /b 1\r\n")

	_, err := Probe(context.Background(), stub)
	if err == nil {
		t.Fatal("Probe against failing stub: want error, got nil")
	}
	if !errors.Is(err, ErrProbeFailed) {
		t.Errorf("Probe error = %v, want wrapping ErrProbeFailed", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("Probe error = %v, want it to include stderr diagnostic", err)
	}
}
