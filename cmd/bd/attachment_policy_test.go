package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentSyncBackupPolicyIsDocumented(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "ATTACHMENTS.md"))
	if err != nil {
		t.Fatalf("ReadFile docs/ATTACHMENTS.md: %v", err)
	}
	doc := normalizedPolicyText(string(data))
	for _, want := range []string{
		"attachment metadata is stored in the dolt database",
		"attachment files are local-only",
		"dolt sync",
		"dolt backups",
		"do not copy the plain files in `.beads/attachments`",
		"`bd show` and `bd attachment list` report those entries as",
		"`bd export` and `.beads/issues.jsonl`",
		"git lfs",
		"bd attachment fsck",
		"bd attachment prune",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("docs/ATTACHMENTS.md missing policy text %q", want)
		}
	}
}

func TestBackupSyncHelpMentionsAttachmentBytesAreNotBackedUp(t *testing.T) {
	help := normalizedPolicyText(backupSyncCmd.Long)
	for _, want := range []string{
		"attachment metadata is part of the database",
		"attachment bytes under .beads/attachments are local files",
		"separate filesystem backup",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("backup sync help missing %q", want)
		}
	}
}

func normalizedPolicyText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
