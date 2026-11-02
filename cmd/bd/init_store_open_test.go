package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// initStoreFake scripts openInitStore's opener and records, in order, every
// open (with the LenientOpen it was asked for), commit and close.
type initStoreFake struct {
	t       *testing.T
	results []initStoreOpenResult // one per open, consumed in order
	events  []string
	configs []dolt.Config // a copy of each config passed to open
}

type initStoreOpenResult struct {
	err       error
	committed bool  // CommitAll's bool for a returned store
	commitErr error // CommitAll's error for a returned store
	closeErr  error // Close's error for a returned store
}

type initStoreFakeStore struct {
	storage.DoltStorage // nil: any method openInitStore must not call panics
	fake                *initStoreFake
	name                string
	result              initStoreOpenResult
}

func (s *initStoreFakeStore) CommitAll(_ context.Context, message string) (bool, error) {
	s.fake.events = append(s.fake.events, "commit "+s.name+": "+message)
	return s.result.committed, s.result.commitErr
}

func (s *initStoreFakeStore) Close() error {
	s.fake.events = append(s.fake.events, "close "+s.name)
	return s.result.closeErr
}

func (f *initStoreFake) open(_ context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	n := len(f.configs)
	f.configs = append(f.configs, *cfg)
	if n >= len(f.results) {
		f.t.Fatalf("open #%d: openInitStore opened more times than the %d scripted", n+1, len(f.results))
	}
	mode := "strict"
	if cfg.LenientOpen {
		mode = "lenient"
	}
	name := fmt.Sprintf("%s#%d", mode, n+1)
	f.events = append(f.events, "open "+name)
	r := f.results[n]
	if r.err != nil {
		return nil, r.err
	}
	return &initStoreFakeStore{fake: f, name: name, result: r}, nil
}

func initStoreTestConfig() *dolt.Config {
	return &dolt.Config{
		Database:        "hq",
		ServerMode:      true,
		ServerHost:      "127.0.0.1",
		ServerPort:      3307,
		CreateIfMissing: true,
		OpenedByInit:    true,
	}
}

func unversionedDirtyRefusal() error {
	return fmt.Errorf("failed to initialize schema: %w", &schema.DirtyTablesError{
		Tables:      []string{"config", "interactions", "routes"},
		Unversioned: true,
	})
}

func runOpenInitStore(t *testing.T, cfg *dolt.Config, results ...initStoreOpenResult) (*initStoreFake, storage.DoltStorage, error) {
	t.Helper()
	fake := &initStoreFake{t: t, results: results}
	store, err := openInitStore(context.Background(), cfg, fake.open)
	return fake, store, err
}

func assertInitStoreEvents(t *testing.T, fake *initStoreFake, want ...string) {
	t.Helper()
	if !slices.Equal(fake.events, want) {
		t.Fatalf("events =\n  %s\nwant\n  %s", strings.Join(fake.events, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestOpenInitStoreReconcilesUnversionedDirtyRefusal pins the adoption
// sequence for gastownhall/beads#6089: strict, lenient, one commit, close of
// the lenient handle, then strict once more, whose store is returned.
func TestOpenInitStoreReconcilesUnversionedDirtyRefusal(t *testing.T) {
	cfg := initStoreTestConfig()
	want := *cfg
	fake, store, err := runOpenInitStore(t, cfg,
		initStoreOpenResult{err: unversionedDirtyRefusal()},
		initStoreOpenResult{committed: true},
		initStoreOpenResult{},
	)
	if err != nil {
		t.Fatalf("openInitStore error = %v, want the final strict open's store", err)
	}
	final, ok := store.(*initStoreFakeStore)
	if !ok || final.name != "strict#3" {
		t.Fatalf("openInitStore store = %#v, want the third (strict) open's store", store)
	}
	assertInitStoreEvents(t, fake,
		"open strict#1",
		"open lenient#2",
		"commit lenient#2: "+initReconcileCommitMessage,
		"close lenient#2",
		"open strict#3",
	)
	// The caller's config is untouched, and only LenientOpen differs on the
	// lenient copy.
	if *cfg != want {
		t.Fatalf("caller config mutated: %+v, want %+v", *cfg, want)
	}
	lenientWant := want
	lenientWant.LenientOpen = true
	if fake.configs[0] != want || fake.configs[1] != lenientWant || fake.configs[2] != want {
		t.Fatalf("open configs = %+v, want strict, strict+LenientOpen, strict", fake.configs)
	}
}

// TestOpenInitStoreNothingToCommitStillReopens covers a lenient open that
// finds nothing committable, as when a concurrent writer committed first:
// CommitAll reports that as (false, nil), and the final strict open still
// runs and decides the result.
func TestOpenInitStoreNothingToCommitStillReopens(t *testing.T) {
	fake, store, err := runOpenInitStore(t, initStoreTestConfig(),
		initStoreOpenResult{err: unversionedDirtyRefusal()},
		initStoreOpenResult{},
		initStoreOpenResult{},
	)
	if err != nil || store == nil {
		t.Fatalf("openInitStore = (%v, %v), want the final strict open's store", store, err)
	}
	assertInitStoreEvents(t, fake,
		"open strict#1",
		"open lenient#2",
		"commit lenient#2: "+initReconcileCommitMessage,
		"close lenient#2",
		"open strict#3",
	)
}

// TestOpenInitStoreCloseErrorDoesNotFailAdoption pins the close convention:
// as at init's other store closes, the lenient handle's close error is
// ignored, and a committed recovery still reopens and succeeds.
func TestOpenInitStoreCloseErrorDoesNotFailAdoption(t *testing.T) {
	fake, store, err := runOpenInitStore(t, initStoreTestConfig(),
		initStoreOpenResult{err: unversionedDirtyRefusal()},
		initStoreOpenResult{committed: true, closeErr: errors.New("close: pool drained")},
		initStoreOpenResult{},
	)
	if err != nil {
		t.Fatalf("openInitStore error = %v, want the final strict open's store", err)
	}
	if final, ok := store.(*initStoreFakeStore); !ok || final.name != "strict#3" {
		t.Fatalf("openInitStore store = %#v, want the third (strict) open's store", store)
	}
	assertInitStoreEvents(t, fake,
		"open strict#1",
		"open lenient#2",
		"commit lenient#2: "+initReconcileCommitMessage,
		"close lenient#2",
		"open strict#3",
	)
}

// TestOpenInitStoreFinalStrictRefusalIsReturned pins that a refusal on the
// last strict open is fatal and passed through as is, with no further open.
func TestOpenInitStoreFinalStrictRefusalIsReturned(t *testing.T) {
	finalErr := fmt.Errorf("failed to initialize schema: %w", &schema.DirtyTablesError{Tables: []string{"issues"}, Unversioned: true})
	fake, store, err := runOpenInitStore(t, initStoreTestConfig(),
		initStoreOpenResult{err: unversionedDirtyRefusal()},
		initStoreOpenResult{committed: true},
		initStoreOpenResult{err: finalErr},
	)
	if store != nil {
		t.Fatalf("openInitStore store = %v, want nil", store)
	}
	if err != finalErr {
		t.Fatalf("openInitStore error = %v, want the final strict open's error unchanged", err)
	}
	if len(fake.configs) != 3 {
		t.Fatalf("opens = %d, want 3", len(fake.configs))
	}
}

// TestOpenInitStoreRecoveryFailureStops pins the failed-recovery contract: the
// error names both the refusal and the recovery failure, the lenient handle
// is closed when the commit fails, and no third open follows.
func TestOpenInitStoreRecoveryFailureStops(t *testing.T) {
	recoveryErr := errors.New("connection reset")
	// Dolt's raw nothing-to-commit error is a failure here too: CommitAll
	// already reports an empty working set as (false, nil).
	nothingErr := errors.New("nothing to commit")
	for _, tc := range []struct {
		name     string
		lenient  initStoreOpenResult
		recovery error
		events   []string
	}{
		{
			name:     "lenient open fails",
			lenient:  initStoreOpenResult{err: recoveryErr},
			recovery: recoveryErr,
			events:   []string{"open strict#1", "open lenient#2"},
		},
		{
			name:     "commit fails",
			lenient:  initStoreOpenResult{commitErr: recoveryErr},
			recovery: recoveryErr,
			events: []string{
				"open strict#1",
				"open lenient#2",
				"commit lenient#2: " + initReconcileCommitMessage,
				"close lenient#2",
			},
		},
		{
			name:     "commit reports nothing to commit as an error",
			lenient:  initStoreOpenResult{commitErr: nothingErr},
			recovery: nothingErr,
			events: []string{
				"open strict#1",
				"open lenient#2",
				"commit lenient#2: " + initReconcileCommitMessage,
				"close lenient#2",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal := unversionedDirtyRefusal()
			fake, store, err := runOpenInitStore(t, initStoreTestConfig(),
				initStoreOpenResult{err: refusal},
				tc.lenient,
			)
			if store != nil {
				t.Fatalf("openInitStore store = %v, want nil", store)
			}
			assertInitStoreEvents(t, fake, tc.events...)
			if err == nil {
				t.Fatal("openInitStore error = nil, want the recovery failure")
			}
			want := refusal.Error() + "; bd init could not commit the pre-existing working set: " + tc.recovery.Error()
			if err.Error() != want {
				t.Fatalf("openInitStore error =\n  %q\nwant\n  %q", err.Error(), want)
			}
			var dirtyErr *schema.DirtyTablesError
			if !errors.As(err, &dirtyErr) || !errors.Is(err, refusal) || !errors.Is(err, tc.recovery) {
				t.Fatalf("openInitStore error %v does not wrap both the original refusal and the recovery error", err)
			}
		})
	}
}
