package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/config"
)

// bd-2e1byb (GH#5581): the custom vocabulary getters must never cache a
// degraded answer. A failed database resolve surfaces as an error (or a
// per-call, uncached config.yaml fallback), and the next call retries; only a
// successful resolution populates the cache, which SetConfig invalidates.

// newCustomConfigStore returns a store over a fresh sqlmock database that
// verifies every queued SQL expectation when the test ends, so a subtest that
// reads when it should have served from cache (or vice versa) fails on the
// unmet expectation.
func newCustomConfigStore(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet SQL expectations: %v", err)
		}
	})
	return &DoltStore{db: db}, mock
}

// withCustomVocabularyYAML isolates the process config with a throwaway
// .beads/config.yaml declaring status.custom and/or types.custom, with HOME
// and XDG_CONFIG_HOME redirected so no user-level config can leak in. The
// config package is process-global, so these tests never run in parallel.
func withCustomVocabularyYAML(t *testing.T, statusCSV, typesCSV string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("BEADS_DIR", "")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	var b strings.Builder
	if statusCSV != "" {
		fmt.Fprintf(&b, "status:\n  custom: %q\n", statusCSV)
	}
	if typesCSV != "" {
		fmt.Fprintf(&b, "types:\n  custom: %q\n", typesCSV)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Chdir(dir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

func requireErrIs(t *testing.T, err error, cause error, where string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: got nil error, want %v", where, cause)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("%s: err = %v, want errors.Is(err, %v)", where, err, cause)
	}
}

func cacheFlagsSet(t *testing.T, store *DoltStore) (statusCached, typeCached bool) {
	t.Helper()
	store.cacheMu.Lock()
	defer store.cacheMu.Unlock()
	return store.customStatusCached, store.customTypeCached
}

func TestCustomConfigGettersDoNotCacheFailedResolve(t *testing.T) {
	cause := errors.New("simulated read failure")

	// AC-1: with no usable YAML fallback, every getter surfaces the resolve
	// error and leaves the cache unpopulated, so the next call retries.
	t.Run("failed resolve without yaml returns the cause and does not cache", func(t *testing.T) {
		withCustomVocabularyYAML(t, "", "")
		store, mock := newCustomConfigStore(t)
		ctx := context.Background()

		mock.ExpectBegin().WillReturnError(cause)
		mock.ExpectBegin().WillReturnError(cause)
		mock.ExpectBegin().WillReturnError(cause)

		names, err := store.GetCustomStatuses(ctx)
		requireErrIs(t, err, cause, "GetCustomStatuses after a failed resolve")
		if len(names) != 0 {
			t.Fatalf("GetCustomStatuses = %v, want no vocabulary", names)
		}
		detailed, err := store.GetCustomStatusesDetailed(ctx)
		requireErrIs(t, err, cause, "GetCustomStatusesDetailed after a failed resolve")
		if len(detailed) != 0 {
			t.Fatalf("GetCustomStatusesDetailed = %v, want no vocabulary", detailed)
		}
		gotTypes, err := store.GetCustomTypes(ctx)
		requireErrIs(t, err, cause, "GetCustomTypes after a failed resolve")
		if len(gotTypes) != 0 {
			t.Fatalf("GetCustomTypes = %v, want no vocabulary", gotTypes)
		}

		if statusCached, typeCached := cacheFlagsSet(t, store); statusCached || typeCached {
			t.Fatalf("failed resolve was cached: statuses=%v, types=%v", statusCached, typeCached)
		}
	})
}
