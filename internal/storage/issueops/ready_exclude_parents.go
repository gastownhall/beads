package issueops

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// GetOpenChildParentIDsInTx returns the DISTINCT, sorted ids of the parents
// resident in mainTable's family ("issues" or "wisps") that have at least one
// recorded non-closed child — the close gate's open-children set
// (countOpenChildrenForTargetInTx) in projection form
// (sqlbuild.OpenChildParentIDsSQL). A WorkFilter.ExcludeParentsWithOpenChildren
// ready read excludes exactly these parents, so `bd ready --exclude-parents`
// and `bd close`'s open-children refusal can never disagree about which
// children count: both child families, pinned children included, the child's
// own readiness or visibility in the ready query irrelevant.
//
// Exported so the domain/db stack resolves the identical set (Seam A parity);
// each ready read resolves it once per family, then folds it into
// sqlbuild.BuildReadyWorkWhere through ReadyWorkWhereInputs.OpenChildParentIDs.
//
// The combined union names the optional wisp tables, so a database that
// legitimately lacks the wisp plane falls back to the durable leg alone —
// exactly where the close gate itself tolerates the missing table.
//
//nolint:gosec // G201: mainTable is validated against the two family names below.
func GetOpenChildParentIDsInTx(ctx context.Context, tx DBTX, mainTable string) ([]string, error) {
	if mainTable != "issues" && mainTable != "wisps" {
		return nil, fmt.Errorf("open-child parents: unsupported family table %q", mainTable)
	}
	ids, err := queryIDs(ctx, tx, sqlbuild.OpenChildParentIDsSQL(mainTable))
	if err == nil {
		sort.Strings(ids)
		return ids, nil
	}
	if !missingOptionalWispTable(err) {
		return nil, fmt.Errorf("open-child parents in %s: %w", mainTable, err)
	}
	// A pre-migration database's wisp plane is absent: the durable leg alone
	// answers, mirroring countOpenChildrenForTargetInTx's tolerance there.
	ids, err = queryIDs(ctx, tx, sqlbuild.DurableOpenChildParentIDsSQL(mainTable))
	if err != nil {
		return nil, fmt.Errorf("open-child parents in %s: %w", mainTable, err)
	}
	sort.Strings(ids)
	return ids, nil
}
