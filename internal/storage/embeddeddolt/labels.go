//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func (s *EmbeddedDoltStore) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	var labels []string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		labels, err = issueops.GetLabelsInTx(ctx, tx, "labels", issueID)
		return err
	})
	return labels, err
}

func (s *EmbeddedDoltStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.AddLabelInTx(ctx, tx, "labels", "events", issueID, label, actor)
	})
}
