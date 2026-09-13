//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// DefineLabel adds a row to the opt-in label vocabulary registry (bd label
// define). See issueops.DefineLabelInTx for the case-collision discipline.
func (s *EmbeddedDoltStore) DefineLabel(ctx context.Context, label, description, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.DefineLabelInTx(ctx, tx, label, description, actor)
	})
}

// UndefineLabel removes a row from the label vocabulary registry (bd label
// undefine).
func (s *EmbeddedDoltStore) UndefineLabel(ctx context.Context, label string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.UndefineLabelInTx(ctx, tx, label)
	})
}

// ListLabelDefinitions returns every row in the label vocabulary registry,
// sorted by label.
func (s *EmbeddedDoltStore) ListLabelDefinitions(ctx context.Context) ([]types.LabelDefinition, error) {
	var defs []types.LabelDefinition
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		defs, err = issueops.ListLabelDefinitionsInTx(ctx, tx)
		return err
	})
	return defs, err
}
