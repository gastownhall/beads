package storage

import "context"

// FederationStore provides federation peer management.
type FederationStore interface {
	// AddFederationPeer upserts a federation peer record.
	//
	// Sovereignty contract (GH#5213): for an insert, the supplied tier is
	// stored, including empty. For an existing peer, a non-empty tier
	// replaces the stored tier; an empty tier preserves it unless
	// peer.ClearSovereignty is true, which clears it. A non-empty tier
	// wins when a caller also sets ClearSovereignty. On success the
	// effective stored tier is copied into peer.Sovereignty; on failure the
	// input fields are left untouched (no other rollback is promised).
	// Reads never populate ClearSovereignty.
	AddFederationPeer(ctx context.Context, peer *FederationPeer) error
	GetFederationPeer(ctx context.Context, name string) (*FederationPeer, error)
	ListFederationPeers(ctx context.Context) ([]*FederationPeer, error)
	RemoveFederationPeer(ctx context.Context, name string) error
}
