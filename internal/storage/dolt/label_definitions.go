package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// DefineLabel adds a row to the opt-in label vocabulary registry (bd label
// define). See issueops.DefineLabelInTx for the case-collision discipline.
//
// The write DOLT-commits (bda-d1py): label_definitions is a main-plane table
// (no dolt_ignore, unlike label_namespace_locks) holding the workspace's
// shared curated vocabulary, so a row left in the working set never travels
// on push/clone - a peer under labels.vocabulary=enforce would run against an
// empty or stale registry. Same discipline as the sibling durable label
// mutations (AddLabel/RemoveLabel -> doltAddAndCommit).
func (s *DoltStore) DefineLabel(ctx context.Context, label, description, actor string) error {
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
			return issueops.DefineLabelInTx(ctx, tx, label, description, actor)
		}); err != nil {
			return err
		}
		return wrapDefinitionPublish(
			s.doltAddAndCommit(ctx, []string{"label_definitions"}, fmt.Sprintf("bd: label define %s", label)))
	})
}

// ImportLabelDefinitions applies a batch of vocabulary records in ONE
// transaction and ONE Dolt commit (bda-o5gq). The per-record DefineLabel loop
// the classic import used published every definition independently - a
// failing issue import after N definitions left N already-public rows, a
// retry was not the same atomic operation, and a large input minted one Dolt
// history commit per record. Decision logic stays in
// issueops.ImportLabelDefinitions (the one authority); this method only
// scopes it to a single tx + commit.
//
// SCOPE: this guarantee is DoltStore's alone. The caller
// (cmd/bd.applyLabelDefinitionsClassic) reaches it through an optional
// interface assertion, and EmbeddedDoltStore does not implement this method,
// so embedded mode still takes the per-record DefineLabel loop -- one
// transaction and one Dolt commit per definition, as before. That is a
// weaker atomicity story, not a wrong one: the loop's own decision logic is
// the same issueops.ImportLabelDefinitions, so which definitions land and
// which warn is identical either way; only the batching differs.
func (s *DoltStore) ImportLabelDefinitions(ctx context.Context, incoming []types.LabelDefinition, actor string) (int, []string, error) {
	if len(incoming) == 0 {
		return 0, nil, nil
	}
	var defined int
	var warnings []string
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
			existing, err := issueops.ListLabelDefinitionsInTx(ctx, tx)
			if err != nil {
				return err
			}
			// withRetryTx may re-run the closure: reset the outputs so a
			// retried attempt cannot double-count.
			defined, warnings = 0, nil
			d, w, err := issueops.ImportLabelDefinitions(ctx, existing, incoming, actor,
				func(ctx context.Context, label, description, defActor string) error {
					return issueops.DefineLabelInTx(ctx, tx, label, description, defActor)
				})
			defined, warnings = d, w
			return err
		}); err != nil {
			return err
		}
		if defined == 0 {
			return nil // exact re-import: nothing changed, no commit to mint
		}
		return wrapDefinitionPublish(s.doltAddAndCommit(ctx, []string{"label_definitions"},
			fmt.Sprintf("bd: import %d label definitions", defined)))
	})
	return defined, warnings, err
}

// UndefineLabel removes a row from the label vocabulary registry (bd label
// undefine). Dolt-commits for the same reason DefineLabel does.
func (s *DoltStore) UndefineLabel(ctx context.Context, label string) error {
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
			return issueops.UndefineLabelInTx(ctx, tx, label)
		}); err != nil {
			return err
		}
		return wrapDefinitionPublish(
			s.doltAddAndCommit(ctx, []string{"label_definitions"}, fmt.Sprintf("bd: label undefine %s", label)))
	})
}

// ListLabelDefinitions returns every row in the label vocabulary registry,
// sorted by label.
func (s *DoltStore) ListLabelDefinitions(ctx context.Context) ([]types.LabelDefinition, error) {
	var defs []types.LabelDefinition
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		defs, err = issueops.ListLabelDefinitionsInTx(ctx, tx)
		return err
	})
	return defs, err
}

// wrapDefinitionPublish makes a publication failure DETERMINATE in its message
// (bda-ahxf): the vocabulary row's SQL transaction has already committed when
// doltAddAndCommit runs, so a failure here is NOT a failed definition - the
// row exists in the working set and travels with the next Dolt commit, and a
// retry correctly reports already-defined/not-defined. Without this wrap the
// caller reads an error, retries, hits the validation refusal, and cannot
// tell which of the two operations actually took effect.
func wrapDefinitionPublish(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("label definition recorded but its publication to Dolt history is INDETERMINATE "+
		"(the row is committed in the working set; it is published only once a Dolt commit that stages "+
		"label_definitions succeeds - run 'bd dolt commit' or any labels write to settle it; "+
		"a retry reporting already-defined/not-defined confirms the row itself landed): %w", err)
}
