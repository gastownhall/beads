package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

type fakeDoltRemoteAddStore struct {
	remotes []storage.RemoteInfo
	calls   []string
}

func (f *fakeDoltRemoteAddStore) ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error) {
	f.calls = append(f.calls, "list")
	return append([]storage.RemoteInfo(nil), f.remotes...), nil
}

func (f *fakeDoltRemoteAddStore) AddRemote(ctx context.Context, name, url string) error {
	f.calls = append(f.calls, "add "+name+" "+url)
	f.remotes = append(f.remotes, storage.RemoteInfo{Name: name, URL: url})
	return nil
}

func (f *fakeDoltRemoteAddStore) AddRemoteWithRef(ctx context.Context, name, url, ref string) error {
	if ref == "" {
		return f.AddRemote(ctx, name, url)
	}
	f.calls = append(f.calls, "add "+name+" "+url+" ref="+ref)
	f.remotes = append(f.remotes, storage.RemoteInfo{Name: name, URL: url, Ref: ref})
	return nil
}

func (f *fakeDoltRemoteAddStore) RemoveRemote(ctx context.Context, name string) error {
	f.calls = append(f.calls, "remove "+name)
	filtered := f.remotes[:0]
	for _, remote := range f.remotes {
		if remote.Name != name {
			filtered = append(filtered, remote)
		}
	}
	f.remotes = filtered
	return nil
}

func TestEnsureDoltRemoteSameNormalizedURLIsNoop(t *testing.T) {
	store := &fakeDoltRemoteAddStore{
		remotes: []storage.RemoteInfo{
			{Name: "origin", URL: "https://github.com/org/repo.git"},
		},
	}
	prompted := false

	result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", func(surface, name, existingURL, newURL string) bool {
		prompted = true
		return true
	})
	if err != nil {
		t.Fatalf("ensureDoltRemote: %v", err)
	}
	if result.Canceled {
		t.Fatal("same normalized URL should not cancel")
	}
	if prompted {
		t.Fatal("same normalized URL should not prompt for overwrite")
	}
	if want := []string{"list"}; !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
}

func TestEnsureDoltRemoteDifferentURLReplacesExisting(t *testing.T) {
	store := &fakeDoltRemoteAddStore{
		remotes: []storage.RemoteInfo{
			{Name: "origin", URL: "git+https://github.com/org/old.git"},
		},
	}
	confirmed := false

	result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/new.git", func(surface, name, existingURL, newURL string) bool {
		confirmed = true
		if surface != "SQL server" || name != "origin" || existingURL != "git+https://github.com/org/old.git" || newURL != "git+https://github.com/org/new.git" {
			t.Fatalf("confirm args = %q %q %q %q", surface, name, existingURL, newURL)
		}
		return true
	})
	if err != nil {
		t.Fatalf("ensureDoltRemote: %v", err)
	}
	if result.Canceled {
		t.Fatal("confirmed replacement should not cancel")
	}
	if !confirmed {
		t.Fatal("different URL should prompt before replacement")
	}
	want := []string{
		"list",
		"remove origin",
		"add origin git+https://github.com/org/new.git",
	}
	if !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
}

// fakeDoltRemoteAddStoreWithDisk simulates the GH#2118 cold-start window: the
// SQL listing (embedded fake) can be empty while remotes are persisted on
// disk. It implements persistedRemoteInfoLister directly, which
// persistedRemoteInfosFor honors before any decorator peeling.
type fakeDoltRemoteAddStoreWithDisk struct {
	fakeDoltRemoteAddStore
	persisted []storage.RemoteInfo
	removeErr error
	addErr    error
}

func (f *fakeDoltRemoteAddStoreWithDisk) PersistedRemoteInfos() []storage.RemoteInfo {
	f.calls = append(f.calls, "persisted")
	return append([]storage.RemoteInfo(nil), f.persisted...)
}

func (f *fakeDoltRemoteAddStoreWithDisk) AddRemoteWithRef(ctx context.Context, name, url, ref string) error {
	if f.addErr != nil {
		f.calls = append(f.calls, "add-fail "+name)
		return f.addErr
	}
	return f.fakeDoltRemoteAddStore.AddRemoteWithRef(ctx, name, url, ref)
}

func (f *fakeDoltRemoteAddStoreWithDisk) RemoveRemote(ctx context.Context, name string) error {
	if f.removeErr != nil {
		f.calls = append(f.calls, "remove-fail "+name)
		return f.removeErr
	}
	return f.fakeDoltRemoteAddStore.RemoveRemote(ctx, name)
}

// TestEnsureDoltRemoteColdStartSameURLMigratesToSQL: a matching remote known
// only from persisted disk state (SQL dolt_remotes empty) is one the server
// cannot push, pull or fetch with, since it resolves names through that
// listing. The add registers it over SQL with the recorded URL and git ref,
// not the caller's spelling, asks nothing, and surfaces a failed write
// instead of reporting success over a remote SQL still lacks (PR #7447).
func TestEnsureDoltRemoteColdStartSameURLMigratesToSQL(t *testing.T) {
	noPrompt := func(t *testing.T) doltRemoteOverwriteConfirmer {
		return func(surface, name, existingURL, newURL string) bool {
			t.Fatalf("same-URL migration should not prompt (existing %s, new %s)", existingURL, newURL)
			return false
		}
	}

	t.Run("recorded_url", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "https://github.com/org/repo.git"},
			},
		}
		result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", noPrompt(t))
		if err != nil {
			t.Fatalf("ensureDoltRemote: %v", err)
		}
		if result.Canceled {
			t.Fatal("same-URL migration should not cancel")
		}
		want := []string{"list", "persisted", "add origin https://github.com/org/repo.git"}
		if !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %v, want %v", store.calls, want)
		}
	})

	t.Run("recorded_ref", func(t *testing.T) {
		const url = "git+ssh://git@github.com/org/repo.git"
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: url, Ref: "refs/dolt/custom"},
			},
		}
		result, err := ensureDoltRemote(context.Background(), store, "origin", url, noPrompt(t))
		if err != nil {
			t.Fatalf("ensureDoltRemote: %v", err)
		}
		if result.Canceled {
			t.Fatal("same-URL migration should not cancel")
		}
		want := []string{"list", "persisted", "add origin " + url + " ref=refs/dolt/custom"}
		if !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %v, want %v", store.calls, want)
		}
	})

	t.Run("add_error_surfaces", func(t *testing.T) {
		addErr := errors.New("server refused")
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "https://github.com/org/repo.git"},
			},
			addErr: addErr,
		}
		_, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", noPrompt(t))
		if !errors.Is(err, addErr) || !strings.HasPrefix(err.Error(), "add remote origin: ") {
			t.Fatalf("ensureDoltRemote error = %v, want add remote origin: wrapping %v", err, addErr)
		}
	})
}

// TestEnsureDoltRemoteListedSameURLSkipsDisk: a match the SQL listing
// already has is the idempotent case: the add neither consults the persisted
// remotes nor writes, even when disk records the same remote.
func TestEnsureDoltRemoteListedSameURLSkipsDisk(t *testing.T) {
	store := &fakeDoltRemoteAddStoreWithDisk{
		fakeDoltRemoteAddStore: fakeDoltRemoteAddStore{
			remotes: []storage.RemoteInfo{
				{Name: "origin", URL: "https://github.com/org/repo.git"},
			},
		},
		persisted: []storage.RemoteInfo{
			{Name: "origin", URL: "https://github.com/org/repo.git"},
		},
	}
	result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", func(surface, name, existingURL, newURL string) bool {
		t.Fatalf("listed same-URL add should not prompt")
		return false
	})
	if err != nil {
		t.Fatalf("ensureDoltRemote: %v", err)
	}
	if result.Canceled {
		t.Fatal("listed same-URL add should not cancel")
	}
	if want := []string{"list"}; !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
}

// TestEnsureDoltRemoteColdStartDifferentURLPrompts pins that the invisible
// persisted remote gets the SAME overwrite confirmation the visible one
// would — before wy-6k7f7 an empty listing skipped the confirmation and the
// add silently clobbered the persisted remote. A declined confirmation
// writes nothing; a confirmed one proceeds even though the cold server
// cannot remove a remote it does not see yet.
func TestEnsureDoltRemoteColdStartDifferentURLPrompts(t *testing.T) {
	t.Run("declined_writes_nothing", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "git+https://github.com/org/old.git"},
			},
		}
		confirmed := false
		result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/new.git", func(surface, name, existingURL, newURL string) bool {
			confirmed = true
			if existingURL != "git+https://github.com/org/old.git" {
				t.Fatalf("confirm existingURL = %q, want the persisted on-disk URL", existingURL)
			}
			return false
		})
		if err != nil {
			t.Fatalf("ensureDoltRemote: %v", err)
		}
		if !confirmed {
			t.Fatal("cold-start URL disagreement should prompt for overwrite")
		}
		if !result.Canceled {
			t.Fatal("declined overwrite should cancel")
		}
		for _, call := range store.calls {
			if strings.HasPrefix(call, "add") || strings.HasPrefix(call, "remove") {
				t.Fatalf("declined overwrite wrote to the store: %v", store.calls)
			}
		}
	})

	t.Run("confirmed_tolerates_unremovable_invisible_remote", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "git+https://github.com/org/old.git"},
			},
			removeErr: errors.New("unknown remote: origin"),
		}
		result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/new.git", func(surface, name, existingURL, newURL string) bool {
			return true
		})
		if err != nil {
			t.Fatalf("ensureDoltRemote: %v", err)
		}
		if result.Canceled {
			t.Fatal("confirmed overwrite should not cancel")
		}
		want := []string{"list", "persisted", "remove-fail origin", "add origin git+https://github.com/org/new.git"}
		if !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %v, want %v", store.calls, want)
		}
	})
}
