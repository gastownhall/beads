package main

import (
	"context"
	"reflect"
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
