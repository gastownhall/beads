package backends_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/backends"
)

// TestCreateIsOptional holds the Create hook to its contract: a backend that
// leaves it nil can be looked up and opened as before but cannot provision a
// workspace, and Create on such a backend or on an unregistered name fails
// with an error that names the backend instead of touching the directory.
func TestCreateIsOptional(t *testing.T) {
	const name = "fixture-open-only"
	backends.Register(name, fixtureBackend())
	t.Cleanup(func() { backends.Deregister(name) })

	if backends.CanCreate(name) {
		t.Fatalf("CanCreate(%q) = true for a backend without a Create hook", name)
	}
	err := backends.Create(context.Background(), name, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "cannot create") {
		t.Fatalf("Create(%q) error = %v, want a refusal naming the backend", name, err)
	}

	if backends.CanCreate("never-registered") {
		t.Fatal(`CanCreate("never-registered") = true`)
	}
	err = backends.Create(context.Background(), "never-registered", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf(`Create("never-registered") error = %v, want a not-registered error`, err)
	}
}

// TestCreateDispatchesToTheBackend checks that Create reaches the registered
// hook with the workspace directory it was given and returns the hook's error
// unwrapped enough for errors.Is.
func TestCreateDispatchesToTheBackend(t *testing.T) {
	const name = "fixture-creatable"
	errCreate := errors.New("fixture create")
	var gotDir string
	backend := fixtureBackend()
	backend.Create = func(_ context.Context, beadsDir string) error {
		gotDir = beadsDir
		return errCreate
	}
	backends.Register(name, backend)
	t.Cleanup(func() { backends.Deregister(name) })

	if !backends.CanCreate(name) {
		t.Fatalf("CanCreate(%q) = false for a backend with a Create hook", name)
	}
	dir := t.TempDir()
	if err := backends.Create(context.Background(), name, dir); !errors.Is(err, errCreate) {
		t.Fatalf("Create(%q) error = %v, want %v", name, err, errCreate)
	}
	if gotDir != dir {
		t.Fatalf("Create hook got beadsDir %q, want %q", gotDir, dir)
	}
}
