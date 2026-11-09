package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The authorized half of the offline child-id collision repair (GH#4796,
// bd-28sb25): `bd dolt pull --renumber-children`. The read-only half
// (PlanChildRenumbers, renumber.go) computes what collides; this file renames
// the local copies above both clones' high-water marks, commits the rename
// once, and merges the fetched remote revision through normal settlement.
//
// The repair is all-or-nothing per invocation: the renames run in one SQL
// transaction — a failure anywhere rolls the whole rename back, so nothing
// persists and no rename commit exists (AC-7) — and once committed the rename
// is never replayed: the merge that follows either lands or leaves the rename
// commit in history with the partial result reporting what was renamed.

// TxSession is the connection contract the repair needs beyond DBConn: the
// rename transaction must open on the SAME pinned session the fetch, the plan
// reads, and the merge run on, or the head revalidation and the settle
// machinery's session flags would describe different connections. *sql.Conn
// satisfies it; both Dolt stores pin one for the pull path.
type TxSession interface {
	DBConn
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// renumberDirtyTables are the synced tables the rename transaction can dirty.
// The dolt_ignore'd planes the rename also touches (events, leases,
// wisp_dependencies, the journal) are deliberately absent: they cannot be
// staged, and their rows are clone-local so the merge never sees them.
var renumberDirtyTables = map[string]bool{
	"issues":               true,
	"dependencies":         true,
	"labels":               true,
	"comments":             true,
	"issue_snapshots":      true,
	"compaction_snapshots": true,
	"provenance_events":    true,
	"child_counters":       true,
}

// fetchFromRemote fetches branch from remote on db — the fetch half of Pull,
// split out so the repair fetches exactly once and pins what it merges. user
// is the remote credential ("" leaves Dolt's own auth untouched).
func fetchFromRemote(ctx context.Context, db DBConn, remote, branch, user string) error {
	return withRemoteEnvGuards(func() error {
		if user != "" {
			_, err := db.ExecContext(ctx, "CALL DOLT_FETCH('--user', ?, ?, ?)", user, remote, branch)
			return err
		}
		_, err := db.ExecContext(ctx, "CALL DOLT_FETCH(?, ?)", remote, branch)
		return err
	})
}

// PullWithChildRenumbers is the authorized repair for `bd dolt pull
// --renumber-children`: fetch branch from remote, recompute and revalidate the
// repair plan, rename the colliding local children in one transaction, commit
// the rename, then merge the fetched remote revision through normal
// settlement. db must be a pinned single session (TxSession); author follows
// Dolt's --author form and names the actor on the rename and its commit.
//
// The result is nil when no collision needed repairing — the pull then
// behaved exactly like a plain one, and its success or failure is the merge's
// own (AC-8). On a merge that still fails after the renames, the result is
// returned non-nil WITH the error: the rename commit is retained, and its
// mappings and hash are the partial result the retry reports instead of
// renaming again.
func PullWithChildRenumbers(ctx context.Context, db DBConn, remote, branch, user, author string) (*storage.ChildRenumberResult, error) {
	if err := fetchFromRemote(ctx, db, remote, branch, user); err != nil {
		return nil, fmt.Errorf("fetch from %s/%s: %w", remote, branch, err)
	}
	return PullWithChildRenumbersFromFetched(ctx, db, remote, branch, author)
}

// PullWithChildRenumbersFromFetched is PullWithChildRenumbers for a caller
// that has already fetched remote/branch through its own transport (the server
// store routes the fetch itself); the plan is computed against whatever the
// fetch left in the tracking ref, and the merge targets that same revision.
func PullWithChildRenumbersFromFetched(ctx context.Context, db DBConn, remote, branch, author string) (*storage.ChildRenumberResult, error) {
	plan, err := PlanChildRenumbers(ctx, db, branch, remote)
	if err != nil {
		return nil, err
	}
	if !plan.Empty() {
		return applyAndMergeChildRenumbers(ctx, db, plan, branch, author)
	}
	// No eligible collision: no identity changes, no rename commit — normal
	// pull settlement alone decides success or failure (AC-8).
	if err := MergeAndSettle(ctx, db, remote+"/"+branch); err != nil {
		return nil, err
	}
	return nil, nil
}

// applyAndMergeChildRenumbers executes one revalidated plan: rename the local
// colliding children in one transaction, commit the rename, then merge the
// pinned remote revision through normal settlement.
func applyAndMergeChildRenumbers(ctx context.Context, db DBConn, plan *storage.ChildRenumberPlan, branch, author string) (*storage.ChildRenumberResult, error) {
	// Revalidate the plan against the live branch: the plan was computed from
	// dolt_branches reads, and any commit that landed since invalidates it —
	// the collisions it mapped may no longer exist or may collide differently
	// (AC-7's invalidated-plan decline).
	headNow, err := branchHashAsOf(ctx, db, branch)
	if err != nil {
		return nil, err
	}
	if headNow != plan.LocalHead {
		return nil, fmt.Errorf("renumber: branch %s moved since the repair plan was computed (%s -> %s); re-run the pull",
			branch, plan.LocalHead, headNow)
	}
	// Refuse an active merge: a halted merge's working set is mid-resolution
	// state the rename must not commit on top of.
	merging, err := mergeInProgress(ctx, db)
	if err != nil {
		return nil, err
	}
	if merging {
		return nil, fmt.Errorf("renumber: a merge is already in progress; resolve or abort it before renumbering children")
	}

	session, ok := db.(TxSession)
	if !ok {
		return nil, fmt.Errorf("renumber: the connection does not support the rename transaction; the repair needs a pinned session")
	}
	tx, err := session.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("renumber: begin rename transaction: %w", err)
	}
	if err := applyChildRenumbersInTx(ctx, tx, plan, author); err != nil {
		// The transaction's writes die with it: no issue, reference, or
		// counter rename survives a mid-repair failure (AC-7).
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("renumber: commit rename transaction: %w", err)
	}

	// The rename commit carries the mapping in its message, and stages only
	// the tables the rename dirtied — nothing unrelated is swept in.
	commitMsg := childRenumberCommitMessage(plan)
	if err := StageAndCommit(ctx, db, renumberDirtyTables, commitMsg, author); err != nil {
		return nil, fmt.Errorf("renumber: commit renames: %w", err)
	}
	renameCommit, err := branchHashAsOf(ctx, db, branch)
	if err != nil {
		return nil, err
	}

	result := &storage.ChildRenumberResult{
		Plan:         plan,
		RenameCommit: renameCommit,
	}
	// Merge the pinned revision — the tracking ref the plan read, untouched by
	// any second fetch. The rename commit is already in history, so a failure
	// here retains it: the error and the partial result travel together.
	if err := MergeAndSettle(ctx, db, plan.Remote+"/"+branch); err != nil {
		return result, err
	}
	return result, nil
}

// childRenumberCommitMessage is the rename commit's message: one human summary
// line and the mapping, so history shows exactly which identities moved.
func childRenumberCommitMessage(plan *storage.ChildRenumberPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "bd: renumber local children before merging %s (GH#4796)\n", plan.RemoteRef)
	for _, m := range plan.Mappings {
		fmt.Fprintf(&b, "renumber: %s -> %s\n", m.OldID, m.NewID)
	}
	return b.String()
}

// applyChildRenumbersInTx applies the plan's renames in one transaction:
// mappings are already ordered roots-first with each root's subtree after it,
// so a rename never touches an id a later mapping still needs under its old
// name. Every mapped issue is renamed through issueops.UpdateIssueIDInTx with
// unchanged text, which retains the retirement checks and carries edges,
// leases, and the audit journal. The rename's own follow-through runs with
// each mapping, inside the same transaction: the issue's child-counter row —
// the high-water mark its future children mint from — moves to the new id
// with its value preserved, and the synced aux rows that followed the issue
// (comments, snapshots) get their content-derived ids re-derived from the new
// issue id. After the renames, every allocation parent's child counter is
// raised to the planned high-water mark, so the next local create starts
// above the collision instead of re-minting a colliding suffix.
func applyChildRenumbersInTx(ctx context.Context, tx *sql.Tx, plan *storage.ChildRenumberPlan, actor string) error {
	// Refuse unsupported versioned-history cases before any mutation: the
	// rename does not migrate issue_versions rows (the table has no FK to
	// issues), so an issue carrying versions would keep them under its old id.
	for _, m := range plan.Mappings {
		var versions int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", m.OldID).Scan(&versions); err != nil {
			return fmt.Errorf("renumber: check versioned history for %s: %w", m.OldID, err)
		}
		if versions > 0 {
			return fmt.Errorf("renumber: %s carries %d issue_versions row(s); renaming an issue with versioned history is not supported", m.OldID, versions)
		}
	}
	for _, m := range plan.Mappings {
		issue, err := readIssueTextForRename(ctx, tx, m.OldID)
		if err != nil {
			return err
		}
		// Capture the issue's own counter BEFORE the rename: the ignored
		// counter migration's ON UPDATE CASCADE may move the row while the
		// rename runs, so reading it after would be reading the moved row,
		// not capturing what to preserve.
		counter, hasCounter, err := capturedChildCounterInTx(ctx, tx, m.OldID)
		if err != nil {
			return err
		}
		if err := issueops.UpdateIssueIDInTx(ctx, tx, m.OldID, m.NewID, issue, actor); err != nil {
			return fmt.Errorf("renumber: rename %s -> %s: %w", m.OldID, m.NewID, err)
		}
		if hasCounter {
			if err := relocateChildCounterInTx(ctx, tx, m.OldID, m.NewID, counter); err != nil {
				return err
			}
		}
		// The aux rows followed the issue by FK cascade; their content-derived
		// ids must be re-derived from the new issue id or they stay pinned to
		// the old one the other clone still holds.
		if err := issueops.RekeyAuxRowsForIssueInTx(ctx, tx, m.NewID); err != nil {
			return fmt.Errorf("renumber: rename %s -> %s: %w", m.OldID, m.NewID, err)
		}
	}
	// The floors name parents as the PLAN saw them, and a parent that was
	// itself renamed mints its future children under the new id: remap each
	// floor key through the mappings before raising, or the raise would try
	// to seed a counter row under an id the rename just vacated (its row
	// followed the issue, and the issues row is gone).
	floors := make(map[string]int64, len(plan.CounterFloors))
	renamed := make(map[string]string, len(plan.Mappings))
	for _, m := range plan.Mappings {
		renamed[m.OldID] = m.NewID
	}
	for parent, floor := range plan.CounterFloors {
		if newParent, ok := renamed[parent]; ok {
			parent = newParent
		}
		floors[parent] = floor
	}
	return raiseChildCounterFloorsInTx(ctx, tx, floors)
}

// capturedChildCounterInTx reads an issue's own child-counter row before its
// rename: the high-water mark its future children mint from. An issue without
// a row — its children predate the counter backfill, or it never had any —
// reports hasCounter false and relocates nothing.
func capturedChildCounterInTx(ctx context.Context, tx *sql.Tx, issueID string) (int64, bool, error) {
	var last int64
	err := tx.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", issueID).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("renumber: read child counter for %s: %w", issueID, err)
	}
	return last, true, nil
}

// relocateChildCounterInTx moves a renamed issue's own counter row to its new
// id with the captured high-water preserved, and verifies the postcondition:
// the row lives ONLY under the new id with the same value, none under the
// old. The UPDATE normally affects 0 rows — the ignored counter migration
// restores an ON UPDATE CASCADE that moves the row when the issues row is
// renamed — but a schema without that FK leaves the row behind, and the
// UPDATE is what moves it. Either way the verification is the contract; a
// mismatch fails the transaction and with it the whole repair.
func relocateChildCounterInTx(ctx context.Context, tx *sql.Tx, oldID, newID string, captured int64) error {
	if _, err := tx.ExecContext(ctx,
		"UPDATE child_counters SET parent_id = ? WHERE parent_id = ?", newID, oldID); err != nil {
		return fmt.Errorf("renumber: move child counter %s -> %s: %w", oldID, newID, err)
	}
	var got sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", newID).Scan(&got); err != nil {
		return fmt.Errorf("renumber: read child counter for %s: %w", newID, err)
	}
	if !got.Valid || got.Int64 != captured {
		return fmt.Errorf("renumber: child counter for %s = %d after rename, want the preserved high-water %d",
			newID, got.Int64, captured)
	}
	var stale int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", oldID).Scan(&stale); err != nil {
		return fmt.Errorf("renumber: check child counter for %s: %w", oldID, err)
	}
	if stale > 0 {
		return fmt.Errorf("renumber: %d child counter row(s) remain for %s after the rename", stale, oldID)
	}
	return nil
}

// readIssueTextForRename reads the identity-carrying text UpdateIssueIDInTx
// rewrites, to pass it back unchanged: the repair moves ids, never content.
func readIssueTextForRename(ctx context.Context, tx *sql.Tx, id string) (*types.Issue, error) {
	var title string
	var description, design, acceptanceCriteria, notes sql.NullString
	err := tx.QueryRowContext(ctx,
		"SELECT title, description, design, acceptance_criteria, notes FROM issues WHERE id = ?", id).
		Scan(&title, &description, &design, &acceptanceCriteria, &notes)
	if err != nil {
		return nil, fmt.Errorf("renumber: read %s for rename: %w", id, err)
	}
	return &types.Issue{
		Title:              title,
		Description:        description.String,
		Design:             design.String,
		AcceptanceCriteria: acceptanceCriteria.String,
		Notes:              notes.String,
	}, nil
}

// raiseChildCounterFloorsInTx raises each allocation parent's counter to the
// planned high-water mark (never lowers it). A parent with no counter row —
// possible when its children predate the counter backfill or arrived by merge —
// gets one at the floor: the floor is the guarantee the next local create
// starts above the collision, and it only holds if the row exists.
//
//nolint:gosec // G201: no interpolation.
func raiseChildCounterFloorsInTx(ctx context.Context, tx *sql.Tx, floors map[string]int64) error {
	for parent, floor := range floors {
		res, err := tx.ExecContext(ctx,
			"UPDATE child_counters SET last_child = ? WHERE parent_id = ? AND last_child < ?",
			floor, parent, floor)
		if err != nil {
			return fmt.Errorf("renumber: raise child counter for %s to %d: %w", parent, floor, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", parent).Scan(&exists); err != nil {
			return fmt.Errorf("renumber: check child counter for %s: %w", parent, err)
		}
		if exists > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)", parent, floor); err != nil {
			return fmt.Errorf("renumber: seed child counter for %s at %d: %w", parent, floor, err)
		}
	}
	return nil
}
