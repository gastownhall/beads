package versioncontrolops

import (
	"context"
	"fmt"
)

// DoltGC runs Dolt's default, generational garbage collection to reclaim disk
// space. Archive level 0 writes classic Snappy table files instead of zstd
// archives.
//
// Dolt storage is generational: every GC promotes the chunks reachable at that
// moment into the old generation, and a default pass only visits the new
// generation. Data that survived an earlier GC is therefore never revisited,
// so this pass reclaims only what was written since the last one. Use
// DoltGCFull after a history rewrite, which orphans chunks an earlier GC
// already promoted.
//
// conn must be a non-transactional database connection since
// DOLT_GC cannot run inside an explicit transaction.
func DoltGC(ctx context.Context, conn DBConn) error {
	return doltGC(ctx, conn, false)
}

// DoltGCFull runs a full Dolt garbage collection, collecting both the old and
// the new generation. Its reclaim set is a strict superset of DoltGC's: it
// frees everything a default pass frees, plus data an earlier GC promoted to
// the old generation. If a full pass frees nothing, the remaining bytes are
// still referenced.
//
// Full collections rewrite the old generation, so they cost more than a
// default pass — minutes on multi-gigabyte stores. Callers should reserve it
// for history rewrites (flatten, compact) and explicit opt-in.
//
// conn must be a non-transactional database connection since
// DOLT_GC cannot run inside an explicit transaction.
func DoltGCFull(ctx context.Context, conn DBConn) error {
	return doltGC(ctx, conn, true)
}

func doltGC(ctx context.Context, conn DBConn, full bool) error {
	stmt := "CALL DOLT_GC('--archive-level', '0')"
	if full {
		stmt = "CALL DOLT_GC('--full', '--archive-level', '0')"
	}
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("dolt gc: %w", err)
	}
	return nil
}
