package backends_test

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

var errFixtureOpen = errors.New("fixture backend open")

func fixtureBackend() backends.Backend {
	open := func(context.Context, string) (storage.DoltStorage, error) {
		return nil, errFixtureOpen
	}
	return backends.Backend{Open: open, OpenReadOnly: open}
}

func TestRegisterLookupAndDeregister(t *testing.T) {
	const name = "fixture"
	backends.Register(name, fixtureBackend())
	t.Cleanup(func() { backends.Deregister(name) })

	registered, ok := backends.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) did not find the registered backend", name)
	}
	if _, err := registered.Open(t.Context(), t.TempDir()); !errors.Is(err, errFixtureOpen) {
		t.Fatalf("Open() error = %v, want %v", err, errFixtureOpen)
	}
	if !backends.Registered(name) {
		t.Fatalf("Registered(%q) = false, want true", name)
	}
	if !backends.Deregister(name) {
		t.Fatalf("Deregister(%q) = false, want true", name)
	}
	if _, ok := backends.Lookup(name); ok {
		t.Fatalf("Lookup(%q) found a deregistered backend", name)
	}
	if backends.Deregister(name) {
		t.Fatalf("second Deregister(%q) = true, want false", name)
	}
}

func TestRegisterRejectsInvalidContracts(t *testing.T) {
	mustPanic := func(t *testing.T, register func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatal("Register did not panic")
			}
		}()
		register()
	}

	t.Run("empty name", func(t *testing.T) {
		mustPanic(t, func() { backends.Register("", fixtureBackend()) })
	})
	t.Run("reserved dolt name", func(t *testing.T) {
		mustPanic(t, func() { backends.Register("dolt", fixtureBackend()) })
	})
	t.Run("missing open", func(t *testing.T) {
		mustPanic(t, func() {
			backends.Register("missing-open", backends.Backend{
				OpenReadOnly: fixtureBackend().OpenReadOnly,
			})
		})
	})
	t.Run("missing read-only open", func(t *testing.T) {
		mustPanic(t, func() {
			backends.Register("missing-read-only", backends.Backend{
				Open: fixtureBackend().Open,
			})
		})
	})
	t.Run("duplicate name", func(t *testing.T) {
		const name = "duplicate"
		backends.Register(name, fixtureBackend())
		t.Cleanup(func() { backends.Deregister(name) })
		mustPanic(t, func() { backends.Register(name, fixtureBackend()) })
	})
}
