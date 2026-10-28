package issueops

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// UnversionableIssue names one issue that RecordVersionInTx would refuse to
// version, and the refusal it would return. Field is the top-level field of the
// issue the refusal was found in ("metadata", "timeout"), empty when the refusal
// does not name one.
type UnversionableIssue struct {
	ID    string
	Field string
	Err   error
}

// FindUnversionable runs CheckIssueVersionable over every issue in issues that
// the mint would version and returns the ones it refuses, in the order they were
// given. It is the core of the check made when versioned history is switched on:
// with history on, a write that introduces a value the mint refuses aborts in its
// own transaction, but a row that already holds one -- written while history was
// off, or by a path that does not mint -- would, if it participates in history,
// fail every later write to it. The check is over the whole issue, as the mint's
// is, and not over its metadata alone: a gate's timeout past 2^53-1 nanoseconds is
// refused too.
//
// It skips the rows the mint never versions by IsWisp: ephemeral and no-history
// rows are never versioned, so a value they hold can never abort a write to
// them. It cannot skip legacy rows itself, because participation is not on
// types.Issue: what it returns is the set of rows the mint WOULD refuse if they
// participate. The switch narrows that set with ParticipatingIssueIDsInTx,
// because design §16.2b's write fence skips a legacy row whatever it holds, so
// only a row that participates can make a write to it fail; it keeps a legacy
// row only when it is a template, whose values a pour or bond copies into new
// rows that the mint does version and which bd update cannot change (any other
// legacy row of a proto gets a warning). A caller that cannot read participation
// keeps the whole set, which is an over-approximation in the safe direction: it
// can only refuse more. It does no I/O; the caller loads the rows, which is what
// lets one function serve the direct store and the proxied route.
func FindUnversionable(issues []*types.Issue) []UnversionableIssue {
	var found []UnversionableIssue
	for _, issue := range issues {
		if issue == nil || IsWisp(issue) {
			continue
		}
		if err := CheckIssueVersionable(issue); err != nil {
			var named *refusedFieldError
			field := ""
			if errors.As(err, &named) {
				field = named.field
			}
			found = append(found, UnversionableIssue{ID: issue.ID, Field: field, Err: err})
		}
	}
	return found
}

// ParticipatingIssueIDsInTx returns the subset of ids that name an issues-table
// row whose participation_generation is set: the rows whose participation the
// write fence reads (design §16.2b). Combined with FindUnversionable's IsWisp
// filter (a no-history row on the issues table can carry participation, but the
// mint skips it), they are exactly the rows an update-shaped mint versions, and so
// the only rows a value they hold can make every later write to them fail for. A
// legacy row (participation_generation NULL) and a wisps-table row are absent,
// because the fence or the plane rule makes the mint skip a write to them whatever
// they hold, and so is an id that names no row. (A pour or bond that copies a
// legacy row's value into a new row is another matter: the new row is versioned.
// The switch finds the rows a pour or bond copies itself.)
//
// It reads the column the fence reads, from the table the fence reads it from,
// and nothing else, and it runs whether or not history is on for tx: the switch
// asks before it turns history on. It is the pre-enable scan's second read
// (FindUnversionable gives it the ids), so it is asked about the offenders alone,
// which in a clean store is no query at all. The ids are deduplicated and read in
// batches of queryBatchSize.
func ParticipatingIssueIDsInTx(ctx context.Context, tx DBTX, ids []string) (map[string]struct{}, error) {
	participating := make(map[string]struct{})
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	for start := 0; start < len(unique); start += queryBatchSize {
		end := min(start+queryBatchSize, len(unique))
		if err := participatingIssueIDBatchInTx(ctx, tx, unique[start:end], participating); err != nil {
			return nil, err
		}
	}
	return participating, nil
}

func participatingIssueIDBatchInTx(ctx context.Context, tx DBTX, batch []string, participating map[string]struct{}) error {
	placeholders, args := sqlbuild.InPlaceholders(batch)
	//nolint:gosec // G202: placeholders is only "?" markers; every id is a bound argument
	rows, err := tx.QueryContext(ctx,
		"SELECT id FROM issues WHERE participation_generation IS NOT NULL AND id IN ("+placeholders+")", args...)
	if err != nil {
		return fmt.Errorf("versioned history: read participation_generation: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("versioned history: scan participation_generation: %w", err)
		}
		participating[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("versioned history: read participation_generation: %w", err)
	}
	return nil
}
