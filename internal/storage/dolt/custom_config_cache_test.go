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
	"github.com/steveyegge/beads/internal/types"
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

func assertCustomStatuses(t *testing.T, got []types.CustomStatus, want []types.CustomStatus) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("statuses = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statuses = %+v, want %+v", got, want)
		}
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
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

	// AC-2: with the vocabulary declared in config.yaml, a failed resolve
	// serves the parsed YAML with a nil error — and does not cache it.
	t.Run("failed resolve with yaml serves fallback without caching", func(t *testing.T) {
		withCustomVocabularyYAML(t, "review:active,qa", "spike,yaml-only,spike")
		store, mock := newCustomConfigStore(t)
		ctx := context.Background()

		mock.ExpectBegin().WillReturnError(cause)
		mock.ExpectBegin().WillReturnError(cause)
		mock.ExpectBegin().WillReturnError(cause)

		names, err := store.GetCustomStatuses(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatuses with yaml fallback: %v", err)
		}
		assertStrings(t, names, []string{"review", "qa"})
		detailed, err := store.GetCustomStatusesDetailed(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatusesDetailed with yaml fallback: %v", err)
		}
		assertCustomStatuses(t, detailed, []types.CustomStatus{
			{Name: "review", Category: types.CategoryActive},
			{Name: "qa", Category: types.CategoryUnspecified},
		})
		gotTypes, err := store.GetCustomTypes(ctx)
		if err != nil {
			t.Fatalf("GetCustomTypes with yaml fallback: %v", err)
		}
		assertStrings(t, gotTypes, []string{"spike", "yaml-only"})

		if statusCached, typeCached := cacheFlagsSet(t, store); statusCached || typeCached {
			t.Fatalf("yaml fallback was cached: statuses=%v, types=%v", statusCached, typeCached)
		}
	})

	// AC-2: the fallback is per getter: YAML statuses do not mask a failed
	// type resolve, and YAML types do not mask a failed status resolve.
	t.Run("failed resolve with only status yaml errors for types", func(t *testing.T) {
		withCustomVocabularyYAML(t, "review:active", "")
		store, mock := newCustomConfigStore(t)
		ctx := context.Background()

		mock.ExpectBegin().WillReturnError(cause)
		mock.ExpectBegin().WillReturnError(cause)

		names, err := store.GetCustomStatuses(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatuses with status yaml fallback: %v", err)
		}
		assertStrings(t, names, []string{"review"})
		_, err = store.GetCustomTypes(ctx)
		requireErrIs(t, err, cause, "GetCustomTypes with no type yaml fallback")
	})

	t.Run("failed resolve with only types yaml errors for statuses", func(t *testing.T) {
		withCustomVocabularyYAML(t, "", "spike")
		store, mock := newCustomConfigStore(t)
		ctx := context.Background()

		mock.ExpectBegin().WillReturnError(cause)
		mock.ExpectBegin().WillReturnError(cause)

		_, err := store.GetCustomStatuses(ctx)
		requireErrIs(t, err, cause, "GetCustomStatuses with no status yaml fallback")
		gotTypes, err := store.GetCustomTypes(ctx)
		if err != nil {
			t.Fatalf("GetCustomTypes with type yaml fallback: %v", err)
		}
		assertStrings(t, gotTypes, []string{"spike"})
	})
}
