package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// CLI-route pull settlement (GH#7839).
//
// A git-protocol, credentialed or cloud-auth pull runs `dolt pull` as a
// subprocess in the server's own database directory. That subprocess is an
// ordinary client of the running sql-server, and on a conflict it does what
// git does: it leaves the conflicted merge open on the branch's working set.
// The SQL route instead rolls back the transaction that ran DOLT_PULL, and the
// embedded route aborts the merge (versioncontrolops.SettleMerge). Left open,
// the merge blocks every later write and pull on the database, because Dolt
// refuses an autocommit transaction over a conflicted working set.
//
// So the CLI route settles the same way the other two do: the safe conflict
// classes are auto-resolved as before, and a merge the resolver declined is
// aborted with DOLT_MERGE('--abort') on one pinned session, its conflicts
// captured first and returned as a MergeConflictsError. Only an abort is ever
// issued: never DOLT_RESET('--hard'), since nothing sampled before a subprocess
// on a shared server can prove a reset would destroy nothing but the merge.

// cliMergeState is the merge state of the store's branch as one session reads
// it: dolt_merge_status plus the row conflicts and the non-row blockers
// (schema conflicts, constraint violations) that keep a merge from concluding.
type cliMergeState struct {
	merging   bool
	source    string
	target    string
	conflicts []storage.Conflict
	blockers  storage.MergeBlockers
}

// unsettled reports whether anything a pull could have left behind is open:
// an active merge, row conflicts, or merge blockers.
func (m cliMergeState) unsettled() bool {
	return m.merging || len(m.conflicts) > 0 || m.blockers.Blocked()
}

// tables names every table carrying a row conflict, a schema conflict or a
// constraint violation, in that order, each once.
func (m cliMergeState) tables() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, c := range m.conflicts {
		add(c.Field)
	}
	for _, t := range m.blockers.SchemaConflictTables {
		add(t)
	}
	for _, v := range m.blockers.ConstraintViolations {
		add(v.Table)
	}
	return out
}

// readCLIMergeState reads the merge state through db, which must be one
// session (a *sql.Conn or *sql.Tx): dolt_merge_status and dolt_conflicts are
// per-session views of the branch's working set.
func readCLIMergeState(ctx context.Context, db versioncontrolops.DBConn) (cliMergeState, error) {
	var st cliMergeState
	var source, target sql.NullString
	err := db.QueryRowContext(ctx, "SELECT is_merging, source, target FROM dolt_merge_status").
		Scan(&st.merging, &source, &target)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, fmt.Errorf("read dolt_merge_status: %w", err)
	}
	st.source, st.target = source.String, target.String
	conflicts, err := versioncontrolops.GetConflicts(ctx, db)
	if err != nil {
		return st, err
	}
	st.conflicts = conflicts
	blockers, err := versioncontrolops.GetMergeBlockers(ctx, db)
	if err != nil {
		return st, err
	}
	st.blockers = blockers
	return st, nil
}

// readStoreMergeState reads the merge state of the store's branch on a
// connection from the store pool: those connections are on the branch the CLI
// `dolt pull` merged into (see autoResolveConflictsAfterCLIPull).
func (s *DoltStore) readStoreMergeState(ctx context.Context) (cliMergeState, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return cliMergeState{}, fmt.Errorf("acquire connection to read merge state: %w", err)
	}
	defer conn.Close()
	return readCLIMergeState(ctx, conn)
}

// pullMergeStateError reports merge state a pull will not settle on its own.
// It never claims the working set was restored.
type pullMergeStateError struct {
	remote string // empty when the refusal came before a remote was chosen
	state  cliMergeState
}

func (e *pullMergeStateError) Error() string {
	what := "a merge"
	if e.state.source != "" {
		what = "a merge of " + e.state.source
	}
	detail := ""
	if tables := e.state.tables(); len(tables) > 0 {
		detail = " with conflicts in " + strings.Join(tables, ", ")
	}
	from := ""
	if e.remote != "" {
		from = " from " + e.remote
	}
	return fmt.Sprintf("cannot pull%s: %s is already open on this database%s; "+
		"bd will not pull over it or abort a merge it did not start. "+
		"Inspect it with 'bd conflicts list', then resolve and commit it, "+
		`or abandon it with: bd sql "CALL DOLT_MERGE('--abort')"`, from, what, detail)
}

// refuseOpenConflictedMerge stops a pull before it stages anything when the
// branch already carries an unconcluded merge with row conflicts, schema
// conflicts or constraint violations (GH#7839): committing over it fails with
// an opaque "tables are in conflict", and a pull must never abort a merge it
// did not start. A merge with nothing left to resolve is the pre-pull commit's
// to conclude, as before. The read is a diagnosis only: when it fails, the
// pull proceeds and reports whatever the commit or transport reports.
func (s *DoltStore) refuseOpenConflictedMerge(ctx context.Context) error {
	st, err := s.readStoreMergeState(ctx)
	if err != nil {
		return nil //nolint:nilerr // diagnosis only; never a gate
	}
	if len(st.conflicts) > 0 || st.blockers.Blocked() {
		return &pullMergeStateError{state: st}
	}
	return nil
}

// runCLIPull runs one CLI-route pull (pull is the doltCLIPull or
// doltCLIPullFromPeer call) and settles what it left on the branch. All five
// CLI pull call sites go through it. A read-only store cannot resolve, abort
// or commit, so it surfaces the transport's result as-is. A merge already open
// before the transport is reported without running it: what the pull leaves
// behind could not be told apart from it.
func (s *DoltStore) runCLIPull(ctx context.Context, remote string, pull func() error) error {
	if s.readOnly {
		return pull()
	}
	if pre, err := s.readStoreMergeState(ctx); err == nil && pre.merging {
		return &pullMergeStateError{remote: remote, state: pre}
	}
	return s.settleCLIPull(ctx, remote, pull())
}

// settleCLIPull settles a CLI pull whose transport returned pullErr. A pull
// that fails because of conflicts is recoverable once they resolve, so the
// branch's state is inspected whatever pullErr is.
func (s *DoltStore) settleCLIPull(ctx context.Context, remote string, pullErr error) error {
	post, err := s.readStoreMergeState(ctx)
	if err == nil && !post.unsettled() {
		// Nothing open: the transport's own result stands.
		return pullErr
	}

	resolved, resolveErr := s.autoResolveConflictsAfterCLIPull(ctx)
	if errors.Is(resolveErr, storage.ErrCommitIndeterminate) {
		// The settlement commit may have landed; aborting now could undo a
		// concluded merge. Keep today's error selection and touch nothing.
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}

	// The resolver's boolean is not trusted on its own: its FK-repair-only
	// path can report true over row conflicts it never resolved, so the
	// branch is re-read before anything is called settled.
	remaining, remErr := s.readStoreMergeState(ctx)
	if remErr == nil && !remaining.unsettled() {
		if resolveErr != nil {
			if pullErr != nil {
				return pullErr
			}
			return resolveErr
		}
		if pullErr != nil && !resolved {
			return pullErr
		}
		// A successful resolution suppresses the transport's conflict exit.
		return nil
	}
	return s.abortCLIPullMerge(ctx, remote, pullErr, resolveErr)
}

// abortCLIPullMerge captures the conflicts of the merge a CLI pull left open,
// aborts it, and verifies the abort, all on one pinned session.
func (s *DoltStore) abortCLIPullMerge(ctx context.Context, remote string, pullErr, resolveErr error) error {
	mergeErr := pullErr
	if mergeErr == nil {
		mergeErr = resolveErr
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pull from %s left a conflicted merge open and bd could not abort it: acquire connection: %w (pull error: %v)", remote, err, mergeErr)
	}
	defer conn.Close()

	captured, _ := readCLIMergeState(ctx, conn)
	abortErr := schema.DrainCall(ctx, conn, "CALL DOLT_MERGE('--abort')")
	if abortErr != nil {
		return fmt.Errorf("pull from %s left a conflicted merge open and bd could not abort it: %w (pull error: %v)", remote, abortErr, mergeErr)
	}
	if len(captured.conflicts) > 0 {
		return &versioncontrolops.MergeConflictsError{Conflicts: captured.conflicts, MergeErr: mergeErr}
	}
	return fmt.Errorf("pull from %s left %s that bd cannot auto-resolve; merge aborted and working set restored: %w",
		remote, describeMergeBlockers(captured), errOrPlaceholder(mergeErr))
}

// describeMergeBlockers names a restored merge's non-row blockers.
func describeMergeBlockers(st cliMergeState) string {
	var parts []string
	if len(st.blockers.SchemaConflictTables) > 0 {
		parts = append(parts, "schema conflicts in "+strings.Join(st.blockers.SchemaConflictTables, ", "))
	}
	if len(st.blockers.ConstraintViolations) > 0 {
		var tables []string
		for _, v := range st.blockers.ConstraintViolations {
			tables = append(tables, v.Table)
		}
		parts = append(parts, "constraint violations in "+strings.Join(tables, ", "))
	}
	if len(parts) == 0 {
		return "an unconcluded merge"
	}
	return strings.Join(parts, " and ")
}

// errOrPlaceholder keeps a %w wrap well-formed when the transport reported no
// error of its own.
func errOrPlaceholder(err error) error {
	if err != nil {
		return err
	}
	return errors.New("dolt pull reported no error")
}
