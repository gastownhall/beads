package backends_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// fixtureCredential is a minimal backends.Credential implementation for
// tests: a bare marker with no fields, since this package never inspects a
// Credential's shape.
type fixtureCredential struct{}

func (fixtureCredential) BackendCredential() {}

var errFixtureOpenWith = errors.New("fixture backend OpenWith")

// openWithCapturingBackend returns a Backend whose OpenWith records the
// beadsDir and opts it was called with (via the returned pointers) and a
// plain Open that errors if dispatch ever falls back to it despite OpenWith
// being set — proving OpenWithOptions prefers OpenWith when present.
func openWithCapturingBackend(gotBeadsDir *string, gotOpts *backends.OpenOptions, calls *int) backends.Backend {
	open := func(context.Context, string) (storage.DoltStorage, error) {
		return nil, errors.New("fixture: Open called despite backend having OpenWith")
	}
	return backends.Backend{
		Open:         open,
		OpenReadOnly: open,
		OpenWith: func(_ context.Context, beadsDir string, opts backends.OpenOptions) (storage.DoltStorage, error) {
			*calls++
			*gotBeadsDir = beadsDir
			*gotOpts = opts
			return nil, errFixtureOpenWith
		},
	}
}

func TestBackendOpenWithOptionsPrefersOpenWithAndPassesOptsThrough(t *testing.T) {
	var gotBeadsDir string
	var gotOpts backends.OpenOptions
	var calls int
	backend := openWithCapturingBackend(&gotBeadsDir, &gotOpts, &calls)

	cred := fixtureCredential{}
	client := &http.Client{}
	wantOpts := backends.OpenOptions{Credential: cred, HTTPClient: client, UserAgent: "test-agent/1.0"}

	_, err := backend.OpenWithOptions(t.Context(), "/tmp/fixture-beads-dir", wantOpts)
	if !errors.Is(err, errFixtureOpenWith) {
		t.Fatalf("OpenWithOptions error = %v, want %v", err, errFixtureOpenWith)
	}
	if calls != 1 {
		t.Fatalf("OpenWith called %d times, want 1", calls)
	}
	if gotBeadsDir != "/tmp/fixture-beads-dir" {
		t.Fatalf("OpenWith beadsDir = %q, want /tmp/fixture-beads-dir", gotBeadsDir)
	}
	if gotOpts.Credential != cred {
		t.Fatalf("OpenWith opts.Credential = %v, want %v", gotOpts.Credential, cred)
	}
	if gotOpts.HTTPClient != client {
		t.Fatalf("OpenWith opts.HTTPClient = %v, want %v", gotOpts.HTTPClient, client)
	}
	if gotOpts.UserAgent != "test-agent/1.0" {
		t.Fatalf("OpenWith opts.UserAgent = %q, want test-agent/1.0", gotOpts.UserAgent)
	}
}

// TestBackendOpenWithOptionsZeroOptionsBackCompat covers back-compat for
// EVERY backend shape OSS and downstream registrants can produce: a backend
// with no OpenWith (the historical, Open-only shape) and a backend WITH
// OpenWith (the new optional seam) must both behave identically to calling
// Open directly when opts is the zero value.
func TestBackendOpenWithOptionsZeroOptionsBackCompat(t *testing.T) {
	t.Run("backend without OpenWith falls back to Open", func(t *testing.T) {
		backend := fixtureBackend() // from backends_test.go: Open/OpenReadOnly only, no OpenWith
		_, err := backend.OpenWithOptions(t.Context(), t.TempDir(), backends.OpenOptions{})
		if !errors.Is(err, errFixtureOpen) {
			t.Fatalf("OpenWithOptions(zero opts) error = %v, want %v (the Open sentinel)", err, errFixtureOpen)
		}
	})

	t.Run("backend with OpenWith still receives the call with a zero OpenOptions", func(t *testing.T) {
		var gotBeadsDir string
		var gotOpts backends.OpenOptions
		var calls int
		backend := openWithCapturingBackend(&gotBeadsDir, &gotOpts, &calls)

		dir := t.TempDir()
		_, err := backend.OpenWithOptions(t.Context(), dir, backends.OpenOptions{})
		if !errors.Is(err, errFixtureOpenWith) {
			t.Fatalf("OpenWithOptions(zero opts) error = %v, want %v", err, errFixtureOpenWith)
		}
		if calls != 1 {
			t.Fatalf("OpenWith called %d times, want 1", calls)
		}
		if gotOpts != (backends.OpenOptions{}) {
			t.Fatalf("OpenWith opts = %+v, want the zero value", gotOpts)
		}
	})
}

// TestBackendOpenWithOptionsRefusesCredentialWithoutOpenWith is the design's
// explicit requirement: a backend with no OpenWith seam must refuse a
// non-nil Credential rather than silently opening unauthenticated and
// discarding it.
func TestBackendOpenWithOptionsRefusesCredentialWithoutOpenWith(t *testing.T) {
	backend := fixtureBackend() // Open/OpenReadOnly only, no OpenWith

	store, err := backend.OpenWithOptions(t.Context(), t.TempDir(), backends.OpenOptions{
		Credential: fixtureCredential{},
	})
	if store != nil {
		t.Fatal("OpenWithOptions returned a non-nil store alongside the refusal")
	}
	if !errors.Is(err, backends.ErrCredentialWithoutOpenWith) {
		t.Fatalf("OpenWithOptions error = %v, want %v", err, backends.ErrCredentialWithoutOpenWith)
	}
}

// TestBackendOpenWithOptionsRefusesHTTPClientAndUserAgentWithoutOpenWith is
// the L2 fail-closed rule: a backend with no OpenWith seam must refuse a
// non-nil HTTPClient or non-empty UserAgent the same way it refuses a
// non-nil Credential, rather than silently falling back to Open and
// dropping the override — a caller that asked for a specific transport or
// User-Agent must be told that request was not honored.
func TestBackendOpenWithOptionsRefusesHTTPClientAndUserAgentWithoutOpenWith(t *testing.T) {
	t.Run("HTTPClient alone", func(t *testing.T) {
		backend := fixtureBackend()
		store, err := backend.OpenWithOptions(t.Context(), t.TempDir(), backends.OpenOptions{
			HTTPClient: &http.Client{},
		})
		if store != nil {
			t.Fatal("OpenWithOptions returned a non-nil store alongside the refusal")
		}
		if !errors.Is(err, backends.ErrHTTPClientWithoutOpenWith) {
			t.Fatalf("OpenWithOptions error = %v, want %v", err, backends.ErrHTTPClientWithoutOpenWith)
		}
	})

	t.Run("UserAgent alone", func(t *testing.T) {
		backend := fixtureBackend()
		store, err := backend.OpenWithOptions(t.Context(), t.TempDir(), backends.OpenOptions{
			UserAgent: "test-agent/1.0",
		})
		if store != nil {
			t.Fatal("OpenWithOptions returned a non-nil store alongside the refusal")
		}
		if !errors.Is(err, backends.ErrUserAgentWithoutOpenWith) {
			t.Fatalf("OpenWithOptions error = %v, want %v", err, backends.ErrUserAgentWithoutOpenWith)
		}
	})
}

// fixtureNarrowCredential is the credential type a hypothetical backend's
// OpenWith expects. fixtureCredential (the generic marker used elsewhere in
// this file) does not implement it, so passing fixtureCredential to an
// OpenWith that requires fixtureNarrowCredential must be refused.
type fixtureNarrowCredential struct{ apiKey string }

func (fixtureNarrowCredential) BackendCredential() {}

// narrowOpenWithBackend returns a Backend whose OpenWith type-asserts
// opts.Credential to fixtureNarrowCredential and refuses with
// backends.ErrUnsupportedCredential (wrapped, to prove errors.Is still
// matches through a wrap) on any non-nil Credential that fails the
// assertion — demonstrating the M3 contract OpenWith implementations must
// follow.
func narrowOpenWithBackend() backends.Backend {
	open := func(context.Context, string) (storage.DoltStorage, error) {
		return nil, errors.New("fixture: Open called despite backend having OpenWith")
	}
	return backends.Backend{
		Open:         open,
		OpenReadOnly: open,
		OpenWith: func(_ context.Context, _ string, opts backends.OpenOptions) (storage.DoltStorage, error) {
			if opts.Credential == nil {
				return nil, errFixtureOpenWith
			}
			if _, ok := opts.Credential.(fixtureNarrowCredential); !ok {
				return nil, fmt.Errorf("fixture: credential %T is not fixtureNarrowCredential: %w", opts.Credential, backends.ErrUnsupportedCredential)
			}
			return nil, errFixtureOpenWith
		},
	}
}

// TestBackendOpenWithRefusesUnsupportedCredentialType is the M3 contract: an
// OpenWith implementation that receives a non-nil Credential it cannot
// type-assert to its expected concrete type MUST refuse with
// ErrUnsupportedCredential (or a wrap of it), not silently fall back to
// default/ambient auth.
func TestBackendOpenWithRefusesUnsupportedCredentialType(t *testing.T) {
	backend := narrowOpenWithBackend()

	t.Run("mismatched credential type is refused", func(t *testing.T) {
		store, err := backend.OpenWithOptions(t.Context(), t.TempDir(), backends.OpenOptions{
			Credential: fixtureCredential{},
		})
		if store != nil {
			t.Fatal("OpenWithOptions returned a non-nil store alongside the refusal")
		}
		if !errors.Is(err, backends.ErrUnsupportedCredential) {
			t.Fatalf("OpenWithOptions error = %v, want it to wrap %v", err, backends.ErrUnsupportedCredential)
		}
	})

	t.Run("matching credential type is accepted", func(t *testing.T) {
		_, err := backend.OpenWithOptions(t.Context(), t.TempDir(), backends.OpenOptions{
			Credential: fixtureNarrowCredential{apiKey: "k"},
		})
		if errors.Is(err, backends.ErrUnsupportedCredential) {
			t.Fatalf("OpenWithOptions refused a matching credential type: %v", err)
		}
		if !errors.Is(err, errFixtureOpenWith) {
			t.Fatalf("OpenWithOptions error = %v, want %v (OpenWith reached, credential accepted)", err, errFixtureOpenWith)
		}
	})

	t.Run("nil credential is unaffected", func(t *testing.T) {
		_, err := backend.OpenWithOptions(t.Context(), t.TempDir(), backends.OpenOptions{})
		if !errors.Is(err, errFixtureOpenWith) {
			t.Fatalf("OpenWithOptions(nil Credential) error = %v, want %v", err, errFixtureOpenWith)
		}
	})
}

func TestBackendRemoteAndIsRemote(t *testing.T) {
	const remoteName = "fixture-remote"
	const localName = "fixture-local"

	remote := fixtureBackend()
	remote.Remote = true
	backends.Register(remoteName, remote)
	t.Cleanup(func() { backends.Deregister(remoteName) })

	backends.Register(localName, fixtureBackend())
	t.Cleanup(func() { backends.Deregister(localName) })

	if !backends.IsRemote(remoteName) {
		t.Errorf("IsRemote(%q) = false, want true", remoteName)
	}
	if backends.IsRemote(localName) {
		t.Errorf("IsRemote(%q) = true, want false", localName)
	}
	if backends.IsRemote("never-registered") {
		t.Error("IsRemote(unregistered name) = true, want false")
	}

	registered, ok := backends.Lookup(remoteName)
	if !ok || !registered.Remote {
		t.Fatalf("Lookup(%q) Remote = %v, ok = %v, want true, true", remoteName, registered.Remote, ok)
	}
}
