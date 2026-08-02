//go:build cgo

package embeddeddolt_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

type pristineConformanceTemplate struct {
	beadsDir string
}

func requireEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
}

func newPristineConformanceTemplate(t *testing.T) pristineConformanceTemplate {
	t.Helper()
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "test", "main")
	if err != nil {
		t.Fatalf("open pristine conformance template: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("configure pristine conformance template: %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("commit pristine conformance template: %v", err)
	}
	closeEmbeddedDoltStore(t, store)
	return pristineConformanceTemplate{beadsDir: beadsDir}
}

func clonePristineConformanceTemplate(template pristineConformanceTemplate, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("reserve clone destination %q: %w", destination, err)
	}
	if err := os.CopyFS(destination, os.DirFS(template.beadsDir)); err != nil {
		return fmt.Errorf("copy pristine conformance template to %q: %w", destination, err)
	}
	return nil
}

func openPristineConformanceClone(t *testing.T, beadsDir string) *embeddeddolt.EmbeddedDoltStore {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), beadsDir, "test", "main")
	if err != nil {
		t.Fatalf("open pristine conformance clone %q: %v", beadsDir, err)
	}
	return store
}

func closeEmbeddedDoltStore(t *testing.T, store *embeddeddolt.EmbeddedDoltStore) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("close embedded Dolt store: %v", err)
	}
}

func directoryDigest(t *testing.T, root string) string {
	t.Helper()
	digest := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(digest, relative+"\\x00"); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := digest.Write(contents); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("digest %q: %v", root, err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func TestPristineConformanceTemplateClonesAreIndependent(t *testing.T) {
	requireEmbeddedDolt(t)

	template := newPristineConformanceTemplate(t)
	before := directoryDigest(t, template.beadsDir)

	cloneAPath := filepath.Join(t.TempDir(), ".beads")
	if err := clonePristineConformanceTemplate(template, cloneAPath); err != nil {
		t.Fatalf("clone A: %v", err)
	}
	cloneBPath := filepath.Join(t.TempDir(), ".beads")
	if err := clonePristineConformanceTemplate(template, cloneBPath); err != nil {
		t.Fatalf("clone B: %v", err)
	}
	if err := clonePristineConformanceTemplate(template, cloneAPath); err == nil {
		t.Fatal("clone into an existing destination succeeded")
	}

	cloneA := openPristineConformanceClone(t, cloneAPath)
	if err := cloneA.CreateIssue(t.Context(), &types.Issue{
		ID:        "test-1",
		Title:     "only clone A has this issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create in clone A: %v", err)
	}
	if err := cloneA.Commit(t.Context(), "mutate clone A"); err != nil {
		t.Fatalf("commit clone A: %v", err)
	}
	closeEmbeddedDoltStore(t, cloneA)

	if got := directoryDigest(t, template.beadsDir); got != before {
		t.Fatalf("template digest changed after clone A mutation: got %s, want %s", got, before)
	}

	cloneB := openPristineConformanceClone(t, cloneBPath)
	assertEmptyPristineConformanceStore(t, cloneB, "clone B")
	closeEmbeddedDoltStore(t, cloneB)

	templateStore := openPristineConformanceClone(t, template.beadsDir)
	assertEmptyPristineConformanceStore(t, templateStore, "template")
	closeEmbeddedDoltStore(t, templateStore)
	if got := directoryDigest(t, template.beadsDir); got != before {
		t.Fatalf("template digest changed after reopen: got %s, want %s", got, before)
	}

	cloneA = openPristineConformanceClone(t, cloneAPath)
	if _, err := cloneA.GetIssue(t.Context(), "test-1"); err != nil {
		t.Fatalf("reopened clone A GetIssue(test-1): %v", err)
	}
	closeEmbeddedDoltStore(t, cloneA)

	cloneB = openPristineConformanceClone(t, cloneBPath)
	if _, err := cloneB.GetIssue(t.Context(), "test-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("reopened clone B GetIssue(test-1) error = %v, want ErrNotFound", err)
	}
	closeEmbeddedDoltStore(t, cloneB)
}

func assertEmptyPristineConformanceStore(t *testing.T, store storage.DoltStorage, name string) {
	t.Helper()
	count, err := store.CountIssues(t.Context(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("count issues in %s: %v", name, err)
	}
	if count != 0 {
		t.Fatalf("%s has %d issues, want empty store", name, count)
	}
}

// TestConformance runs the backend-agnostic storage conformance suite
// (internal/storage/conformance) against the embedded Dolt backend, so the
// storage contract is enforced against a real implementation rather than only
// asserted. The factory returns a fresh, empty in-process store per sub-test.
func TestConformance(t *testing.T) {
	requireEmbeddedDolt(t)
	template := newPristineConformanceTemplate(t)

	conformance.RunAll(t, func(t *testing.T) storage.DoltStorage {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := clonePristineConformanceTemplate(template, beadsDir); err != nil {
			t.Fatalf("clone pristine conformance template: %v", err)
		}
		store := openPristineConformanceClone(t, beadsDir)
		t.Cleanup(func() { closeEmbeddedDoltStore(t, store) })
		return store
	})
}
