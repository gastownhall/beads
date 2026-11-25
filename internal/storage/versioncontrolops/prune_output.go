//go:build cgo

package versioncontrolops

import (
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/dolthub/dolt/go/cmd/dolt/cli"

	"github.com/steveyegge/beads/internal/storage"
)

// Dolt's DOLT_BACKUP prune procedure writes its summary with cli.Printf
// instead of returning it through SQL, so the embedded adapter installs a
// filtering writer for the duration of one backup sync; SQL and proxy adapters
// report the summary as unavailable.
//
// cli.CliOut is a single process-wide variable that Dolt reads without
// synchronization on every cli.Printf. doltPruneOutputCaptureMu serializes
// captures against each other only; it does not synchronize the swap against
// other goroutines printing through Dolt's cli package. Such a concurrent
// print during the capture window is a data race on cli.CliOut, and its
// output is routed through the wrapper, which forwards every line that is not
// this destination's prune summary. The only caller is the embedded
// EmbeddedDoltStore.BackupSyncWithPrune, which bd runs under the cross-process
// backup lock from a single short-lived command, where no other Dolt CLI
// output is expected; any new caller must preserve that invariant.
var doltPruneOutputCaptureMu sync.Mutex

type doltPruneOutputWriter struct {
	destination io.Writer
	backupURL   string
	capture     *doltPruneOutputCapture
	mu          sync.Mutex
}

func (w *doltPruneOutputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if summary, ok := doltPruneSummaryForURL(string(p), w.backupURL); ok && w.capture != nil {
		w.capture.summaries = append(w.capture.summaries, summary)
		return len(p), nil
	}
	return w.destination.Write(p)
}

type doltPruneOutputCapture struct {
	summaries []string
}

// CaptureDoltPruneOutput observes the summary emitted by one embedded backup
// sync. The writer is installed only while syncBackup runs and is restored on
// success, failure and panic. When Dolt prints several summaries for the
// destination, the last one wins. It is compiled only with cgo, like its one
// caller in embeddeddolt.
func CaptureDoltPruneOutput(backupURL string, syncBackup func() error) (storage.BackupPruneOutcome, error) {
	doltPruneOutputCaptureMu.Lock()
	defer doltPruneOutputCaptureMu.Unlock()

	capture := &doltPruneOutputCapture{}
	destination := cli.CliOut
	cli.CliOut = &doltPruneOutputWriter{destination: destination, backupURL: backupURL, capture: capture}
	defer func() { cli.CliOut = destination }()

	err := syncBackup()
	return capture.outcome(), err
}

// noEmbeddedPruneSummaryOutcome is the outcome when the in-process capture
// observed no summary. There is no Dolt server in embedded mode, so unlike
// unavailableBackupPruneOutcome it does not point at server output: Dolt
// prints nothing for a no-op prune and logs, rather than prints, a prune error.
func noEmbeddedPruneSummaryOutcome() storage.BackupPruneOutcome {
	return storage.BackupPruneOutcome{
		Status: storage.BackupPruneUnavailable,
		Message: "Dolt did not print a prune summary; best-effort pruning found nothing to prune or failed " +
			"(Dolt logs a prune failure instead of printing it). The backup sync itself succeeded.",
	}
}

func (capture *doltPruneOutputCapture) outcome() storage.BackupPruneOutcome {
	if len(capture.summaries) == 0 {
		return noEmbeddedPruneSummaryOutcome()
	}
	summary := capture.summaries[len(capture.summaries)-1]
	outcome, ok := parseDoltPruneSummary(summary)
	if !ok {
		return storage.BackupPruneOutcome{
			Status:  storage.BackupPruneUnavailable,
			Message: "Dolt returned an unrecognized prune summary: " + summary,
		}
	}
	return outcome
}

func doltPruneSummaryForURL(line, backupURL string) (string, bool) {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	rest := strings.TrimPrefix(line, "Pruning ")
	if rest == line {
		return "", false
	}
	separator := strings.Index(rest, ": ")
	if separator < 0 || rest[:separator] != backupURL {
		return "", false
	}
	return rest[separator+2:], true
}

func parseDoltPruneSummary(summary string) (storage.BackupPruneOutcome, bool) {
	const filesMarker = " file(s), reclaimed "
	const bytesMarker = " bytes"
	if !strings.HasPrefix(summary, "deleted ") {
		return storage.BackupPruneOutcome{}, false
	}

	filesEnd := strings.Index(summary, filesMarker)
	if filesEnd < 0 {
		return storage.BackupPruneOutcome{}, false
	}
	filesDeleted, err := strconv.Atoi(strings.TrimPrefix(summary[:filesEnd], "deleted "))
	if err != nil || filesDeleted < 0 {
		return storage.BackupPruneOutcome{}, false
	}
	bytesStart := filesEnd + len(filesMarker)
	bytesEnd := strings.Index(summary[bytesStart:], bytesMarker)
	if bytesEnd < 0 {
		return storage.BackupPruneOutcome{}, false
	}
	bytesReclaimed, err := strconv.ParseInt(summary[bytesStart:bytesStart+bytesEnd], 10, 64)
	if err != nil || bytesReclaimed < 0 {
		return storage.BackupPruneOutcome{}, false
	}

	files := filesDeleted
	bytes := bytesReclaimed
	outcome := storage.BackupPruneOutcome{
		Status:         storage.BackupPrunePruned,
		FilesDeleted:   &files,
		BytesReclaimed: &bytes,
	}
	remainder := summary[bytesStart+bytesEnd+len(bytesMarker):]
	if remainder == "" {
		return outcome, true
	}
	if !strings.HasPrefix(remainder, "; skipped ") {
		return storage.BackupPruneOutcome{}, false
	}
	for _, reason := range strings.Split(strings.TrimPrefix(remainder, "; skipped "), "; skipped ") {
		if reason == "" {
			return storage.BackupPruneOutcome{}, false
		}
		outcome.Skipped = append(outcome.Skipped, reason)
	}
	if filesDeleted == 0 {
		outcome.Status = storage.BackupPruneSkipped
	} else {
		outcome.Status = storage.BackupPrunePartial
	}
	return outcome, true
}
