//go:build cgo

package versioncontrolops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dolthub/dolt/go/cmd/dolt/cli"

	"github.com/steveyegge/beads/internal/storage"
)

func TestDoltPruneOutputWriterPassesNonPruneOutputUnchanged(t *testing.T) {
	var destination bytes.Buffer
	writer := &doltPruneOutputWriter{destination: &destination}
	input := []byte("Cloning backup destination\rprogress 1/2\n")

	n, err := writer.Write(input)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(input) {
		t.Fatalf("Write returned %d bytes, want %d", n, len(input))
	}
	if !bytes.Equal(destination.Bytes(), input) {
		t.Fatalf("forwarded output = %q, want %q", destination.String(), input)
	}
}

func TestDoltPruneOutputWriterCapturesOnlyMatchingDestination(t *testing.T) {
	for _, tt := range []struct {
		name      string
		backupURL string
		other     string
	}{
		{name: "plain", backupURL: "file:///tmp/backup", other: "file:///tmp/other"},
		// DirToFileURL does not escape paths, so a URL can contain ": ".
		{name: "url with colon-space", backupURL: "file:///tmp/beads: old", other: "file:///tmp/beads"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var destination bytes.Buffer
			capture := &doltPruneOutputCapture{}
			writer := &doltPruneOutputWriter{destination: &destination, backupURL: tt.backupURL, capture: capture}
			matching := []byte("Pruning " + tt.backupURL + ": deleted 2 file(s), reclaimed 471490 bytes")
			unmatched := []byte("Pruning " + tt.other + ": deleted 1 file(s), reclaimed 8 bytes")

			if _, err := writer.Write(matching); err != nil {
				t.Fatalf("Write matching summary: %v", err)
			}
			if _, err := writer.Write(unmatched); err != nil {
				t.Fatalf("Write unmatched summary: %v", err)
			}
			if destination.String() != string(unmatched) {
				t.Fatalf("forwarded output = %q, want unmatched summary %q", destination.String(), unmatched)
			}
			outcome := capture.outcome()
			if outcome.Status != "pruned" || outcome.FilesDeleted == nil || *outcome.FilesDeleted != 2 {
				t.Fatalf("captured outcome = %+v, want two deleted files", outcome)
			}
		})
	}
}

func TestCaptureDoltPruneOutputRestoresGlobalWriter(t *testing.T) {
	original := cli.CliOut
	var destination bytes.Buffer
	cli.CliOut = &destination
	t.Cleanup(func() { cli.CliOut = original })

	outcome, err := CaptureDoltPruneOutput("file:///tmp/backup", func() error {
		_, err := cli.CliOut.Write([]byte("Pruning file:///tmp/backup: deleted 2 file(s), reclaimed 471490 bytes"))
		return err
	})
	if err != nil {
		t.Fatalf("CaptureDoltPruneOutput: %v", err)
	}
	if cli.CliOut != &destination {
		t.Fatalf("cli.CliOut after capture = %T, want restored destination writer", cli.CliOut)
	}
	if destination.Len() != 0 {
		t.Fatalf("captured summary leaked to output: %q", destination.String())
	}
	if outcome.Status != "pruned" || outcome.FilesDeleted == nil || *outcome.FilesDeleted != 2 {
		t.Fatalf("captured outcome = %+v, want two deleted files", outcome)
	}
}

// installCaptureTestWriter points Dolt's global writer at a buffer for one
// test and restores the original afterwards.
func installCaptureTestWriter(t *testing.T) *bytes.Buffer {
	t.Helper()
	original := cli.CliOut
	destination := &bytes.Buffer{}
	cli.CliOut = destination
	t.Cleanup(func() { cli.CliOut = original })
	return destination
}

func TestCaptureDoltPruneOutputRestoresWriterOnSyncError(t *testing.T) {
	destination := installCaptureTestWriter(t)
	syncErr := errors.New("sync failed")

	_, err := CaptureDoltPruneOutput("file:///tmp/backup", func() error { return syncErr })
	if err != syncErr {
		t.Fatalf("CaptureDoltPruneOutput error = %v, want the sync error unchanged", err)
	}
	if cli.CliOut != destination {
		t.Fatalf("cli.CliOut after failed sync = %T, want restored destination writer", cli.CliOut)
	}
}

func TestCaptureDoltPruneOutputRestoresWriterAndUnlocksOnPanic(t *testing.T) {
	destination := installCaptureTestWriter(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("CaptureDoltPruneOutput swallowed the sync panic")
			}
		}()
		_, _ = CaptureDoltPruneOutput("file:///tmp/backup", func() error { panic("sync panicked") })
	}()
	if cli.CliOut != destination {
		t.Fatalf("cli.CliOut after panic = %T, want restored destination writer", cli.CliOut)
	}
	// The capture mutex must have been released, or this second capture
	// deadlocks.
	if _, err := CaptureDoltPruneOutput("file:///tmp/backup", func() error { return nil }); err != nil {
		t.Fatalf("capture after panic: %v", err)
	}
}

func TestCaptureDoltPruneOutputReportsUnrecognizedSummary(t *testing.T) {
	installCaptureTestWriter(t)

	outcome, err := CaptureDoltPruneOutput("file:///tmp/backup", func() error {
		_, err := cli.CliOut.Write([]byte("Pruning file:///tmp/backup: removed everything\n"))
		return err
	})
	if err != nil {
		t.Fatalf("CaptureDoltPruneOutput: %v", err)
	}
	if outcome.Status != storage.BackupPruneUnavailable ||
		outcome.Message != "Dolt returned an unrecognized prune summary: removed everything" {
		t.Fatalf("outcome = %+v, want unavailable with the unrecognized summary", outcome)
	}
}

func TestCaptureDoltPruneOutputUsesLastSummaryAndStripsCRLF(t *testing.T) {
	destination := installCaptureTestWriter(t)

	outcome, err := CaptureDoltPruneOutput("file:///tmp/backup", func() error {
		for _, line := range []string{
			"Pruning file:///tmp/backup: deleted 1 file(s), reclaimed 8 bytes\n",
			"Pruning file:///tmp/backup: deleted 0 file(s), reclaimed 0 bytes; skipped destination is busy\r\n",
		} {
			if _, err := cli.CliOut.Write([]byte(line)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CaptureDoltPruneOutput: %v", err)
	}
	if outcome.Status != storage.BackupPruneSkipped || len(outcome.Skipped) != 1 || outcome.Skipped[0] != "destination is busy" {
		t.Fatalf("outcome = %+v, want the last summary with its CRLF stripped", outcome)
	}
	if destination.Len() != 0 {
		t.Fatalf("captured summaries leaked to output: %q", destination.String())
	}
}

func TestCaptureDoltPruneOutputSerializesConcurrentCaptures(t *testing.T) {
	installCaptureTestWriter(t)

	// Assert the lock is held during the callback even when the scheduler runs
	// all concurrent captures sequentially.
	_, err := CaptureDoltPruneOutput("file:///tmp/backup", func() error {
		if doltPruneOutputCaptureMu.TryLock() {
			doltPruneOutputCaptureMu.Unlock()
			return errors.New("backup sync ran without the capture lock")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Overlap is checked inside each sync rather than inferred from the
	// captured counts: without doltPruneOutputCaptureMu, nested wrappers can
	// still route every summary to the right capture. A second sync in flight,
	// or a global writer that is not this capture's own, fails the test
	// directly.
	const captures = 8
	var inFlight, overlaps, foreignWriters atomic.Int32
	outcomes := make([]storage.BackupPruneOutcome, captures)
	var wg sync.WaitGroup
	for i := 0; i < captures; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			backupURL := fmt.Sprintf("file:///tmp/backup-%d", i)
			outcomes[i], _ = CaptureDoltPruneOutput(backupURL, func() error {
				if inFlight.Add(1) > 1 {
					overlaps.Add(1)
				}
				defer inFlight.Add(-1)
				for step := 0; step < 50; step++ {
					if writer, ok := cli.CliOut.(*doltPruneOutputWriter); !ok || writer.backupURL != backupURL {
						foreignWriters.Add(1)
					}
					runtime.Gosched()
				}
				summary := fmt.Sprintf("Pruning %s: deleted %d file(s), reclaimed 8 bytes\n", backupURL, i+1)
				_, err := cli.CliOut.Write([]byte(summary))
				return err
			})
		}(i)
	}
	wg.Wait()
	if n := overlaps.Load(); n != 0 {
		t.Fatalf("%d captures ran while another capture's sync was in flight", n)
	}
	if n := foreignWriters.Load(); n != 0 {
		t.Fatalf("%d checks inside a sync saw a cli.CliOut that was not that capture's writer", n)
	}
	for i, got := range outcomes {
		if got.FilesDeleted == nil || *got.FilesDeleted != i+1 {
			t.Fatalf("capture %d = %+v, want only its own summary (%d deleted)", i, got, i+1)
		}
	}
}

// TestDoltPruneOutputMinGracePeriodMatchesDolt pins bd's copy of Dolt's prune
// grace-period floor to Dolt's own constant. storage cannot import Dolt's cli
// package, so without this check a Dolt upgrade that moves the floor would
// leave bd validating against a stale minimum.
func TestDoltPruneOutputMinGracePeriodMatchesDolt(t *testing.T) {
	if storage.MinBackupPruneGracePeriod != cli.BackupPruneMinGracePeriod {
		t.Fatalf("storage.MinBackupPruneGracePeriod = %s, Dolt's cli.BackupPruneMinGracePeriod = %s; update bd's copy",
			storage.MinBackupPruneGracePeriod, cli.BackupPruneMinGracePeriod)
	}
}

// TestParseDoltPruneSummaryJSONCountKeys pins the JSON contract documented on
// storage.BackupPruneOutcome at the producers: a parsed skipped summary emits
// files_deleted and bytes_reclaimed as 0, and the unavailable outcome omits
// both keys.
func TestParseDoltPruneSummaryJSONCountKeys(t *testing.T) {
	skipped, ok := parseDoltPruneSummary("deleted 0 file(s), reclaimed 0 bytes; skipped destination is busy")
	if !ok || skipped.Status != storage.BackupPruneSkipped {
		t.Fatalf("parseDoltPruneSummary(skipped) = %+v, %v; want a skipped outcome", skipped, ok)
	}
	for _, tt := range []struct {
		name        string
		outcome     storage.BackupPruneOutcome
		wantPresent bool
	}{
		{name: "skipped", outcome: skipped, wantPresent: true},
		{name: "unavailable", outcome: unavailableBackupPruneOutcome(), wantPresent: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.outcome)
			if err != nil {
				t.Fatalf("marshal %+v: %v", tt.outcome, err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &keys); err != nil {
				t.Fatalf("unmarshal %s: %v", encoded, err)
			}
			for _, key := range []string{"files_deleted", "bytes_reclaimed"} {
				value, present := keys[key]
				if present != tt.wantPresent {
					t.Fatalf("%s present = %v, want %v: %s", key, present, tt.wantPresent, encoded)
				}
				if present && string(value) != "0" {
					t.Fatalf("%s = %s, want 0: %s", key, value, encoded)
				}
			}
		})
	}
}

// TestCaptureDoltPruneOutputNoSummaryDoesNotCiteServer pins the embedded
// no-summary message: embedded mode has no Dolt server, so it must not send
// the user to server output or logs the way the SQL-route message does.
func TestCaptureDoltPruneOutputNoSummaryDoesNotCiteServer(t *testing.T) {
	installCaptureTestWriter(t)
	outcome, err := CaptureDoltPruneOutput("file:///tmp/backup", func() error { return nil })
	if err != nil {
		t.Fatalf("CaptureDoltPruneOutput: %v", err)
	}
	if outcome.Status != storage.BackupPruneUnavailable {
		t.Fatalf("Status = %q, want unavailable", outcome.Status)
	}
	if strings.Contains(strings.ToLower(outcome.Message), "server") {
		t.Fatalf("Message = %q, want no reference to a Dolt server in embedded mode", outcome.Message)
	}
	if !strings.Contains(outcome.Message, "found nothing to prune") {
		t.Fatalf("Message = %q, want it to explain that there may be nothing to prune", outcome.Message)
	}
}

func TestUnavailableBackupPruneOutcomeMentionsNoWork(t *testing.T) {
	outcome := unavailableBackupPruneOutcome()
	if outcome.Status != "unavailable" {
		t.Fatalf("Status = %q, want unavailable", outcome.Status)
	}
	if !strings.Contains(outcome.Message, "found nothing to prune") {
		t.Fatalf("Message = %q, want it to explain that there may be nothing to prune", outcome.Message)
	}
}

func TestParseDoltPruneSummary(t *testing.T) {
	tests := []struct {
		name           string
		summary        string
		status         storage.BackupPruneStatus
		filesDeleted   int
		bytesReclaimed int64
		skipped        []string
		wantOK         bool
	}{
		{
			name:           "deleted files",
			summary:        "deleted 2 file(s), reclaimed 471490 bytes",
			status:         "pruned",
			filesDeleted:   2,
			bytesReclaimed: 471490,
			wantOK:         true,
		},
		{
			name:           "skipped because destination was recent",
			summary:        "deleted 0 file(s), reclaimed 0 bytes; skipped destination has a file newer than 10m",
			status:         "skipped",
			bytesReclaimed: 0,
			skipped:        []string{"destination has a file newer than 10m"},
			wantOK:         true,
		},
		{
			name:           "partial deletion and skip",
			summary:        "deleted 1 file(s), reclaimed 8 bytes; skipped manifest lock was busy; skipped destination has a recent file",
			status:         "partial",
			filesDeleted:   1,
			bytesReclaimed: 8,
			skipped:        []string{"manifest lock was busy", "destination has a recent file"},
			wantOK:         true,
		},
		{
			// Dolt does not print this for a no-op prune today; the parser
			// still accepts it rather than misreporting it as unrecognized.
			name:           "zero counts without skips",
			summary:        "deleted 0 file(s), reclaimed 0 bytes",
			status:         "pruned",
			filesDeleted:   0,
			bytesReclaimed: 0,
			wantOK:         true,
		},
		{
			name:    "unexpected summary",
			summary: "pruning failed",
		},
		{
			name:    "non-numeric file count",
			summary: "deleted x file(s), reclaimed 8 bytes",
		},
		{
			name:    "negative file count",
			summary: "deleted -1 file(s), reclaimed 8 bytes",
		},
		{
			name:    "missing files marker",
			summary: "deleted 1 files, reclaimed 8 bytes",
		},
		{
			name:    "missing bytes marker",
			summary: "deleted 1 file(s), reclaimed 8",
		},
		{
			name:    "non-numeric byte count",
			summary: "deleted 1 file(s), reclaimed many bytes",
		},
		{
			name:    "overflowing byte count",
			summary: "deleted 1 file(s), reclaimed 99999999999999999999 bytes",
		},
		{
			name:    "negative byte count",
			summary: "deleted 1 file(s), reclaimed -8 bytes",
		},
		{
			name:    "unexpected trailer",
			summary: "deleted 1 file(s), reclaimed 8 bytes, done",
		},
		{
			name:    "empty trailing skip reason",
			summary: "deleted 1 file(s), reclaimed 8 bytes; skipped ",
		},
		{
			name:    "empty skip reason between reasons",
			summary: "deleted 0 file(s), reclaimed 0 bytes; skipped ; skipped busy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseDoltPruneSummary(tt.summary)
			if ok != tt.wantOK {
				t.Fatalf("parseDoltPruneSummary ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Status != tt.status {
				t.Errorf("Status = %q, want %q", got.Status, tt.status)
			}
			if got.FilesDeleted == nil || *got.FilesDeleted != tt.filesDeleted {
				t.Errorf("FilesDeleted = %v, want %d", got.FilesDeleted, tt.filesDeleted)
			}
			if got.BytesReclaimed == nil || *got.BytesReclaimed != tt.bytesReclaimed {
				t.Errorf("BytesReclaimed = %v, want %d", got.BytesReclaimed, tt.bytesReclaimed)
			}
			if len(got.Skipped) != len(tt.skipped) {
				t.Fatalf("Skipped = %v, want %v", got.Skipped, tt.skipped)
			}
			for i := range tt.skipped {
				if got.Skipped[i] != tt.skipped[i] {
					t.Errorf("Skipped[%d] = %q, want %q", i, got.Skipped[i], tt.skipped[i])
				}
			}
		})
	}
}
