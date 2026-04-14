package storage

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

const (
	// MaxInboxTitleLen is the maximum allowed length for inbox item titles.
	MaxInboxTitleLen = 500
	// MaxInboxDescriptionLen is the maximum allowed length for inbox item descriptions.
	MaxInboxDescriptionLen = 100_000
	// MaxInboxMetadataLen is the maximum allowed length for inbox metadata JSON.
	MaxInboxMetadataLen = 50_000
)

// ValidateInboxItem checks size constraints on inbox item fields.
func ValidateInboxItem(item *types.InboxItem) error {
	if len(item.Title) > MaxInboxTitleLen {
		return fmt.Errorf("inbox item title too long (%d chars, max %d)", len(item.Title), MaxInboxTitleLen)
	}
	if len(item.Description) > MaxInboxDescriptionLen {
		return fmt.Errorf("inbox item description too long (%d chars, max %d)", len(item.Description), MaxInboxDescriptionLen)
	}
	if len(item.Metadata) > MaxInboxMetadataLen {
		return fmt.Errorf("inbox item metadata too long (%d chars, max %d)", len(item.Metadata), MaxInboxMetadataLen)
	}
	return nil
}

// InboxStore provides local inbox CRUD operations.
// Each project has an inbox table where other projects can deposit issues.
// The receiving project controls when and whether to import them.
//
// Delivery to remote projects is handled separately by the transfer.InboxTransport
// interface, which decouples the transport mechanism from local storage.
type InboxStore interface {
	AddInboxItem(ctx context.Context, item *types.InboxItem) error
	GetInboxItem(ctx context.Context, inboxID string) (*types.InboxItem, error)
	GetInboxItemByPrefix(ctx context.Context, prefix string) (*types.InboxItem, error)
	GetPendingInboxItems(ctx context.Context) ([]*types.InboxItem, error)
	MarkInboxItemImported(ctx context.Context, inboxID string, importedIssueID string) error
	MarkInboxItemRejected(ctx context.Context, inboxID string, reason string) error
	CleanInbox(ctx context.Context) (int64, error)
	CountPendingInbox(ctx context.Context) (int64, error)
}
