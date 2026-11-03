package versioncontrolops

import (
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/dolthub/dolt/go/cmd/dolt/cli"
)

// Dolt's DOLT_BACKUP prune procedure writes its summary with cli.Printf
// instead of returning it through SQL. Wrap Dolt's exported output writer once
// at package initialization: this diverts only prune summaries from stdout and
// associates them with the registered destination. It avoids redirecting
// process stdout or swapping Dolt's global writer around a query, either of
// which would race unrelated commands.
func init() {
	cli.CliOut = &doltPruneOutputWriter{destination: cli.CliOut}
}

type doltPruneOutputWriter struct {
	destination io.Writer
	mu          sync.Mutex
}

func (w *doltPruneOutputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	line := string(p)
	if strings.HasPrefix(line, "Pruning ") {
		if recordDoltPruneOutput(line) {
			return len(p), nil
		}
		return io.WriteString(cli.CliErr, line)
	}
	return w.destination.Write(p)
}

type doltPruneOutputCapture struct {
	summaries []string
}

var doltPruneOutputCaptures = struct {
	sync.Mutex
	byURL map[string][]*doltPruneOutputCapture
}{byURL: make(map[string][]*doltPruneOutputCapture)}

func beginDoltPruneOutputCapture(backupURL string) func() BackupPruneOutcome {
	capture := &doltPruneOutputCapture{}
	doltPruneOutputCaptures.Lock()
	doltPruneOutputCaptures.byURL[backupURL] = append(doltPruneOutputCaptures.byURL[backupURL], capture)
	doltPruneOutputCaptures.Unlock()

	return func() BackupPruneOutcome {
		doltPruneOutputCaptures.Lock()
		captures := doltPruneOutputCaptures.byURL[backupURL]
		for i, active := range captures {
			if active == capture {
				captures = append(captures[:i], captures[i+1:]...)
				break
			}
		}
		if len(captures) == 0 {
			delete(doltPruneOutputCaptures.byURL, backupURL)
		} else {
			doltPruneOutputCaptures.byURL[backupURL] = captures
		}
		summaries := append([]string(nil), capture.summaries...)
		doltPruneOutputCaptures.Unlock()

		if len(summaries) == 0 {
			return unavailableBackupPruneOutcome()
		}
		outcome, ok := parseDoltPruneSummary(summaries[len(summaries)-1])
		if !ok {
			return BackupPruneOutcome{
				Status:  "unavailable",
				Message: "Dolt returned an unrecognized prune summary: " + summaries[len(summaries)-1],
			}
		}
		return outcome
	}
}

func recordDoltPruneOutput(line string) bool {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	rest := strings.TrimPrefix(line, "Pruning ")
	separator := strings.Index(rest, ": ")
	if separator < 0 {
		return false
	}
	backupURL := rest[:separator]
	summary := rest[separator+2:]

	doltPruneOutputCaptures.Lock()
	defer doltPruneOutputCaptures.Unlock()
	captures := doltPruneOutputCaptures.byURL[backupURL]
	if len(captures) == 0 {
		return false
	}
	for _, capture := range captures {
		capture.summaries = append(capture.summaries, summary)
	}
	return true
}

func unavailableBackupPruneOutcome() BackupPruneOutcome {
	return BackupPruneOutcome{
		Status: "unavailable",
		Message: "Dolt did not return a prune summary; best-effort pruning may have skipped or failed. " +
			"Check Dolt server logs for details.",
	}
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
