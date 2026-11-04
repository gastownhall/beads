//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedAutoExportFirstCreateAfterOptIn walks the documented opt-in
// recipe from docs/reference/configuration.md (GH#6955): on a fresh
// workspace, the two `bd config set` commands each run their own post-command
// auto-export over an EMPTY store, which used to open a full export.interval
// throttle window. The first `bd create` then produced no .beads/issues.jsonl
// until a later write — or none at all. The automatic file must exist and
// contain the new issue with no explicit export.
func TestEmbeddedAutoExportFirstCreateAfterOptIn(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fe")

	// The documented two-config-command opt-in sequence.
	for _, kv := range [][]string{
		{"export.auto", "true"},
		{"export.git-add", "true"},
	} {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", kv[0], kv[1]); err != nil {
			t.Fatalf("bd config set %s %s: %v\n%s", kv[0], kv[1], err, out)
		}
	}

	// The first durable create, well inside the default 60s interval.
	id := bdCreateSilent(t, bd, dir, "First issue after opt-in", "--type", "task")

	data, err := os.ReadFile(filepath.Join(beadsDir, "issues.jsonl"))
	if err != nil {
		t.Fatalf("automatic export file missing after the first create: %v", err)
	}
	if !strings.Contains(string(data), id) {
		t.Fatalf("issues.jsonl does not contain the newly created issue %s:\n%s", id, data)
	}
}

// TestEmbeddedAutoExportBatchModeTracksUncommittedWrites is GH#6955's
// uncommitted-write half, on top of the empty-export half above. Embedded
// change detection used HEAD only (HASHOF('HEAD'); the store had no
// storage.StateHasher), so under dolt.auto-commit batch/off a durable create
// that had not been version-committed looked like "no changes since the last
// export" and the automatic JSONL stayed silent for as long as HEAD stood
// still — with no further write, indefinitely. The baseline issues stay
// nonempty so the empty-export branch is never used, and the seeded state's
// timestamp is deliberately older than the default export.interval so that
// ONLY change detection, not the throttle, decides whether the batch-mode
// create exports.
func TestEmbeddedAutoExportBatchModeTracksUncommittedWrites(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	for _, mode := range []string{"batch", "off"} {
		t.Run(mode, func(t *testing.T) {
			bd := buildEmbeddedBD(t)
			// bd init names the embedded database after the prefix.
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "bu")
			opts := []struct{ key, value string }{
				{"export.auto", "true"},
				{"export.git-add", "false"},
			}
			for _, kv := range opts {
				if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", kv.key, kv.value); err != nil {
					t.Fatalf("bd config set %s %s: %v\n%s", kv.key, kv.value, err, out)
				}
			}

			// A committed, exported baseline: under the default on-policy a
			// create version-commits, its post-command pass sees a changed
			// HEAD, and issues.jsonl is nonempty before the scenario starts.
			baselineID := bdCreateSilent(t, bd, dir, "Baseline committed issue", "--type", "task")
			baselineJSONL := readAutoExportJSONL(t, filepath.Join(beadsDir, "issues.jsonl"))
			if !strings.Contains(baselineJSONL, baselineID) {
				t.Fatalf("baseline create did not auto-export %s — the scenario needs a nonempty exported baseline:\n%s", baselineID, baselineJSONL)
			}
			baselineHead := embeddedTestHead(t, dir, beadsDir, "bu")

			// Seed: make the saved timestamp safely older than the 60s
			// default export.interval, and prove the seeded state really
			// satisfies shouldExport before the batch-mode create.
			state := loadExportAutoState(beadsDir)
			state.Timestamp = time.Now().Add(-2 * time.Hour)
			saveExportAutoState(beadsDir, state)
			if seeded := loadExportAutoState(beadsDir); !shouldExport(seeded, time.Minute) {
				t.Fatalf("seeded state %#v must satisfy shouldExport before the create", seeded)
			}

			// The eligible write: durable, but without a version commit, so
			// HEAD must stand still across it.
			followUpID := bdCreateSilent(t, bd, dir, "Uncommitted follow-up", "--type", "task", "--dolt-auto-commit", mode)
			if head := embeddedTestHead(t, dir, beadsDir, "bu"); head != baselineHead {
				t.Fatalf("HEAD advanced %s → %s under dolt-auto-commit=%s; the create did not stay unversioned, so this run proves nothing", baselineHead, head, mode)
			}

			data, err := os.ReadFile(filepath.Join(beadsDir, "issues.jsonl"))
			if err != nil {
				t.Fatalf("automatic export file missing after the uncommitted create: %v", err)
			}
			for _, id := range []string{baselineID, followUpID} {
				if !strings.Contains(string(data), id) {
					t.Errorf("issues.jsonl does not contain %s after the uncommitted create under dolt-auto-commit=%s:\n%s", id, mode, data)
				}
			}
		})
	}
}

func readAutoExportJSONL(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// embeddedTestHead reads HEAD straight out of the fixture's embedded database
// (the way readBackOnce opens the store) so the test can pin that a
// batch/off-mode create really did leave HEAD standing still. The embedded
// engine serializes concurrent openers, so probe with the same retry shape.
func embeddedTestHead(t *testing.T, dir, beadsDir, database string) string {
	t.Helper()
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		head, err := embeddedTestHeadOnce(dir, beadsDir, database)
		if err == nil {
			return head
		}
		lastErr = err
		if !strings.Contains(err.Error(), "locked") {
			break // non-lock error, don't retry
		}
		t.Logf("embeddedTestHead: attempt %d/%d got lock error, retrying: %v", attempt+1, maxAttempts, err)
	}
	t.Fatalf("embeddedTestHead(%s, %s): %v", beadsDir, database, lastErr)
	return ""
}

func embeddedTestHeadOnce(dir, beadsDir, database string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		return "", err
	}
	defer st.Close()
	hash, err := st.GetCurrentCommit(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}
