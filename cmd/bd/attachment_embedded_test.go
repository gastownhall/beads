//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bdAttachment(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"attachment"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd attachment %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func bdAttachmentFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"attachment"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd attachment %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedAttachmentCommands(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "att")
	issue := bdCreate(t, bd, dir, "Attachment issue", "--type", "task")

	source := filepath.Join(dir, "body.md")
	if err := os.WriteFile(source, []byte("# Attachment\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	addOut := bdAttachment(t, bd, dir, "add", issue.ID, source, "--json")
	var added attachmentListItem
	if err := json.Unmarshal([]byte(addOut), &added); err != nil {
		t.Fatalf("parse attachment add JSON: %v\n%s", err, addOut)
	}
	if added.IssueID != issue.ID {
		t.Fatalf("added issue_id = %q, want %q", added.IssueID, issue.ID)
	}
	if added.OriginalFilename != "body.md" {
		t.Fatalf("original filename = %q", added.OriginalFilename)
	}
	if added.Missing {
		t.Fatal("added attachment is unexpectedly missing")
	}

	storedPath := filepath.Join(beadsDir, filepath.FromSlash(added.StorageRelPath))
	if _, err := os.Stat(storedPath); err != nil {
		t.Fatalf("stored attachment not found at %s: %v", storedPath, err)
	}

	listOut := bdAttachment(t, bd, dir, "list", issue.ID, "--json")
	var listed []attachmentListItem
	if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
		t.Fatalf("parse attachment list JSON: %v\n%s", err, listOut)
	}
	if len(listed) != 1 || listed[0].ID != added.ID {
		t.Fatalf("listed attachments = %+v, want one %s", listed, added.ID)
	}

	orphanPath := filepath.Join(beadsDir, "attachments", issue.ID, "orphan")
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsckOut := bdAttachment(t, bd, dir, "fsck", "--json")
	var fsck attachmentMaintenanceResult
	if err := json.Unmarshal([]byte(fsckOut), &fsck); err != nil {
		t.Fatalf("parse attachment fsck JSON: %v\n%s", err, fsckOut)
	}
	if fsck.Count != 1 || fsck.Unreachable[0].StorageRelPath != filepath.ToSlash(filepath.Join("attachments", issue.ID, "orphan")) {
		t.Fatalf("fsck result = %+v, want orphan", fsck)
	}
	bdAttachment(t, bd, dir, "prune", "--dry-run")
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("dry-run prune removed orphan unexpectedly: %v", err)
	}
	bdAttachment(t, bd, dir, "prune")
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("orphan still exists or stat failed unexpectedly: %v", err)
	}

	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyOut := bdAttachment(t, bd, dir, "copy", issue.ID, added.ShortHash, outDir, "--json")
	var copied map[string]interface{}
	if err := json.Unmarshal([]byte(copyOut), &copied); err != nil {
		t.Fatalf("parse attachment copy JSON: %v\n%s", err, copyOut)
	}
	copiedPath := filepath.Join(outDir, "body.md")
	data, err := os.ReadFile(copiedPath)
	if err != nil {
		t.Fatalf("copy target missing: %v", err)
	}
	if string(data) != "# Attachment\n" {
		t.Fatalf("copied data = %q", data)
	}

	failOut := bdAttachmentFail(t, bd, dir, "copy", issue.ID, "body.md", outDir)
	if !strings.Contains(failOut, "already exists") {
		t.Fatalf("overwrite failure = %q, want already exists", failOut)
	}
	bdAttachment(t, bd, dir, "copy", issue.ID, "body.md", outDir, "--force")

	removeOut := bdAttachment(t, bd, dir, "remove", issue.ID, added.ContentHash, "--json")
	var removed map[string]interface{}
	if err := json.Unmarshal([]byte(removeOut), &removed); err != nil {
		t.Fatalf("parse attachment remove JSON: %v\n%s", err, removeOut)
	}
	if removed["status"] != "removed" {
		t.Fatalf("remove status = %v", removed["status"])
	}
	if _, err := os.Stat(storedPath); !os.IsNotExist(err) {
		t.Fatalf("stored attachment still exists or stat failed unexpectedly: %v", err)
	}

	listOut = bdAttachment(t, bd, dir, "list", issue.ID, "--json")
	if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
		t.Fatalf("parse final attachment list JSON: %v\n%s", err, listOut)
	}
	if len(listed) != 0 {
		t.Fatalf("final listed attachments = %+v, want empty", listed)
	}
}
