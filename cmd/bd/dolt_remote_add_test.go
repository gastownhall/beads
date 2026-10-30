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
	return f.AddRemoteWithRef(ctx, name, url, "")
}

// AddRemoteWithRef records the git ref alongside the add, the way the SQL
// store does; an empty ref logs the same call AddRemote always logged, so
// expectations that never carry a ref are unchanged.
func (f *fakeDoltRemoteAddStore) AddRemoteWithRef(ctx context.Context, name, url, ref string) error {
	call := "add " + name + " " + url
	if ref != "" {
		call += " ref=" + ref
	}
	f.calls = append(f.calls, call)
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
	if wantRemotes := []storage.RemoteInfo{{Name: "origin", URL: "https://github.com/org/repo.git"}}; !reflect.DeepEqual(store.remotes, wantRemotes) {
		t.Fatalf("remotes = %+v, want %+v (a listed match keeps the listed spelling, untouched)", store.remotes, wantRemotes)
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
	// raced is what a concurrent add registered on the server after the
	// listing: a failing add lists it from then on, as the losing side of
	// that race finds the server.
	raced []storage.RemoteInfo
}

func (f *fakeDoltRemoteAddStoreWithDisk) PersistedRemoteInfos() []storage.RemoteInfo {
	f.calls = append(f.calls, "persisted")
	return append([]storage.RemoteInfo(nil), f.persisted...)
}

func (f *fakeDoltRemoteAddStoreWithDisk) AddRemoteWithRef(ctx context.Context, name, url, ref string) error {
	if f.addErr != nil {
		f.calls = append(f.calls, "add-fail "+name)
		f.remotes = append(f.remotes, f.raced...)
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
// only from persisted disk state (SQL dolt_remotes empty) must be registered
// in SQL, so `bd dolt remote add` reporting success leaves the remote visible
// to the server (PR #7447). The recorded URL and git ref are what get written,
// not the caller's spelling: the recorded URL is the one a server restart
// would load, doltremote.Normalize equates https:// with git+https:// while
// Dolt implies git transport only for .git and git+ URLs, and a re-add
// without the recorded ref would pin pushes onto the default data ref.
func TestEnsureDoltRemoteColdStartSameURLMigratesToSQL(t *testing.T) {
	t.Run("recorded_url", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "https://github.com/org/repo.git"},
			},
		}

		result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", func(surface, name, existingURL, newURL string) bool {
			t.Fatalf("same-URL migration should not prompt")
			return false
		})
		if err != nil {
			t.Fatalf("ensureDoltRemote: %v", err)
		}
		if result.Canceled {
			t.Fatal("same-URL migration should not cancel")
		}
		want := []string{"list", "persisted", "add origin https://github.com/org/repo.git"}
		if !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %v, want %v (the recorded URL, not the caller's)", store.calls, want)
		}
		if got := findDoltRemoteURL(store.remotes, "origin"); got != "https://github.com/org/repo.git" {
			t.Fatalf("SQL remotes = %+v, want origin at the recorded URL", store.remotes)
		}
	})

	t.Run("recorded_ref", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "git+ssh://git@github.com/org/repo.git", Ref: "refs/dolt/custom"},
			},
		}

		_, err := ensureDoltRemote(context.Background(), store, "origin", "git+ssh://git@github.com/org/repo.git", func(surface, name, existingURL, newURL string) bool {
			t.Fatalf("same-URL migration should not prompt")
			return false
		})
		if err != nil {
			t.Fatalf("ensureDoltRemote: %v", err)
		}
		want := []string{"list", "persisted", "add origin git+ssh://git@github.com/org/repo.git ref=refs/dolt/custom"}
		if !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %v, want %v (the recorded ref carried onto the add)", store.calls, want)
		}
		if wantRemotes := []storage.RemoteInfo{{Name: "origin", URL: "git+ssh://git@github.com/org/repo.git", Ref: "refs/dolt/custom"}}; !reflect.DeepEqual(store.remotes, wantRemotes) {
			t.Fatalf("SQL remotes = %+v, want %+v (the recorded ref kept)", store.remotes, wantRemotes)
		}
	})

	t.Run("add_error_surfaces", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "https://github.com/org/repo.git"},
			},
			addErr: errors.New("dolt refused the add"),
		}

		_, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", func(surface, name, existingURL, newURL string) bool {
			t.Fatalf("same-URL migration should not prompt")
			return false
		})
		if err == nil {
			t.Fatal("ensureDoltRemote = nil error, want the failed registration surfaced, not a silent success")
		}
		if !errors.Is(err, store.addErr) || !strings.Contains(err.Error(), "add remote origin") {
			t.Fatalf("ensureDoltRemote = %v, want an error wrapping the add failure as \"add remote origin: …\"", err)
		}
	})

	// The server checks the add against the set it lists, so "remote already
	// exists" after a listing without the remote means a concurrent add
	// registered it. As for the push path's re-add, that is the state the add
	// wanted, once the server lists the remote at a matching URL.
	t.Run("already_exists_is_accepted", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "origin", URL: "https://github.com/org/repo.git"},
			},
			addErr: errors.New("Error 1105 (HY000): remote already exists"),
			raced:  []storage.RemoteInfo{{Name: "origin", URL: "git+https://github.com/org/repo.git"}},
		}

		result, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", func(surface, name, existingURL, newURL string) bool {
			t.Fatalf("same-URL migration should not prompt")
			return false
		})
		if err != nil {
			t.Fatalf("ensureDoltRemote = %v, want the concurrently registered remote taken as registered", err)
		}
		if result.Canceled {
			t.Fatal("same-URL migration should not cancel")
		}
		want := []string{"list", "persisted", "add-fail origin", "list"}
		if !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %v, want %v (the listing read again before the race counts as success)", store.calls, want)
		}
	})

	t.Run("already_exists_without_a_matching_listing_surfaces", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			raced []storage.RemoteInfo
		}{
			{name: "listed_at_another_url", raced: []storage.RemoteInfo{{Name: "origin", URL: "git+https://github.com/org/other.git"}}},
			{name: "still_unlisted"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store := &fakeDoltRemoteAddStoreWithDisk{
					persisted: []storage.RemoteInfo{
						{Name: "origin", URL: "https://github.com/org/repo.git"},
					},
					addErr: errors.New("Error 1105 (HY000): remote already exists"),
					raced:  tc.raced,
				}

				_, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://github.com/org/repo.git", func(surface, name, existingURL, newURL string) bool {
					t.Fatalf("same-URL migration should not prompt")
					return false
				})
				if !errors.Is(err, store.addErr) || !strings.Contains(err.Error(), "add remote origin") {
					t.Fatalf("ensureDoltRemote = %v, want the add failure surfaced: the server lists no origin at the requested URL (server remotes %+v)", err, store.remotes)
				}
			})
		}
	})

	// DOLT_REMOTE('add') takes --ref alone and the server rewrites the
	// remote's record from it, so registering a remote recorded with the
	// dolt CLI's aws-*/oss-* parameters would erase them, where a restart
	// loads them: the add is refused and nothing is written.
	t.Run("cli_only_params_refused", func(t *testing.T) {
		store := &fakeDoltRemoteAddStoreWithDisk{
			persisted: []storage.RemoteInfo{
				{Name: "awsr", URL: "aws://[tbl:bucket]/db", CLIOnlyParams: []string{"aws-creds-type", "aws-region"}},
			},
		}

		_, err := ensureDoltRemote(context.Background(), store, "awsr", "aws://[tbl:bucket]/db", func(surface, name, existingURL, newURL string) bool {
			t.Fatalf("same-URL migration should not prompt")
			return false
		})
		if err == nil {
			t.Fatal("ensureDoltRemote = nil error, want the add refused: it would erase the recorded aws-* parameters")
		}
		for _, part := range []string{"add remote awsr", "aws-creds-type, aws-region", "restart the Dolt server"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("ensureDoltRemote = %v, want it to name %q", err, part)
			}
		}
		if want := []string{"list", "persisted"}; !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %v, want %v (nothing written)", store.calls, want)
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
