//go:build cgo

package versioncontrolops

import (
	"bytes"
	"strings"
	"testing"
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
		status         string
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
			name:    "unexpected summary",
			summary: "pruning failed",
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
