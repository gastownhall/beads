package schema

import (
	"context"
	"fmt"
)

// DiagnoseDroppedDefaultDrift reports the recorded dropped-default
// post-conditions whose DDL never took effect (GH#5269): the marks-say-v53 /
// DDL-says-v49 state a guarded PREPARE/EXECUTE that reported success without
// applying leaves behind, and that no later migration pass can heal, because
// the recorded mark skips the body. It is the read-only probe behind `bd
// doctor`'s Migration Post-Conditions check: nothing is corrected, and no
// cursor, table or commit is written — the explicit repair lives in
// RepairDroppedDefaultDrift (`bd migrate schema`).
//
// Obligations are re-derived from what the cursors RECORD, each series read
// independently; a cursor table that does not exist records nothing, so a
// fresh store owes no post-conditions. A store whose marks never got ahead
// of its DDL returns no violations. Any read that fails — an unreadable
// cursor, an unprobeable column — returns an error for the caller to surface
// as could-not-check: a broken query behind a check must not read as health
// (the bd-6dnrw.27 lesson).
func DiagnoseDroppedDefaultDrift(ctx context.Context, db DBConn) ([]string, error) {
	recorded := make(map[string]int)
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		version, exists, err := recordedCursorVersion(ctx, db, src.cursorTable)
		if err != nil {
			return nil, err // already names the cursor table
		}
		if exists {
			recorded[src.cursorTable] = version
		}
	}
	obligations := coveredDroppedDefaultObligations(recorded)
	if len(obligations) == 0 {
		return nil, nil
	}
	drifts, err := probeDroppedDefaultDrifts(ctx, db, obligations)
	if err != nil {
		return nil, err
	}
	if len(drifts) == 0 {
		return nil, nil
	}
	violations := make([]string, 0, len(drifts))
	for _, d := range drifts {
		violations = append(violations, fmt.Sprintf("%s.%s (v%04d, %s): DEFAULT %s still present",
			d.obligation.table, d.obligation.column, d.obligation.version, d.obligation.cursorTable, d.observed.String))
	}
	return violations, nil
}
