package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func NewLabelSQLRepository(runner Runner) domain.LabelSQLRepository {
	return &labelSQLRepositoryImpl{
		runner: runner,
		events: NewEventsSQLRepository(runner),
	}
}

type labelSQLRepositoryImpl struct {
	runner Runner
	events domain.EventsSQLRepository
}

var _ domain.LabelSQLRepository = (*labelSQLRepositoryImpl)(nil)

func pickLabelTable(useWisps bool) string {
	if useWisps {
		return "wisp_labels"
	}
	return "labels"
}

func (r *labelSQLRepositoryImpl) Insert(ctx context.Context, issueID, label, actor string, opts domain.LabelOpts) error {
	if issueID == "" {
		return fmt.Errorf("db: LabelSQLRepository.Insert: issueID must not be empty")
	}
	if label == "" {
		return fmt.Errorf("db: LabelSQLRepository.Insert: label must not be empty")
	}
	// Reject an over-length label before the INSERT IGNORE, which would otherwise
	// silently truncate it to the VARCHAR(255) column. This is the proxied-server
	// (uow) analog of issueops.AddLabelInTx's guard, so both write stacks return a
	// typed ErrFieldTooLong instead of storing a label the caller never sent.
	if err := types.CheckFieldLen("label", label); err != nil {
		return err
	}
	table := pickLabelTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	if _, err := r.runner.ExecContext(ctx,
		fmt.Sprintf("INSERT IGNORE INTO %s (issue_id, label) VALUES (?, ?)", table),
		issueID, label,
	); err != nil {
		return fmt.Errorf("db: LabelSQLRepository.Insert %s/%s: %w", issueID, label, err)
	}
	return r.events.Record(ctx, domain.Event{
		IssueID:  issueID,
		Type:     types.EventLabelAdded,
		Actor:    actor,
		NewValue: label,
	}, domain.RecordEventOpts{UseWispsTable: opts.UseWispsTable})
}

func (r *labelSQLRepositoryImpl) Delete(ctx context.Context, issueID, label, actor string, opts domain.LabelOpts) error {
	if issueID == "" {
		return fmt.Errorf("db: LabelSQLRepository.Delete: issueID must not be empty")
	}
	if label == "" {
		return fmt.Errorf("db: LabelSQLRepository.Delete: label must not be empty")
	}
	table := pickLabelTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	if _, err := r.runner.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? AND label = ?", table),
		issueID, label,
	); err != nil {
		return fmt.Errorf("db: LabelSQLRepository.Delete %s/%s: %w", issueID, label, err)
	}
	return r.events.Record(ctx, domain.Event{
		IssueID:  issueID,
		Type:     types.EventLabelRemoved,
		Actor:    actor,
		OldValue: label,
	}, domain.RecordEventOpts{UseWispsTable: opts.UseWispsTable})
}

func (r *labelSQLRepositoryImpl) List(ctx context.Context, issueID string, opts domain.LabelOpts) ([]string, error) {
	if issueID == "" {
		return nil, fmt.Errorf("db: LabelSQLRepository.List: issueID must not be empty")
	}
	table := pickLabelTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	rows, err := r.runner.QueryContext(ctx,
		fmt.Sprintf("SELECT label FROM %s WHERE issue_id = ? ORDER BY label", table),
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: LabelSQLRepository.List %s: %w", issueID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("db: LabelSQLRepository.List: scan: %w", err)
		}
		out = append(out, label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: LabelSQLRepository.List: rows: %w", err)
	}
	return out, nil
}

func (r *labelSQLRepositoryImpl) ListByIssueIDs(ctx context.Context, issueIDs []string, opts domain.LabelOpts) (map[string][]string, error) {
	result := make(map[string][]string)
	if len(issueIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(issueIDs))
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	table := pickLabelTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	q := fmt.Sprintf(
		"SELECT issue_id, label FROM %s WHERE issue_id IN (%s) ORDER BY issue_id, label",
		table, strings.Join(placeholders, ","),
	)
	rows, err := r.runner.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: LabelSQLRepository.ListByIssueIDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var issueID, label string
		if err := rows.Scan(&issueID, &label); err != nil {
			return nil, fmt.Errorf("db: LabelSQLRepository.ListByIssueIDs: scan: %w", err)
		}
		result[issueID] = append(result[issueID], label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: LabelSQLRepository.ListByIssueIDs: rows: %w", err)
	}
	return result, nil
}

func (r *labelSQLRepositoryImpl) DeleteAllForIDs(ctx context.Context, ids []string, opts domain.LabelOpts) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	table := "labels"
	if opts.UseWispsTable {
		table = "wisp_labels"
	}
	total := 0
	for start := 0; start < len(ids); start += deleteBatchSize {
		end := start + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		//nolint:gosec // G201: table is one of two hardcoded constants; ? placeholders only.
		res, err := r.runner.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE issue_id IN (%s)", table, strings.Join(placeholders, ",")),
			args...)
		if err != nil {
			if opts.UseWispsTable && dberrors.IsTableNotExist(err) {
				return total, nil
			}
			return total, fmt.Errorf("db: LabelSQLRepository.DeleteAllForIDs from %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("db: LabelSQLRepository.DeleteAllForIDs rows affected: %w", err)
		}
		total += int(n)
	}
	return total, nil
}

func (r *labelSQLRepositoryImpl) CountAllForIDs(ctx context.Context, ids []string, opts domain.LabelOpts) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	table := "labels"
	if opts.UseWispsTable {
		table = "wisp_labels"
	}
	count, err := issueops.CountRowsForIssueIDsInTx(ctx, r.runner, table, ids)
	if err != nil {
		if opts.UseWispsTable && dberrors.IsTableNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("db: LabelSQLRepository.CountAllForIDs: %w", err)
	}
	return count, nil
}
