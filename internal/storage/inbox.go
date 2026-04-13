package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// InboxStore provides cross-project inbox operations.
// Each project has an inbox table where other projects can deposit issues.
// The receiving project controls when and whether to import them.
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
