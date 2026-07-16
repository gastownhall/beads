package flatfile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// mustSymlink creates a symlink or skips the test on platforms where symlink
// creation is unavailable (e.g. Windows without the required privilege). A
// collision with an existing file is a test bug, not a platform limitation,
// and fails instead of skipping: an EEXIST-triggered skip silently disabled
// the symlink regressions on every platform once CreateIssue started
// recording an event file at the path a test wanted to link.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, fs.ErrExist) {
			t.Fatalf("symlink collision (not a platform limitation): %v", err)
		}
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestValidateID(t *testing.T) {
	valid := []string{"proj-abc", "proj-abc.1", "PROJ-123", "a-b-c"}
	for _, id := range valid {
		if err := validateID(id); err != nil {
			t.Errorf("validateID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"../etc/passwd",
		"foo/bar",
		"foo\\bar",
		".hidden",
		"..sneaky",
		"../../escape",
	}
	for _, id := range invalid {
		if err := validateID(id); err == nil {
			t.Errorf("validateID(%q) = nil, want error", id)
		}
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	malicious := []string{
		"../../etc/passwd",
		"../sibling",
		"foo/../../escape",
	}

	for _, id := range malicious {
		// CreateIssue should reject
		err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "evil"}, "attacker")
		if err == nil {
			t.Errorf("CreateIssue(%q) should be rejected", id)
		}

		// GetIssue should reject
		_, err = s.GetIssue(ctx, id)
		if err == nil {
			t.Errorf("GetIssue(%q) should be rejected", id)
		}

		// DeleteIssue should reject
		err = s.DeleteIssue(ctx, id)
		if err == nil {
			t.Errorf("DeleteIssue(%q) should be rejected", id)
		}
	}
}

func TestSafePathRejectsSymlinks(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()

	// Symlinked directory child (comments/BD-1 -> outside).
	mustSymlink(t, outside, filepath.Join(parent, "BD-1"))
	if _, err := safePath(parent, "BD-1", ""); err == nil {
		t.Error("safePath allowed a symlinked directory child")
	}

	// Symlinked file child (events/BD-2.jsonl -> outside file).
	target := filepath.Join(outside, "victim")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, target, filepath.Join(parent, "BD-2.jsonl"))
	if _, err := safePath(parent, "BD-2", ".jsonl"); err == nil {
		t.Error("safePath allowed a symlinked file child")
	}

	// Symlinked parent directory (events -> outside).
	linkParent := filepath.Join(t.TempDir(), "events")
	mustSymlink(t, outside, linkParent)
	if _, err := safePath(linkParent, "BD-3", ".jsonl"); err == nil {
		t.Error("safePath allowed a symlinked parent directory")
	}

	// Regular paths, existing or not, still resolve.
	if _, err := safePath(parent, "BD-4", ".json"); err != nil {
		t.Errorf("safePath rejected a regular missing child: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "BD-5"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := safePath(parent, "BD-5", ""); err != nil {
		t.Errorf("safePath rejected a regular existing directory: %v", err)
	}
}

// TestStoreRootSymlinkRejected covers the ancestor-symlink gap left by the
// per-write checks exercised in TestSafePathRejectsSymlinks: rejectSymlink
// Lstats only the FINAL path component and Lstat traverses ancestors, so a
// hostile repo committing .beads itself as a symlink (the same
// checkout-materialized threat) passed every safePath check —
// Lstat(".beads/issues") follows the .beads link and sees a regular dir at
// the target — and bd create then wrote issue files outside the store.
// Opening a store whose root is a symlink must fail on every constructor,
// and the failed open must not create anything at the link target.
func TestStoreRootSymlinkRejected(t *testing.T) {
	target := t.TempDir() // attacker-chosen destination outside the checkout
	link := filepath.Join(t.TempDir(), ".beads")
	mustSymlink(t, target, link)

	// Valid flatfile metadata at the target so OpenStore gets past metadata
	// verification and the symlink check is what must reject it.
	if err := os.WriteFile(filepath.Join(target, "metadata.json"), []byte(`{"backend":"flatfile"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := NewFlatFileStore(link); err == nil {
		t.Error("NewFlatFileStore opened a symlinked store root")
	}
	if _, err := newFlatFileStoreNoCreate(link); err == nil {
		t.Error("newFlatFileStoreNoCreate opened a symlinked store root")
	}
	if _, err := OpenStore(ctx, link); err == nil {
		t.Error("OpenStore opened a symlinked store root")
	}
	if _, err := OpenStoreReadOnly(ctx, link); err == nil {
		t.Error("OpenStoreReadOnly opened a symlinked store root")
	}

	// Nothing may have been created through the link: only the metadata.json
	// planted above exists at the target.
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "metadata.json" {
		t.Errorf("open created %d entrie(s) at the symlink target", len(entries)-1)
	}

	// A regular (non-symlink) store root still opens.
	if _, err := NewFlatFileStore(filepath.Join(t.TempDir(), ".beads")); err != nil {
		t.Errorf("NewFlatFileStore rejected a regular store root: %v", err)
	}
}

func TestSymlinkEscapeBlockedOnWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "TEST-1", Title: "t"}, "tester"); err != nil {
		t.Fatal(err)
	}

	// Git-committed symlink scenario: comments/TEST-1 points outside the
	// store. AddIssueComment must fail and nothing may land outside.
	outside := t.TempDir()
	mustSymlink(t, outside, filepath.Join(s.commentsDir, "TEST-1"))
	if _, err := s.AddIssueComment(ctx, "TEST-1", "attacker", "payload"); err == nil {
		t.Error("AddIssueComment wrote through a symlinked comments dir")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("comment write escaped the store: %d file(s) outside", len(entries))
	}

	// events/TEST-1.jsonl points at a file outside the store. Appending an
	// event must fail and must not modify the target.
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// CreateIssue recorded a creation event, so the events file already
	// exists; remove it so the symlink takes its place — in the checkout
	// threat model the committed link IS what materializes at that path.
	if err := os.Remove(filepath.Join(s.eventsDir, "TEST-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, victim, filepath.Join(s.eventsDir, "TEST-1.jsonl"))
	if err := s.recordEvent("TEST-1", types.EventStatusChanged, "attacker", "a", "b"); err == nil {
		t.Error("appendEvent followed a symlinked events file")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original\n" {
		t.Errorf("event append escaped the store: victim file modified to %q", got)
	}
}
