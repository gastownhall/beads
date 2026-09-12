package main

import (
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/steveyegge/beads/internal/metrics"
)

// TestSendMetricsHonorsMemDiagnostics is the be-wwy2.2 regression: send-metrics's
// Run calls os.Exit() directly, so it returns before Cobra ever reaches
// PersistentPostRunE (main.go) -- the one place --mem-profile / BEADS_MEM_PROFILE /
// BEADS_MEM_PROFILE_NOGC / BEADS_MEM_STATS are honored for every other bd
// subcommand. That makes the diagnostic tooling unreachable for the one child
// process that runs on the tail of nearly every bd invocation fleet-wide.
//
// This builds a real bd binary and exercises `bd send-metrics` as a subprocess
// (like MaybeSpawnFlusher does in production) rather than calling RunSendMetrics
// in-process, because the bug is specifically about the Cobra command-tree
// wiring around this subcommand, which only an actual subprocess run exercises.
func TestSendMetricsHonorsMemDiagnostics(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	// send-metrics returns from PersistentPreRunE before any workspace/store
	// setup (main.go: `if cmd.Name() == metrics.SendMetricsSubcommand { return nil }`),
	// so no .beads workspace is needed here. BD_DISABLE_METRICS=1 makes
	// RunSendMetrics's Enabled() check false, so it returns 0 right after a
	// no-op prune of the (nonexistent) queue dir -- deterministic and offline,
	// with no real network attempt.
	baseEnv := func(home string) []string {
		return []string{
			"HOME=" + home,
			"PATH=" + os.Getenv("PATH"),
			"BD_DISABLE_METRICS=1",
		}
	}

	t.Run("default with no diagnostics env vars is unaffected", func(t *testing.T) {
		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand)
		cmd.Env = baseEnv(t.TempDir())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd send-metrics: %v\noutput: %s", err, out)
		}
		if len(out) != 0 {
			t.Fatalf("bd send-metrics produced output with no diagnostics requested: %q", out)
		}
	})

	t.Run("BEADS_MEM_STATS writes a one-line MemStats summary", func(t *testing.T) {
		statsPath := filepath.Join(t.TempDir(), "stats.txt")
		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand)
		cmd.Env = append(baseEnv(t.TempDir()), "BEADS_MEM_STATS="+statsPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd send-metrics: %v\noutput: %s", err, out)
		}
		data, err := os.ReadFile(statsPath)
		if err != nil {
			t.Fatalf("BEADS_MEM_STATS file was not written: %v", err)
		}
		want := regexp.MustCompile(`^HeapAlloc=\d+ HeapSys=\d+ HeapInuse=\d+ HeapObjects=\d+\n$`)
		if !want.Match(data) {
			t.Fatalf("BEADS_MEM_STATS content = %q, want match of %s", data, want)
		}
	})

	t.Run("BEADS_MEM_PROFILE writes a valid gzipped heap profile", func(t *testing.T) {
		profilePath := filepath.Join(t.TempDir(), "heap.pprof")
		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand)
		cmd.Env = append(baseEnv(t.TempDir()), "BEADS_MEM_PROFILE="+profilePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd send-metrics: %v\noutput: %s", err, out)
		}
		f, err := os.Open(profilePath)
		if err != nil {
			t.Fatalf("BEADS_MEM_PROFILE file was not written: %v", err)
		}
		defer f.Close()
		// runtime/pprof.WriteHeapProfile always gzips its protobuf output;
		// decompressing cleanly is strong evidence a real profile was written
		// without pulling in a pprof-parsing dependency just for this test.
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("BEADS_MEM_PROFILE is not valid gzip: %v", err)
		}
		body, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("BEADS_MEM_PROFILE gzip stream corrupt: %v", err)
		}
		if len(body) == 0 {
			t.Fatalf("BEADS_MEM_PROFILE decompressed to 0 bytes, want a real heap profile")
		}
	})

	t.Run("BEADS_MEM_PROFILE_NOGC is accepted and still writes the profile", func(t *testing.T) {
		profilePath := filepath.Join(t.TempDir(), "heap.pprof")
		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand)
		cmd.Env = append(baseEnv(t.TempDir()), "BEADS_MEM_PROFILE="+profilePath, "BEADS_MEM_PROFILE_NOGC=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd send-metrics: %v\noutput: %s", err, out)
		}
		if _, err := os.Stat(profilePath); err != nil {
			t.Fatalf("BEADS_MEM_PROFILE file was not written under NOGC: %v", err)
		}
	})

	t.Run("diagnostics still run and exit code is preserved when RunSendMetrics fails", func(t *testing.T) {
		statsPath := filepath.Join(t.TempDir(), "stats.txt")
		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand)
		// Deliberately no HOME: DataDir()'s os.UserHomeDir() fails
		// deterministically and offline, so RunSendMetrics() returns 1 before
		// ever reaching the enabled/network checks.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BEADS_MEM_STATS=" + statsPath}
		out, err := cmd.CombinedOutput()
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("bd send-metrics: want a non-zero *exec.ExitError, got %v (%T)\noutput: %s", err, err, out)
		}
		if code := exitErr.ExitCode(); code != 1 {
			t.Fatalf("bd send-metrics exit code = %d, want 1 (RunSendMetrics's own failure code, preserved through the fix)", code)
		}
		if _, statErr := os.Stat(statsPath); statErr != nil {
			t.Fatalf("BEADS_MEM_STATS file was not written even though the command failed: %v", statErr)
		}
	})
}
