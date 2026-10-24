package dolt

import (
	"context"
	"testing"
)

// openPeerStore opens a second store on store's database, with a connection
// pool of its own as a second bd process would have.
func openPeerStore(t *testing.T, ctx context.Context, store *DoltStore) *DoltStore {
	t.Helper()
	return openStoreOnPort(t, ctx, store, testServerPort)
}

// openStoreOnPort opens a store on store's database through whatever listens
// on port: the test server itself, or a test proxy in front of it.
func openStoreOnPort(t *testing.T, ctx context.Context, store *DoltStore, port int) *DoltStore {
	t.Helper()
	peer, err := New(ctx, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		ServerHost:     "127.0.0.1",
		ServerPort:     port,
		Database:       store.database,
		MaxOpenConns:   2,
	})
	if err != nil {
		t.Fatalf("open store on %s through port %d: %v", store.database, port, err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	return peer
}
