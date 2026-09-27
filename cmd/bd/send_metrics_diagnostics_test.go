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

// memStatsLine is the exact shape writeMemDiagnostics' MemStats summary writes.
var memStatsLine = regexp.MustCompile(`^HeapAlloc=\d+ HeapSys=\d+ HeapInuse=\d+ HeapObjects=\d+\n$`)

// requireHeapProfile fails unless path holds a real heap profile.
// runtime/pprof.WriteHeapProfile always gzips its protobuf output, so
// decompressing cleanly to a non-empty body is strong evidence a genuine profile
// was written -- without pulling in a pprof-parsing dependency just for this
// test. knob names the surface under test so a failure says which one broke.
func requireHeapProfile(t *testing.T, knob, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%s file was not written: %v", knob, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s is not valid gzip: %v", knob, err)
	}
	body, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("%s gzip stream corrupt: %v", knob, err)
	}
	if len(body) == 0 {
		t.Fatalf("%s decompressed to 0 bytes, want a real heap profile", knob)
	}
}

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
		if !memStatsLine.Match(data) {
			t.Fatalf("BEADS_MEM_STATS content = %q, want match of %s", data, memStatsLine)
		}
	})

	t.Run("BEADS_MEM_PROFILE writes a valid gzipped heap profile", func(t *testing.T) {
		profilePath := filepath.Join(t.TempDir(), "heap.pprof")
		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand)
		cmd.Env = append(baseEnv(t.TempDir()), "BEADS_MEM_PROFILE="+profilePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd send-metrics: %v\noutput: %s", err, out)
		}
		requireHeapProfile(t, "BEADS_MEM_PROFILE", profilePath)
	})

	// --mem-profile is registered on rootCmd.PersistentFlags() (main.go), so this
	// hidden subcommand inherits it and advertises it in --help. The call site
	// used to hardcode "", which parsed the flag and then discarded it -- the same
	// "accepted but silently inert" state be-wwy2.2 exists to remove, one line
	// below the fix. No BEADS_MEM_PROFILE here, so only the flag can produce the
	// file.
	t.Run("--mem-profile flag is honored, not just the env var", func(t *testing.T) {
		profilePath := filepath.Join(t.TempDir(), "flag.pprof")
		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand, "--mem-profile="+profilePath)
		cmd.Env = baseEnv(t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd send-metrics --mem-profile: %v\noutput: %s", err, out)
		}
		requireHeapProfile(t, "--mem-profile", profilePath)
	})

	// The detached flusher child inherits the parent's BEADS_MEM_* paths verbatim
	// (flusherChildEnv strips only the endpoint and the flusher marker), and
	// MaybeSpawnFlusher runs on main()'s post-ExecuteC tail -- after
	// PersistentPostRunE already wrote them. Without a distinct destination the
	// child's trivial profile would silently replace the profile of the command
	// the user actually asked about. BD_IS_FLUSHER=1 is what production sets on
	// the child, so driving it here is the real shape, not a contrivance.
	t.Run("flusher child writes beside the parent's files instead of over them", func(t *testing.T) {
		dir := t.TempDir()
		profilePath := filepath.Join(dir, "heap.pprof")
		statsPath := filepath.Join(dir, "stats.txt")

		// Stand in for the parent command's own writeMemDiagnostics call, which
		// has already happened by the time the flusher is spawned.
		parentProfile := []byte("parent heap profile, must survive the spawn\n")
		parentStats := []byte("parent memstats, must survive the spawn\n")
		if err := os.WriteFile(profilePath, parentProfile, 0o600); err != nil {
			t.Fatalf("seed parent profile: %v", err)
		}
		if err := os.WriteFile(statsPath, parentStats, 0o600); err != nil {
			t.Fatalf("seed parent stats: %v", err)
		}

		cmd := exec.Command(bdBin, metrics.SendMetricsSubcommand)
		cmd.Env = append(baseEnv(t.TempDir()),
			"BEADS_MEM_PROFILE="+profilePath,
			"BEADS_MEM_STATS="+statsPath,
			metrics.EnvIsFlusher+"=1",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd send-metrics as flusher child: %v\noutput: %s", err, out)
		}

		// The parent's data is untouched, byte for byte.
		if got, err := os.ReadFile(profilePath); err != nil {
			t.Fatalf("parent profile disappeared: %v", err)
		} else if string(got) != string(parentProfile) {
			t.Fatalf("flusher child overwrote the parent's heap profile: got %q, want %q", got, parentProfile)
		}
		if got, err := os.ReadFile(statsPath); err != nil {
			t.Fatalf("parent stats disappeared: %v", err)
		} else if string(got) != string(parentStats) {
			t.Fatalf("flusher child overwrote the parent's MemStats: got %q, want %q", got, parentStats)
		}

		// ...and the child's own diagnostics are still produced, just beside
		// them. This is the half that distinguishes suffixing the destination
		// from simply scrubbing the vars out of the child's environment, which
		// would reinstate the inertness be-wwy2.2 removes.
		requireHeapProfile(t, "flusher child BEADS_MEM_PROFILE", profilePath+".send-metrics")
		childStats, err := os.ReadFile(statsPath + ".send-metrics")
		if err != nil {
			t.Fatalf("flusher child wrote no MemStats of its own: %v", err)
		}
		if !memStatsLine.Match(childStats) {
			t.Fatalf("flusher child MemStats content = %q, want match of %s", childStats, memStatsLine)
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
