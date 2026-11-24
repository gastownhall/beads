//go:build cgo

package versioncontrolops

import (
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/dolthub/dolt/go/cmd/dolt/cli"
)

// Dolt's DOLT_BACKUP prune procedure writes its summary with cli.Printf
// instead of returning it through SQL. The embedded adapter scopes this
// writer to one backup operation; SQL and proxy adapters report the summary as
// unavailable. Serialize scoped captures because Dolt exposes one process-wide
// output writer, then restore the original writer before returning.
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
// both success and failure.
func CaptureDoltPruneOutput(backupURL string, syncBackup func() error) (BackupPruneOutcome, error) {
	doltPruneOutputCaptureMu.Lock()
	defer doltPruneOutputCaptureMu.Unlock()

	capture := &doltPruneOutputCapture{}
	destination := cli.CliOut
	cli.CliOut = &doltPruneOutputWriter{destination: destination, backupURL: backupURL, capture: capture}
	defer func() { cli.CliOut = destination }()

	err := syncBackup()
	return capture.outcome(), err
}

func (capture *doltPruneOutputCapture) outcome() BackupPruneOutcome {
	if len(capture.summaries) == 0 {
		return unavailableBackupPruneOutcome()
	}
	summary := capture.summaries[len(capture.summaries)-1]
	outcome, ok := parseDoltPruneSummary(summary)
	if !ok {
		return BackupPruneOutcome{
			Status:  "unavailable",
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

func parseDoltPruneSummary(summary string) (BackupPruneOutcome, bool) {
	const filesMarker = " file(s), reclaimed "
	const bytesMarker = " bytes"
	if !strings.HasPrefix(summary, "deleted ") {
		return BackupPruneOutcome{}, false
	}

	filesEnd := strings.Index(summary, filesMarker)
	if filesEnd < 0 {
		return BackupPruneOutcome{}, false
	}
	filesDeleted, err := strconv.Atoi(strings.TrimPrefix(summary[:filesEnd], "deleted "))
	if err != nil || filesDeleted < 0 {
		return BackupPruneOutcome{}, false
	}
	bytesStart := filesEnd + len(filesMarker)
	bytesEnd := strings.Index(summary[bytesStart:], bytesMarker)
	if bytesEnd < 0 {
		return BackupPruneOutcome{}, false
	}
	bytesReclaimed, err := strconv.ParseInt(summary[bytesStart:bytesStart+bytesEnd], 10, 64)
	if err != nil || bytesReclaimed < 0 {
		return BackupPruneOutcome{}, false
	}

	files := filesDeleted
	bytes := bytesReclaimed
	outcome := BackupPruneOutcome{
		Status:         "pruned",
		FilesDeleted:   &files,
		BytesReclaimed: &bytes,
	}
	remainder := summary[bytesStart+bytesEnd+len(bytesMarker):]
	if remainder == "" {
		return outcome, true
	}
	if !strings.HasPrefix(remainder, "; skipped ") {
		return BackupPruneOutcome{}, false
	}
	for _, reason := range strings.Split(strings.TrimPrefix(remainder, "; skipped "), "; skipped ") {
		if reason == "" {
			return BackupPruneOutcome{}, false
		}
		outcome.Skipped = append(outcome.Skipped, reason)
	}
	if filesDeleted == 0 {
		outcome.Status = "skipped"
	} else {
		outcome.Status = "partial"
	}
	return outcome, true
}
