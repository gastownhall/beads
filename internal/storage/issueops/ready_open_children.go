package issueops

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// openChildParentIDsInTx returns, deduplicated and sorted, the rows of one
// parent family (tables.Main) that have at least one open child: the parents
// countOpenChildrenForTargetInTx would count above zero through the family's
// own target column, read for the whole family at once instead of one bound
// parent. Both child families are read; a database without the wisp plane has
// no wisp children to read, exactly as the close gate tolerates.
//
// It is a pure read. The close gate's coordination-cell touch is a write and
// belongs to closing, never to listing ready work.
func openChildParentIDsInTx(ctx context.Context, tx DBTX, tables FilterTables) ([]string, error) {
	targetColumn := sqlbuild.ParentTargetColumn(tables)
	seen := make(map[string]struct{})
	for _, family := range sqlbuild.OpenChildFamilies {
		query, err := sqlbuild.OpenChildParentIDsSQL(family, targetColumn)
		if err != nil {
			return nil, err
		}
		ids, err := queryIDs(ctx, tx, query)
		if err != nil {
			if family.DedupeDurable && missingOptionalWispTable(err) {
				continue
			}
			return nil, fmt.Errorf("open-child parents of %s from %s: %w", tables.Main, family.Dependencies, err)
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// setOpenChildParentIDsInTx resolves the open-child parent set of each family
// in families into inputs, once, and only when filter asks for the exclusion:
// a zero-valued filter issues no statement here.
func setOpenChildParentIDsInTx(ctx context.Context, tx DBTX, excludeParents bool, inputs *sqlbuild.ReadyWorkWhereInputs, families ...FilterTables) error {
	if !excludeParents {
		return nil
	}
	for _, tables := range families {
		ids, err := openChildParentIDsInTx(ctx, tx, tables)
		if err != nil {
			return fmt.Errorf("get ready work: %w", err)
		}
		if inputs.OpenChildParentIDs == nil {
			inputs.OpenChildParentIDs = make(map[string][]string, 2)
		}
		inputs.OpenChildParentIDs[tables.Main] = ids
	}
	return nil
}
