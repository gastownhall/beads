package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// DefineLabelInTx inserts a new row into the opt-in label vocabulary registry
// (bd label define) within an existing transaction.
//
// It is a CREATE, not an upsert: defining a label that already exists --
// under the SAME spelling -- is rejected, so changing a description means
// undefine then define. Defining a label that collides with an EXISTING row
// only under a DIFFERENT case ("Backend" when "backend" is defined) is also
// rejected, naming the existing spelling in the error, so this table can
// never hold two case-variant spellings of the same word: the creation-time
// discipline the vocabulary registry exists to add, without folding any
// label already stored on an issue (that stays `bd label rename`'s job).
//
// The case-insensitive collision is enforced TWICE, deliberately: the
// rejectLabelCollisionInTx pre-check below is the friendly, common-case path
// (it can name the exact existing spelling), but a check-then-insert cannot
// see a row a CONCURRENT transaction has not committed yet. label_folded's
// UNIQUE constraint (migration 0066) is the backstop that makes "never two
// case-variant spellings" a property of the SCHEMA rather than a promise this
// function keeps on its own: when two transactions race to define
// case-variants of the same word, both can pass the pre-check, but only one
// INSERT can land -- the loser's duplicate-key error is translated below into
// the same named-collision error the pre-check would have produced had it run
// a moment later.
//
// strings.ToLower is the ONE folding authority label_folded is populated
// from, here and in rejectLabelCollisionInTx/UndefineLabelInTx below: no
// query in this file folds case in SQL (no LOWER()), so the registry's
// case-insensitive matching can never disagree with itself across call sites.
//
// WHAT THAT AUTHORITY ACTUALLY DELIVERS (bda-h2yd): strings.ToLower is Go's
// simple 1:1 Unicode mapping, not full case folding, so the "never two
// case-variant spellings" discipline holds for ASCII and simply-mapped
// variants only. Pairs that only full folding unifies - Greek final sigma
// vs sigma, Turkish dotless i vs i - fold to DIFFERENT keys and can both be
// defined. Self-consistency is unaffected (store and check share this one
// function); the bound is on the invariant's breadth, accepted deliberately:
// real vocabularies are ASCII in practice, and switching to full folding
// would change the stored label_folded key format under existing rows. The
// migration 0066 header states the discipline in its strong form; this
// comment is the authoritative qualification (the applied migration file is
// content-frozen and cannot be reworded).
func DefineLabelInTx(ctx context.Context, tx DBTX, label, description, actor string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("%w: label cannot be empty", storage.ErrValidation)
	}
	if err := types.CheckFieldLen("label", label); err != nil {
		return err
	}
	if err := types.CheckTextLen("description", description); err != nil {
		return err
	}
	if err := types.CheckFieldLen("created_by", actor); err != nil {
		return err
	}
	if err := rejectLabelCollisionInTx(ctx, tx, label); err != nil {
		return err
	}

	folded := strings.ToLower(label)
	descArg := sql.NullString{String: description, Valid: description != ""}
	actorArg := sql.NullString{String: actor, Valid: actor != ""}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO label_definitions (label, label_folded, description, created_by) VALUES (?, ?, ?, ?)`,
		label, folded, descArg, actorArg,
	); err != nil {
		if isCreateOnlyDuplicateError(err) {
			return fmt.Errorf("%w: label %q collides with a definition committed by a concurrent request (case-insensitive); run 'bd label defined' to see the winning spelling, or undefine it first",
				storage.ErrValidation, label)
		}
		return fmt.Errorf("define label: %w", err)
	}
	return nil
}

// rejectLabelCollisionInTx returns an error naming the existing spelling when
// label already has a case-insensitive match in label_definitions, and a
// distinct error when the match is the exact same spelling (already defined).
// A clean sql.ErrNoRows (no match at all) returns nil.
//
// This is a pre-check, not the enforcement: it narrows the race window and
// gives the common case a friendly, spelling-naming error, but the row it
// looks for can be committed by another transaction moments after this query
// returns. DefineLabelInTx's label_folded UNIQUE constraint is what makes the
// invariant hold regardless.
func rejectLabelCollisionInTx(ctx context.Context, tx DBTX, label string) error {
	var existing string
	err := tx.QueryRowContext(ctx,
		`SELECT label FROM label_definitions WHERE label_folded = ?`, strings.ToLower(label),
	).Scan(&existing)
	switch {
	case err == nil:
		if existing == label {
			return fmt.Errorf("%w: label %q is already defined", storage.ErrValidation, label)
		}
		return fmt.Errorf("%w: label %q collides with the existing definition %q (case-insensitive); use %q or undefine it first",
			storage.ErrValidation, label, existing, existing)
	case errors.Is(err, sql.ErrNoRows):
		return nil
	default:
		return fmt.Errorf("define label: checking for collision: %w", err)
	}
}

// UndefineLabelInTx removes a row from the label vocabulary registry (bd
// label undefine) within an existing transaction. The match is
// case-insensitive, matching the case-collision discipline DefineLabelInTx
// enforces on the way in: only one spelling of a label can ever be defined,
// so there is only ever one row an undefine of any of its case-variants
// could mean.
//
// Undefining a label that was never defined is an error, not a silent no-op:
// unlike RemoveLabelInTx (which repairs a label mistakenly written on an
// issue, where idempotence matters more than catching a typo), undefine acts
// on a registry the caller is expected to already know the contents of, so a
// typo is far more likely than a race with a concurrent undefine of the same
// row.
func UndefineLabelInTx(ctx context.Context, tx DBTX, label string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("%w: label cannot be empty", storage.ErrValidation)
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM label_definitions WHERE label_folded = ?`, strings.ToLower(label),
	)
	if err != nil {
		return fmt.Errorf("undefine label: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("undefine label: rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: label %q is not defined", storage.ErrNotFound, label)
	}
	return nil
}

// ListLabelDefinitionsInTx returns every row in the label vocabulary
// registry, sorted by label, within an existing transaction.
func ListLabelDefinitionsInTx(ctx context.Context, tx DBTX) ([]types.LabelDefinition, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT label, description, created_at, created_by FROM label_definitions ORDER BY label`,
	)
	if err != nil {
		return nil, fmt.Errorf("list label definitions: %w", err)
	}
	defer rows.Close()

	var defs []types.LabelDefinition
	for rows.Next() {
		var (
			d         types.LabelDefinition
			desc      sql.NullString
			createdBy sql.NullString
		)
		if err := rows.Scan(&d.Label, &desc, &d.CreatedAt, &createdBy); err != nil {
			return nil, fmt.Errorf("list label definitions: scan: %w", err)
		}
		if desc.Valid {
			d.Description = &desc.String
		}
		if createdBy.Valid {
			d.CreatedBy = &createdBy.String
		}
		defs = append(defs, d)
	}
	return defs, rows.Err()
}

// LabelVocabularySet builds a case-insensitive lookup of every defined label,
// keyed on its lowercase form, for the write-path vocabulary check
// (checkLabelVocabulary in cmd/bd). The map value is the defined spelling, so
// a caller can suggest it when the input differs only in case.
func LabelVocabularySet(defs []types.LabelDefinition) map[string]string {
	set := make(map[string]string, len(defs))
	for _, d := range defs {
		set[strings.ToLower(d.Label)] = d.Label
	}
	return set
}

// ImportLabelDefinitions applies incoming vocabulary definitions (a JSONL
// import's "_type":"label-definition" records) with define-if-absent
// semantics, so a restored workspace gets its registry back and a re-import
// is a no-op. DefineLabelInTx is deliberately a CREATE, and an import
// replays history that may predate or disagree with the live registry, so
// the import contract mirrors the exclusive-labels one: never fail the batch
// on historical data.
//
//   - an incoming label already defined under the SAME spelling is skipped
//     silently (idempotent re-import)
//   - one that collides only under a DIFFERENT case keeps the existing
//     definition and reports a warning (strings.ToLower is the registry's
//     one folding authority - see the label_folded note above)
//   - an absent one is defined through the caller's define func; a define
//     refused with storage.ErrValidation (a definition committed by a
//     concurrent request between the existing read and this write) also
//     degrades to a warning, any other error fails the import
//
// The caller supplies the existing registry and the write primitive, so the
// same decision logic serves the classic store, the uow batch importer and
// any future transport. fallbackActor is recorded when a record carries no
// created_by of its own.
func ImportLabelDefinitions(ctx context.Context, existing, incoming []types.LabelDefinition, fallbackActor string, define func(ctx context.Context, label, description, actor string) error) (int, []string, error) {
	set := LabelVocabularySet(existing)
	defined := 0
	var warnings []string
	for _, def := range incoming {
		if def.Label == "" {
			continue
		}
		folded := strings.ToLower(def.Label)
		if have, ok := set[folded]; ok {
			if have != def.Label {
				warnings = append(warnings, fmt.Sprintf("label definition %q kept as existing %q (case-insensitive collision)", def.Label, have))
			}
			continue
		}
		description := ""
		if def.Description != nil {
			description = *def.Description
		}
		actor := fallbackActor
		if def.CreatedBy != nil && *def.CreatedBy != "" {
			actor = *def.CreatedBy
		}
		if err := define(ctx, def.Label, description, actor); err != nil {
			if errors.Is(err, storage.ErrValidation) {
				warnings = append(warnings, fmt.Sprintf("label definition %q not imported: %v", def.Label, err))
				continue
			}
			return defined, warnings, fmt.Errorf("import label definition %q: %w", def.Label, err)
		}
		set[folded] = def.Label
		defined++
	}
	return defined, warnings, nil
}
