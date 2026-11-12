package storage

import "context"

// FederationStore provides federation peer management.
type FederationStore interface {
	// AddFederationPeer upserts a peer and its credentials. A new peer stores
	// the supplied Sovereignty, even an empty one. For an existing peer a
	// non-empty Sovereignty replaces the stored tier and an empty one keeps
	// it. On success peer.Sovereignty is set to the peer's effective tier; a
	// failed call leaves it untouched.
	AddFederationPeer(ctx context.Context, peer *FederationPeer) error
	GetFederationPeer(ctx context.Context, name string) (*FederationPeer, error)
	ListFederationPeers(ctx context.Context) ([]*FederationPeer, error)
	RemoveFederationPeer(ctx context.Context, name string) error
}
