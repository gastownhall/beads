//go:build cgo

package embeddeddolt_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

var embeddedDoltFixtureRoot string

const pristineEmbeddedDoltDatabase = "test"

var (
	pristineEmbeddedDoltTemplateOnce  sync.Once
	pristineEmbeddedDoltTemplateValue pristineEmbeddedDoltTemplate
	pristineEmbeddedDoltTemplateErr   error
)

type pristineEmbeddedDoltTemplate struct {
	beadsDir string
	dataDir  string
}

type pristineEmbeddedDoltFixture struct {
	store    *embeddeddolt.EmbeddedDoltStore
	beadsDir string
	dataDir  string
	database string
}

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "embeddeddolt-test-fixtures-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create embedded Dolt test fixture root:", err)
		os.Exit(1)
	}
	embeddedDoltFixtureRoot = root

	code := m.Run()
	if err := os.RemoveAll(root); err != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "remove embedded Dolt test fixture root:", err)
		code = 1
	}
	os.Exit(code)
}

func pristineEmbeddedDoltTemplateForTest(t *testing.T) pristineEmbeddedDoltTemplate {
	t.Helper()
	pristineEmbeddedDoltTemplateOnce.Do(func() {
		beadsDir := filepath.Join(embeddedDoltFixtureRoot, ".beads")
		store, err := embeddeddolt.Open(context.Background(), beadsDir, pristineEmbeddedDoltDatabase, "main")
		if err != nil {
			pristineEmbeddedDoltTemplateErr = fmt.Errorf("open pristine embedded Dolt template: %w", err)
			return
		}
		if err := store.SetConfig(context.Background(), "issue_prefix", pristineEmbeddedDoltDatabase); err != nil {
			if closeErr := store.Close(); closeErr != nil {
				pristineEmbeddedDoltTemplateErr = fmt.Errorf("configure pristine embedded Dolt template: %w", errors.Join(err, closeErr))
				return
			}
			pristineEmbeddedDoltTemplateErr = fmt.Errorf("configure pristine embedded Dolt template: %w", err)
			return
		}
		if err := store.Commit(context.Background(), "bd init"); err != nil {
			if closeErr := store.Close(); closeErr != nil {
				pristineEmbeddedDoltTemplateErr = fmt.Errorf("commit pristine embedded Dolt template: %w", errors.Join(err, closeErr))
				return
			}
			pristineEmbeddedDoltTemplateErr = fmt.Errorf("commit pristine embedded Dolt template: %w", err)
			return
		}
		if err := store.Close(); err != nil {
			pristineEmbeddedDoltTemplateErr = fmt.Errorf("close pristine embedded Dolt template: %w", err)
			return
		}
		pristineEmbeddedDoltTemplateValue = pristineEmbeddedDoltTemplate{
			beadsDir: beadsDir,
			dataDir:  filepath.Join(beadsDir, "embeddeddolt"),
		}
	})
	if pristineEmbeddedDoltTemplateErr != nil {
		t.Fatal(pristineEmbeddedDoltTemplateErr)
	}
	return pristineEmbeddedDoltTemplateValue
}

func clonePristineEmbeddedDoltTemplate(template pristineEmbeddedDoltTemplate, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("reserve clone destination %q: %w", destination, err)
	}
	if err := os.CopyFS(destination, os.DirFS(template.beadsDir)); err != nil {
		return fmt.Errorf("copy pristine embedded Dolt template to %q: %w", destination, err)
	}
	return nil
}

func newPristineEmbeddedDoltFixture(t *testing.T, database string) *pristineEmbeddedDoltFixture {
	t.Helper()
	template := pristineEmbeddedDoltTemplateForTest(t)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := clonePristineEmbeddedDoltTemplate(template, beadsDir); err != nil {
		t.Fatalf("clone pristine embedded Dolt template: %v", err)
	}
	fixture := &pristineEmbeddedDoltFixture{
		beadsDir: beadsDir,
		dataDir:  filepath.Join(beadsDir, "embeddeddolt"),
		database: database,
	}
	if database != pristineEmbeddedDoltDatabase {
		if err := relocatePristineEmbeddedDoltDatabase(fixture.dataDir, database); err != nil {
			t.Fatalf("relocate pristine embedded Dolt database to %q: %v", database, err)
		}
	}
	store, err := embeddeddolt.Open(t.Context(), fixture.beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open pristine embedded Dolt clone: %v", err)
	}
	fixture.store = store
	if database != pristineEmbeddedDoltDatabase {
		if err := store.SetConfig(t.Context(), "issue_prefix", database); err != nil {
			if closeErr := store.Close(); closeErr != nil {
				t.Fatalf("configure relocated embedded Dolt clone: %v; close: %v", err, closeErr)
			}
			t.Fatalf("configure relocated embedded Dolt clone: %v", err)
		}
		if err := store.Commit(t.Context(), "bd init"); err != nil {
			if closeErr := store.Close(); closeErr != nil {
				t.Fatalf("commit relocated embedded Dolt clone: %v; close: %v", err, closeErr)
			}
			t.Fatalf("commit relocated embedded Dolt clone: %v", err)
		}
	}
	return fixture
}

func relocatePristineEmbeddedDoltDatabase(dataDir, database string) error {
	if filepath.Base(database) != database || database == "." || database == ".." {
		return fmt.Errorf("database name %q is not a single path element", database)
	}
	source := filepath.Join(dataDir, pristineEmbeddedDoltDatabase)
	destination := filepath.Join(dataDir, database)
	if err := os.Rename(source, destination); err != nil {
		return fmt.Errorf("rename %q to %q: %w", source, destination, err)
	}
	return nil
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

func TestPristineEmbeddedDoltFixtureRelocatesPrefixes(t *testing.T) {
	template := pristineEmbeddedDoltTemplateForTest(t)
	templateDigest := directoryDigest(t, template.beadsDir)

	fast := newPristineEmbeddedDoltFixture(t, "test")
	if got := directoryDigest(t, fast.beadsDir); got != templateDigest {
		t.Fatalf("test-prefix clone digest = %s, want pristine template %s", got, templateDigest)
	}
	closeEmbeddedDoltStore(t, fast.store)

	alpha := newPristineEmbeddedDoltFixture(t, "alpha")
	beta := newPristineEmbeddedDoltFixture(t, "beta")
	for _, fixture := range []*pristineEmbeddedDoltFixture{alpha, beta} {
		if _, err := os.Stat(filepath.Join(fixture.dataDir, fixture.database)); err != nil {
			t.Fatalf("%s physical database directory: %v", fixture.database, err)
		}
		if _, err := os.Stat(filepath.Join(fixture.dataDir, pristineEmbeddedDoltDatabase)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s clone still contains baseline database directory: %v", fixture.database, err)
		}
		prefix, err := fixture.store.GetConfig(t.Context(), "issue_prefix")
		if err != nil || prefix != fixture.database {
			t.Fatalf("%s issue_prefix = %q, %v; want %q", fixture.database, prefix, err, fixture.database)
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), fixture.dataDir, fixture.database, "main")
		if err != nil {
			t.Fatalf("open raw SQL for %s: %v", fixture.database, err)
		}
		var rawPrefix string
		err = db.QueryRowContext(t.Context(), "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&rawPrefix)
		if cleanupErr := cleanup(); cleanupErr != nil && err == nil {
			err = cleanupErr
		}
		if err != nil || rawPrefix != fixture.database {
			t.Fatalf("raw issue_prefix for %s = %q, %v; want %q", fixture.database, rawPrefix, err, fixture.database)
		}
	}

	issue := &types.Issue{Title: "only alpha has this", Status: types.StatusOpen, IssueType: types.TypeTask}
	if err := alpha.store.CreateIssue(t.Context(), issue, "test"); err != nil {
		t.Fatalf("create alpha issue: %v", err)
	}
	if !strings.HasPrefix(issue.ID, "alpha-") {
		t.Fatalf("generated alpha issue ID = %q, want alpha prefix", issue.ID)
	}
	if err := alpha.store.Commit(t.Context(), "commit alpha issue"); err != nil {
		t.Fatalf("commit alpha issue: %v", err)
	}
	if _, err := beta.store.GetIssue(t.Context(), issue.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("beta GetIssue(%q) error = %v, want ErrNotFound", issue.ID, err)
	}
	closeEmbeddedDoltStore(t, alpha.store)
	closeEmbeddedDoltStore(t, beta.store)

	alphaReopened, err := embeddeddolt.Open(context.Background(), alpha.beadsDir, "alpha", "main")
	if err != nil {
		t.Fatalf("reopen alpha: %v", err)
	}
	if _, err := alphaReopened.GetIssue(t.Context(), issue.ID); err != nil {
		t.Fatalf("reopened alpha GetIssue(%q): %v", issue.ID, err)
	}
	closeEmbeddedDoltStore(t, alphaReopened)

	betaReopened, err := embeddeddolt.Open(context.Background(), beta.beadsDir, "beta", "main")
	if err != nil {
		t.Fatalf("reopen beta: %v", err)
	}
	if _, err := betaReopened.GetIssue(t.Context(), issue.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("reopened beta GetIssue(%q) error = %v, want ErrNotFound", issue.ID, err)
	}
	closeEmbeddedDoltStore(t, betaReopened)

	if got := directoryDigest(t, template.beadsDir); got != templateDigest {
		t.Fatalf("template digest changed after relocated clone mutations: got %s, want %s", got, templateDigest)
	}

	reusedDestination := filepath.Join(t.TempDir(), ".beads")
	if err := clonePristineEmbeddedDoltTemplate(template, reusedDestination); err != nil {
		t.Fatalf("first clone into reusable destination: %v", err)
	}
	if err := clonePristineEmbeddedDoltTemplate(template, reusedDestination); err == nil {
		t.Fatal("clone into an existing destination succeeded")
	}
}
