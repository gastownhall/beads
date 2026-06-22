package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

type attachmentTestStore struct {
	path        string
	cliDir      string
	issues      []*types.Issue
	attachments []*types.Attachment
	err         error
}

func (s attachmentTestStore) Path() string   { return s.path }
func (s attachmentTestStore) CLIDir() string { return s.cliDir }

func (s attachmentTestStore) ListAttachments(context.Context, string) ([]*types.Attachment, error) {
	return s.attachments, s.err
}

func (s attachmentTestStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return s.issues, s.err
}

func TestShortAttachmentHash(t *testing.T) {
	t.Parallel()

	if got := shortAttachmentHash("1234567890abcdef"); got != "1234567890ab" {
		t.Fatalf("shortAttachmentHash() = %q", got)
	}
	if got := shortAttachmentHash("abc"); got != "abc" {
		t.Fatalf("shortAttachmentHash(short) = %q", got)
	}
}

func TestAttachmentListEntryMarksMissingFiles(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	st := attachmentTestStore{path: filepath.Join(tmp, ".beads", "dolt")}
	attachment := &types.Attachment{
		ID:               "att-1",
		IssueID:          "bd-abc",
		HashAlgorithm:    "sha256",
		ContentHash:      "1234567890abcdef",
		OriginalFilename: "body.md",
		MimeType:         "text/markdown",
		ByteSize:         1536,
		StorageRelPath:   "attachments/bd-abc/1234567890abcdef",
		CreatedBy:        "tester",
		CreatedAt:        time.Unix(100, 0).UTC(),
	}

	missing := attachmentListEntry(st, attachment)
	if !missing.Missing {
		t.Fatal("Missing = false, want true")
	}
	if missing.ShortHash != "1234567890ab" {
		t.Fatalf("ShortHash = %q", missing.ShortHash)
	}
	if missing.Size != "1.5 KB" {
		t.Fatalf("Size = %q", missing.Size)
	}

	path := filepath.Join(tmp, ".beads", "attachments", "bd-abc", "1234567890abcdef")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	present := attachmentListEntry(st, attachment)
	if present.Missing {
		t.Fatal("Missing = true, want false")
	}
}

func TestAttachmentRelPathReferenced(t *testing.T) {
	t.Parallel()

	st := attachmentTestStore{
		attachments: []*types.Attachment{
			{ID: "att-1", StorageRelPath: "attachments/bd-abc/hash"},
			{ID: "att-2", StorageRelPath: "attachments/bd-abc/hash"},
			{ID: "att-3", StorageRelPath: "attachments/bd-abc/other"},
		},
	}

	if !attachmentRelPathReferenced(context.Background(), st, "bd-abc", "attachments/bd-abc/hash", "att-1") {
		t.Fatal("referenced by another attachment = false, want true")
	}
	if attachmentRelPathReferenced(context.Background(), st, "bd-abc", "attachments/bd-abc/other", "att-3") {
		t.Fatal("referenced by another attachment = true, want false")
	}
}

func TestAttachmentListEntriesNil(t *testing.T) {
	t.Parallel()

	got := attachmentListEntries(attachmentTestStore{}, nil)
	if got == nil {
		t.Fatal("attachmentListEntries(nil) returned nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestReferencedAttachmentPaths(t *testing.T) {
	t.Parallel()

	st := attachmentTestStore{
		issues: []*types.Issue{{ID: "bd-abc"}},
		attachments: []*types.Attachment{
			{ID: "att-1", StorageRelPath: "attachments/bd-abc/hash"},
			{ID: "att-2", StorageRelPath: ""},
		},
	}

	got, err := referencedAttachmentPaths(context.Background(), st)
	if err != nil {
		t.Fatalf("referencedAttachmentPaths() error = %v", err)
	}
	if _, ok := got["attachments/bd-abc/hash"]; !ok {
		t.Fatalf("referenced paths = %#v, want stored path", got)
	}
	if _, ok := got[""]; ok {
		t.Fatalf("referenced paths included empty key: %#v", got)
	}
}

func TestScanUnreachableAttachmentFiles(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	st := attachmentTestStore{path: filepath.Join(tmp, ".beads", "dolt")}
	referencedPath := filepath.Join(tmp, ".beads", "attachments", "bd-abc", "referenced")
	unreachablePath := filepath.Join(tmp, ".beads", "attachments", "bd-abc", "unreachable")
	for _, path := range []string{referencedPath, unreachablePath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := scanUnreachableAttachmentFiles(st, map[string]struct{}{
		"attachments/bd-abc/referenced": {},
	})
	if err != nil {
		t.Fatalf("scanUnreachableAttachmentFiles() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %#v", len(got), got)
	}
	if got[0].StorageRelPath != "attachments/bd-abc/unreachable" {
		t.Fatalf("StorageRelPath = %q", got[0].StorageRelPath)
	}
	if got[0].ByteSize != 4 || got[0].Size != "4 B" {
		t.Fatalf("size = %d/%q", got[0].ByteSize, got[0].Size)
	}
}
