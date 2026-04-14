// Package transfer defines the transport abstraction for cross-project inbox delivery.
//
// Local inbox CRUD is handled by storage.InboxStore. This package separates the
// delivery mechanism so that different backends (shared Dolt server, federation
// peers, etc.) can implement the same interface without coupling the CLI to a
// specific transport topology.
package transfer

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// Destination describes a target project for inbox delivery.
// ProjectName is the user-facing identifier; Address is transport-specific
// (e.g., a database name for shared-server, a remote URL for federation).
type Destination struct {
	ProjectName string // User-facing project name
	Address     string // Transport-specific target (DB name, remote URL, etc.)
}

// InboxTransport delivers inbox items to a remote project.
// Implementations must be safe for concurrent use.
type InboxTransport interface {
	// Send delivers items to the destination project's inbox.
	// Returns the number of items successfully delivered.
	Send(ctx context.Context, dest Destination, items []*types.InboxItem) (int, error)

	// ValidateDestination checks whether the destination is reachable
	// and correctly configured, without actually sending anything.
	ValidateDestination(ctx context.Context, dest Destination) error
}
