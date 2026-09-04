package dolt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestWithPeerCredentialsMissingPeerRow covers the behavior #4837 asked for:
// a peer name with no federation_peers row is a credential-free remote, so
// withPeerCredentials must invoke fn once with nil credentials and return
// fn's result instead of failing the lookup. The stored-credential case is
// kept alongside so the non-regression side stays honest.
func TestWithPeerCredentialsMissingPeerRow(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	baseDir := t.TempDir()
	beadsDir := filepath.Join(baseDir, ".beads")
	dbName := fmt.Sprintf("test_peer_creds_missing_%d", testServerPort)

	assertDatabaseNotExists(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	store, err := New(ctx, &Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	t.Run("no peer row proceeds with nil credentials", func(t *testing.T) {
		calls := 0
		err := store.withPeerCredentials(ctx, "no-such-peer", func(creds *remoteCredentials) error {
			calls++
			if creds != nil {
				t.Errorf("fn received creds %+v, want nil for missing peer row", creds)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("withPeerCredentials() error = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("fn invoked %d times, want 1", calls)
		}
	})

	t.Run("no peer row propagates fn error", func(t *testing.T) {
		want := errors.New("remote rejected")
		err := store.withPeerCredentials(ctx, "no-such-peer", func(*remoteCredentials) error {
			return want
		})
		if !errors.Is(err, want) {
			t.Fatalf("withPeerCredentials() error = %v, want %v", err, want)
		}
	})

	t.Run("stored credentials reach fn", func(t *testing.T) {
		peer := &storage.FederationPeer{
			Name:        "peerwithcreds",
			RemoteURL:   "file:///tmp/nonexistent-peer",
			Username:    "alice",
			Password:    "s3cret",
			Sovereignty: "T2",
		}
		if err := store.AddFederationPeer(ctx, peer); err != nil {
			t.Fatalf("AddFederationPeer() error = %v", err)
		}

		calls := 0
		err := store.withPeerCredentials(ctx, peer.Name, func(creds *remoteCredentials) error {
			calls++
			if creds == nil {
				t.Fatal("fn received nil creds, want stored credentials")
			}
			if creds.username != peer.Username || creds.password != peer.Password {
				t.Errorf("fn received creds %q/%q, want %q/%q", creds.username, creds.password, peer.Username, peer.Password)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("withPeerCredentials() error = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("fn invoked %d times, want 1", calls)
		}
	})
}

func TestIsMissingFederationPeer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain not found", storage.ErrNotFound, true},
		{"wrapped not found", fmt.Errorf("%w: federation peer origin", storage.ErrNotFound), true},
		{"double wrap", fmt.Errorf("failed to get peer credentials: %w", fmt.Errorf("%w: federation peer origin", storage.ErrNotFound)), true},
		{"other error", errors.New("decrypt failed"), false},
		{"sql-ish other", fmt.Errorf("failed to get federation peer: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingFederationPeer(tc.err); got != tc.want {
				t.Fatalf("isMissingFederationPeer(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
