package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// fakeInitStore is the store half of the openInitStore recorder: it logs the
// commit and close the helper performs, and answers CommitAll with a fixed
// (committed, err) pair. The embedded nil interface panics on any other
// method, which is the point: nothing else on the store is observable here.
type fakeInitStore struct {
	storage.DoltStorage
	events    *[]string
	committed bool
	commitErr error
	closeErr  error
}

func (s *fakeInitStore) CommitAll(_ context.Context, message string) (bool, error) {
	*s.events = append(*s.events, "commit:"+message)
	return s.committed, s.commitErr
}

func (s *fakeInitStore) Close() error {
	*s.events = append(*s.events, "close")
	return s.closeErr
}

// fakeInitOpener is the injected newDoltStore: it logs each open with the
// LenientOpen value of the config it was handed, keeps the exact config
// pointers for identity checks, and answers from a scripted list.
type fakeInitOpener struct {
	events  *[]string
	configs []*dolt.Config
	answers []initOpenAnswer
}

type initOpenAnswer struct {
	store *fakeInitStore
	err   error
}

func (o *fakeInitOpener) open(_ context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	i := len(o.configs)
	o.configs = append(o.configs, cfg)
	*o.events = append(*o.events, fmt.Sprintf("open:lenient=%v", cfg.LenientOpen))
	if i >= len(o.answers) {
		return nil, fmt.Errorf("unexpected open call %d", i+1)
	}
	a := o.answers[i]
	if a.store == nil && a.err == nil {
		a.store = &fakeInitStore{events: o.events}
	}
	if a.store != nil {
		return a.store, nil
	}
	return nil, a.err
}

// eligibleUnversionedRefusal is the #6089 shape: the strict open of a
// server-mode unversioned database refused by the #4566 guard, wrapped the
// way dolt.New wraps it ("failed to initialize schema: %w").
func eligibleUnversionedRefusal() error {
	return fmt.Errorf("failed to initialize schema: %w", &schema.DirtyTablesError{
		Tables:      []string{"config", "interactions", "routes"},
		Unversioned: true,
	})
}

// serverInitConfig is a minimal server-mode init config: ServerMode is what
// routes newDoltStore to dolt.New, and Database names the target.
func serverInitConfig() *dolt.Config {
	return &dolt.Config{ServerMode: true, Database: "legacy_residue"}
}

func unversionedRefusalFrom(err error) *schema.DirtyTablesError {
	var refusal *schema.DirtyTablesError
	if !errors.As(err, &refusal) {
		return nil
	}
	return refusal
}

// TestOpenInitStorePassesThroughFirstOpenSuccess pins the no-recovery
// default: a clean strict open is returned as-is, with no second open, no
// commit and no note.
func TestOpenInitStorePassesThroughFirstOpenSuccess(t *testing.T) {
	var events []string
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{{}}}
	var notes []string
	store, err := openInitStore(context.Background(), serverInitConfig(), opener.open, func(line string) { notes = append(notes, line) })
	if err != nil {
		t.Fatalf("openInitStore: %v", err)
	}
	if store == nil {
		t.Fatal("openInitStore returned a nil store for a successful strict open")
	}
	if want := []string{"open:lenient=false"}; strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
}

// TestOpenInitStorePassesThroughIneligibleError pins that an error the
// adoption policy does not recognize is returned unchanged, with no
// reconciliation attempt.
func TestOpenInitStorePassesThroughIneligibleError(t *testing.T) {
	boom := fmt.Errorf("failed to open Dolt store: connection refused")
	var events []string
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{{err: boom}}}
	store, err := openInitStore(context.Background(), serverInitConfig(), opener.open, nil)
	if store != nil {
		t.Fatal("openInitStore returned a store alongside an error")
	}
	if err != boom {
		t.Fatalf("openInitStore error = %v, want %v unchanged", err, boom)
	}
	if want := []string{"open:lenient=false"}; strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

// TestOpenInitStoreKeepsVersionedDirtyRefusal pins the dirty-data guard: a
// versioned dirty database's refusal (Unversioned=false — a recorded main
// history exists) is returned unchanged, with its rows left uncommitted.
func TestOpenInitStoreKeepsVersionedDirtyRefusal(t *testing.T) {
	refusal := &schema.DirtyTablesError{Tables: []string{"issues"}, Unversioned: false}
	var events []string
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: fmt.Errorf("failed to initialize schema: %w", refusal)},
	}}
	var notes []string
	_, err := openInitStore(context.Background(), serverInitConfig(), opener.open, func(line string) { notes = append(notes, line) })
	if err == nil {
		t.Fatal("openInitStore error = nil, want the versioned refusal")
	}
	if got := unversionedRefusalFrom(err); got != refusal {
		t.Fatalf("openInitStore error chain lost the refusal: %v", err)
	}
	if strings.Contains(fmt.Sprint(events), "commit:") {
		t.Fatalf("versioned refusal was reconciled: %v", events)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
}

// TestOpenInitStoreExcludesIneligibleConfigurations pins that only a plain
// writable server-mode configuration gains adoption authority: embedded
// (ServerMode false), gateway, read-only, preview and proxied opens see the
// unversioned refusal returned unchanged, without a commit attempt.
func TestOpenInitStoreExcludesIneligibleConfigurations(t *testing.T) {
	for name, mutate := range map[string]func(*dolt.Config){
		"embedded (ServerMode false)": func(c *dolt.Config) { c.ServerMode = false },
		"gateway":                     func(c *dolt.Config) { c.Gateway = true },
		"read-only":                   func(c *dolt.Config) { c.ReadOnly = true },
		"preview":                     func(c *dolt.Config) { c.Preview = true },
		"proxied server":              func(c *dolt.Config) { c.ProxiedServer = true },
	} {
		t.Run(name, func(t *testing.T) {
			var events []string
			opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
				{err: eligibleUnversionedRefusal()},
			}}
			cfg := serverInitConfig()
			mutate(cfg)
			_, err := openInitStore(context.Background(), cfg, opener.open, nil)
			if unversionedRefusalFrom(err) == nil {
				t.Fatalf("openInitStore error = %v, want the refusal passed through", err)
			}
			if len(opener.configs) != 1 {
				t.Fatalf("opens = %d, want 1 (no lenient retry)", len(opener.configs))
			}
			if strings.Contains(strings.Join(events, "|"), "commit:") {
				t.Fatalf("ineligible configuration was reconciled: %v", events)
			}
		})
	}
}

// TestOpenInitStoreClassifiesWrappedRefusal pins the classifier directly:
// dolt.New returns the refusal wrapped ("failed to initialize schema: %w"),
// so initStoreAdoptionEligible must see through the chain — true only for
// the server-mode writable configuration AND an Unversioned main-source
// refusal.
func TestOpenInitStoreClassifiesWrappedRefusal(t *testing.T) {
	if !initStoreAdoptionEligible(serverInitConfig(), eligibleUnversionedRefusal()) {
		t.Fatal("initStoreAdoptionEligible = false for a wrapped unversioned server refusal, want true")
	}
	refusal := &schema.DirtyTablesError{Tables: []string{"issues"}}
	if initStoreAdoptionEligible(serverInitConfig(), fmt.Errorf("failed to initialize schema: %w", refusal)) {
		t.Fatal("initStoreAdoptionEligible = true for a versioned refusal, want false")
	}
	if initStoreAdoptionEligible(serverInitConfig(), nil) {
		t.Fatal("initStoreAdoptionEligible = true for a nil error, want false")
	}
}
