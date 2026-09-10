package issueops

// Build-time completeness guard for the row_lock optimistic-concurrency token,
// absorbed from gastownhall/beads#4682's revision guard and retargeted at the
// row_lock column current main actually ships (see the freshRowLock invariant
// in lease.go and types.Issue.RowVersion). The PR's substring stamp check
// (strings.Contains(body, "revision")) was false-negative-prone — any comment
// or identifier mentioning the column name counted as a stamp — so this port
// tightens it to the actual mint forms: a write's own literal must show a
// genuine row_lock rewrite (literalStampsRowLock), and only the routed
// funnels whose SET clause is itself assembled elsewhere (a %s placeholder)
// fall back to an AST-based scan of the enclosing function for a
// freshRowLock()/FreshRowLock()/RowLockClause() call (containsStampCall) —
// never a text/regex scan, so a stamp mentioned only in a comment does not
// count (comments are never part of the expression tree).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// issueTableWriteRe matches the start of an UPDATE or INSERT to an issue-bearing
// table: literal `issues`/`wisps` (optionally backtick-quoted — MySQL identifier
// quoting, used by versioncontrolops's manual conflict-resolution writes,
// e.g. UPDATE `issues` SET ...), or a `%s`-templated table (the routed
// issues/wisps funnels build the table name with WispTableRouting/pickIssueTable
// and interpolate it as %s, itself sometimes backtick-quoted — conflicts.go's
// UPDATE `%s` SET ...). Auxiliary tables reached through the same %s
// templating are filtered out by their distinguishing columns (auxOrExemptMarkers).
//
// This alone over-matches: fmt.Errorf("db: Update %s: %w", id, err) and
// similar error-format strings echo the verb+%s shape without being SQL at
// all. sqlWriteKeywordRe below requires a real clause keyword before a match
// is counted.
//
// The trailing `\b` needs no backtick counterpart: it is a word/non-word
// boundary check anchored right after the bare identifier ("issues", "wisps",
// or the "s" of "%s"), which already holds whether the next real character is
// a backtick, whitespace, or anything else non-word — adding an optional
// backtick there would make it FALSE when one is actually present (backtick
// and the whitespace after it are both non-word, so no boundary exists
// between them).
var issueTableWriteRe = regexp.MustCompile("(?i)\\b(?:UPDATE|INSERT\\s+INTO)\\s+`?(?:issues|wisps|%s)\\b")

// sqlWriteKeywordRe requires a genuine SQL clause keyword in the same
// enclosing literal as an issueTableWriteRe match, so an error-format string
// that merely names the verb for a message — "db: Update %s: %w", "db:
// insert into %s: %w" (internal/storage/domain/db/issue.go) — is not counted
// as a table write. Every real UPDATE has a SET; every real INSERT has
// VALUES; the one aux-table copy that uses neither verb's usual keyword
// (persistence.go's `INSERT INTO %s (%s) SELECT %s FROM %s ...`) has SELECT.
var sqlWriteKeywordRe = regexp.MustCompile(`(?i)\b(?:SET|VALUES|SELECT)\b`)

// auxOrExemptMarkers identify a matched write that legitimately does NOT stamp
// row_lock. The exemption set is main's freshRowLock invariant (lease.go):
// row_lock guards status/assignee/started_at against the reclaim/close races,
// and paths touching only orthogonal cells are safe to merge with a reclaim.
//
//   - is_blocked: the denormalized is_blocked recompute deliberately preserves
//     updated_at (blocked_state.go x4, dependencies.go, domain/db/dependency.go)
//     and must not remint row_lock either — an aux-marker refresh that bumped
//     the token would clobber a concurrent whole-row CAS (ExpectedVersion) for
//     no content change. Analysis absorbed from gastownhall/beads#4682.
//     (The PR's other exemption, the lease heartbeat, is moot on main: since
//     bd-lrgn1 heartbeats live entirely in the ephemeral leases table and never
//     touch the issues row.)
//   - "compaction_level = ": compaction apply/restore rewrites bookkeeping and
//     compacted body text only — cells the reclaim/close races don't care
//     about, exempted by name in the freshRowLock invariant. The assignment
//     form (not the bare column name) keeps the create INSERT, whose column
//     list also names compaction_level, checkable.
//   - "SET id = ?": rename (updateIssueIDInTx/updateWispIDInTx) is the only
//     writer of the primary key, exempted by name in the freshRowLock
//     invariant.
//   - "SELECT %s FROM": the persistence move's fully-templated aux-table copy
//     (INSERT INTO %s (%s) SELECT %s FROM %s ...) — it copies snapshot/event
//     side tables verbatim; the issues/wisps row itself is re-inserted through
//     InsertIssueStrictInTx, which stamps.
//   - the remainder are columns unique to AUXILIARY tables reached through the
//     same %s templating (events / dependencies / child_counters / snapshots /
//     comments). Issue rows key on `id` and never carry these, so their
//     presence proves a non-issue table.
//
// If a future write trips this guard: stamp row_lock with freshRowLock() (or
// RowLockClause()) when it is a real issues/wisps content write; add the
// distinguishing column/marker here — with the WHY — when it is a new
// auxiliary-table or deliberately-orthogonal write.
//
// A marker match is refused outright, regardless of which marker fired, when
// the write's own SET clause assigns status, assignee, or started_at — the
// three columns the freshRowLock invariant exists to guard (lease.go). Those
// markers are legitimate only because is_blocked/compaction/rename writes
// never touch that trio; a hypothetical future write that assigned e.g.
// status in the same statement as an is_blocked-marked WHERE predicate must
// still stamp. See setClauseAssignsLifecycleField.
var auxOrExemptMarkers = []string{
	"is_blocked",          // is_blocked recompute (exempt by design)
	"compaction_level = ", // compaction bookkeeping/restore (exempt by design)
	"SET id = ?",          // rename: primary-key rewrite (exempt by design)
	"SELECT %s FROM",      // persistence move's verbatim aux-table copy
	"issue_id",            // events / dependencies / comments / snapshots FK
	"depends_on",          // dependency edge + retarget writes
	"parent_id",           // child_counters
	"last_child",          // child_counters
	"event_type",          // events / wisp_events
}

// funcNameExemptions are functions whose issues/wisps write is deliberately
// exempt from the row_lock stamp requirement, keyed by the enclosing
// function's identity rather than by a substring marker in the SQL text.
//
// This is a DIFFERENT mechanism from auxOrExemptMarkers, and deliberately
// bypasses setClauseAssignsLifecycleField's refusal: that refusal exists
// because a text marker can be matched incidentally (e.g. in a WHERE clause
// unrelated to the exemption's real reason), so a marker match must not wave
// through a write whose SET clause happens to also assign a lifecycle
// column. A function-name match carries no such risk — it names one specific,
// reviewed function, not a string that could recur by coincidence — so it is
// allowed to exempt a write even when that write's SET clause is fully
// %s-templated and could, at runtime, assign status/assignee/started_at.
//
// Add an entry here only for a write that is genuinely correct without a
// fresh stamp — never as a shortcut past a write that should stamp but
// doesn't.
var funcNameExemptions = map[string]string{
	// resolveOneConflictRow's `theirs` branch (conflicts.go) is whole-row
	// adoption for `bd conflicts resolve --theirs`: every their_* column,
	// including their row_lock, is copied verbatim over ours. The resulting
	// row IS their row, byte for byte, so their row_lock already vouches for
	// exactly this content — the same "settled row equals a parent, that
	// parent's token still vouches for it" argument automerge.go's
	// mergeIssuesConflictRow makes for the no-op (nothing written) case.
	// Reminting here would be pointless content-wise and would let a stale
	// ExpectedVersion CAS reject a row it should still recognize.
	"resolveOneConflictRow": "whole-row `theirs` adoption: the adopted row_lock already vouches for the (identical) adopted content",
}

// TestAllIssueRowWritesStampRowLock is the load-bearing completeness guard for
// the freshRowLock invariant: it proves, at build time, that EVERY issues/wisps
// row write across both whole-row write stacks (issueops and the proxied
// internal/storage/domain/db) either stamps a fresh row_lock or matches a
// documented exemption. A forgotten write path is a test failure, catching an
// accidental reintroduction of the zombie-merge bug before it ships.
//
// This does not mean every documented exemption is itself free of a stale-CAS
// hole in RowVersion — two of them knowingly are one: compaction rewrites
// bookkeeping and compacted body text while deliberately preserving row_lock
// (documented at storage.go:319-325, the same "lifecycle/ownership only"
// contract this guard enforces), and a rename (updateIssueIDInTx) changes the
// primary key without reminting the old row's token — but that is safe by
// construction: the CAS predicate is keyed on id, so a stale ExpectedVersion
// read against the pre-rename id matches no row at all rather than winning
// against renamed content.
func TestAllIssueRowWritesStampRowLock(t *testing.T) {
	// versioncontrolops is included alongside issueops and the proxied
	// domain/db: it writes issues/wisps rows too, both from automerge's
	// field-level conflict resolution (resolveIssuesFieldMerge) and from
	// conflicts.go's manual `bd conflicts resolve` path — both of which this
	// branch's own resolveIssuesFieldMerge/freshRowLockDistinctFrom pairing
	// can assign status/assignee. Absorbed from a dual-vendor review of
	// d10544c96, which found the original two-directory scan missed this
	// package entirely.
	dirs := []string{".", filepath.Join("..", "domain", "db"), filepath.Join("..", "versioncontrolops")}
	checked := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			n, violations := scanIssueWriteRowLockStamps(t, path, src)
			checked += n
			for _, v := range violations {
				t.Error(v)
			}
		}
	}
	if checked == 0 {
		t.Fatal("guard verified zero issue-table writes — the scan regex or directory set is wrong")
	}
	t.Logf("verified %d issues/wisps row writes across both stacks stamp row_lock", checked)
}

// scanIssueWriteRowLockStamps parses one Go source file and returns the number
// of issue-table writes it verified plus a violation message for each such write
// whose enclosing function does not mint a fresh row_lock. Split out so the
// guard's teeth can be tested against synthetic sources (TestRowLockGuardHasTeeth).
func scanIssueWriteRowLockStamps(t *testing.T, path string, src []byte) (checked int, violations []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		start := fset.Position(fn.Body.Pos()).Offset
		end := fset.Position(fn.Body.End()).Offset
		body := string(src[start:end])

		// Computed lazily, and only once per function: the AST-based
		// whole-function scan is needed only for the funnel fallback below,
		// and most functions never reach it.
		checkedStampCall, hasStampCall := false, false
		funcStampsRowLock := func() bool {
			if !checkedStampCall {
				// Function-scope first: the original, tighter guarantee for
				// a funnel whose mint call and %s-templated SET clause live
				// in the SAME function (issueops' Claim/Update).
				hasStampCall = containsStampCall(fn.Body) ||
					// versioncontrolops' automerge plan/apply split puts the
					// mint call in a SIBLING function of the same file
					// (mergeIssuesConflictRow builds the plan resolveIssuesFieldMerge
					// later applies generically), so a per-function scan can
					// never see it; recognition for those two call forms is
					// intentionally file-scoped instead. See
					// crossFuncStampCallNames and containsSetColumnRowLockStamp.
					containsCrossFuncStamp(file)
				checkedStampCall = true
			}
			return hasStampCall
		}

		for _, loc := range issueTableWriteRe.FindAllStringIndex(body, -1) {
			matchText := body[loc[0]:loc[1]]
			isInsert := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(matchText)), "INSERT")

			// Classify by the tight enclosing SQL literal so an adjacent
			// statement's columns can't bleed into this write's markers.
			stmt := enclosingSQLLiteral(body, loc[0])

			if !sqlWriteKeywordRe.MatchString(stmt) {
				// Matched the UPDATE/INSERT-INTO verb shape but names no SET/
				// VALUES/SELECT: an error-format string, not SQL.
				continue
			}

			if why, exempt := funcNameExemptions[fn.Name.Name]; exempt {
				_ = why // documented at the definition; nothing to check further
				continue
			}

			if hasAnyMarker(stmt, auxOrExemptMarkers) && !setClauseAssignsLifecycleField(stmt) {
				continue // auxiliary table or documented exemption: no stamp
			}
			checked++

			stamped := literalStampsRowLock(stmt, isInsert)
			if !stamped && !isInsert && setClauseHasPercentSPlaceholder(stmt) {
				// The write's own literal doesn't show the mint (its SET
				// clause is itself a %s placeholder assembled elsewhere —
				// update.go / domain-db's Update and Claim funnels, or
				// versioncontrolops' resolveIssuesFieldMerge), so fall back
				// to the funnel-fallback check above.
				stamped = funcStampsRowLock()
			}
			if !stamped {
				violations = append(violations, path+": "+funcDisplayName(fn)+
					" performs an issues/wisps row write that does not stamp row_lock:\n\t"+
					firstSQLLine(stmt)+
					"\nEvery issues/wisps content write must mint a fresh token via freshRowLock()/RowLockClause() (see the freshRowLock invariant in lease.go), or carry a documented exemption marker.")
			}
		}
		return true
	})
	return checked, violations
}

// setKeywordRe and whereKeywordRe bound an UPDATE statement's SET clause
// (setClause below), scoping the finding-2 lifecycle-field refusal and the
// finding-3 %s-placeholder funnel signal to the columns actually being
// assigned — not the WHERE predicate, which can legitimately reuse the same
// column names for an unrelated condition (blocked_state.go's mark/unmark
// templates test `i.status <> 'closed'` in WHERE while only ever assigning
// is_blocked/updated_at in SET).
var (
	setKeywordRe   = regexp.MustCompile(`(?i)\bSET\b`)
	whereKeywordRe = regexp.MustCompile(`(?i)\bWHERE\b`)
)

// setClause extracts stmt's SET-to-WHERE fragment, or "" for a statement with
// no SET keyword (an INSERT's column list, or a malformed match).
func setClause(stmt string) string {
	idx := setKeywordRe.FindStringIndex(stmt)
	if idx == nil {
		return ""
	}
	clause := stmt[idx[1]:]
	if w := whereKeywordRe.FindStringIndex(clause); w != nil {
		clause = clause[:w[0]]
	}
	return clause
}

// lifecycleFieldAssignRe matches an assignment to one of the three columns
// the freshRowLock invariant (lease.go) exists to guard: status, assignee,
// started_at (see storage.go's ExpectedVersion doc). A write assigning any of
// these must stamp row_lock regardless of an aux/exempt marker matched
// elsewhere in the statement.
var lifecycleFieldAssignRe = regexp.MustCompile(`(?i)\b(?:status|assignee|started_at)\s*=`)

// setClauseAssignsLifecycleField reports whether stmt's SET clause (not its
// WHERE predicate) assigns status, assignee, or started_at. Used to refuse an
// aux/exempt marker match outright when the write is really a lifecycle
// write that merely happens to share a marker word — e.g. a hypothetical
// `UPDATE issues SET status = ?, assignee = NULL WHERE is_blocked = 0` must
// not be waved through by the is_blocked marker just because that word
// appears in WHERE.
func setClauseAssignsLifecycleField(stmt string) bool {
	return lifecycleFieldAssignRe.MatchString(setClause(stmt))
}

// setClauseHasPercentSPlaceholder reports whether stmt's SET clause itself
// contains a %s token — the signal that the clause (or part of it, e.g.
// Claim's trailing row_lock fragment) is assembled elsewhere and interpolated
// in, rather than written out in this literal. Used to gate the finding-3
// function-wide fallback to only the funnel shape it is meant for.
func setClauseHasPercentSPlaceholder(stmt string) bool {
	return strings.Contains(setClause(stmt), "%s")
}

// rowLockAssignRe matches a genuine row_lock rewrite in an UPDATE's SET
// clause: a bind-parameter assignment ("row_lock = ?", close/reopen/
// unclaim/reclaim/update's generic path/persistence's storage-class move) or
// the upsert refresh ("row_lock = VALUES(row_lock)", insertIssueRow's ON
// DUPLICATE KEY branch). It deliberately does NOT match the token-preserving
// self-assignment "row_lock = row_lock" — TestRowLockGuardHasTeeth's
// mention-only case: that shape names the column without minting a fresh
// value, exactly the false-positive shape this guard exists to catch.
var rowLockAssignRe = regexp.MustCompile(`(?i)\brow_lock\s*=\s*(?:\?|VALUES\s*\(\s*row_lock\s*\))`)

// insertRowLockColumnRe matches row_lock named in an INSERT's column list
// (helpers.go's classic insertIssueIntoTable, domain/db's insertIssueRow):
// every create path pairs it with FreshRowLock() in the same ExecContext
// call's argument list.
var insertRowLockColumnRe = regexp.MustCompile(`(?i)\brow_lock\b`)

// literalStampsRowLock reports whether a write's OWN SQL literal demonstrates
// a fresh row_lock rewrite, without needing to look at the surrounding Go
// code at all. insert selects which literal shape to check: an INSERT names
// row_lock in its column list, an UPDATE rewrites it in its SET clause — the
// same text ("row_lock") means something different in each.
func literalStampsRowLock(stmt string, insert bool) bool {
	if insert {
		return insertRowLockColumnRe.MatchString(stmt)
	}
	return rowLockAssignRe.MatchString(setClause(stmt))
}

// stampCallNames are the forms that actually mint a fresh token: a direct
// freshRowLock()/FreshRowLock() call (close, reopen, unclaim, reclaim,
// insert) or RowLockClause() (claim and the proxied domain/db paths, which
// interpolate the returned "row_lock = ?" fragment into their SET clause).
var stampCallNames = map[string]bool{
	"freshRowLock":  true,
	"FreshRowLock":  true,
	"RowLockClause": true,
}

// containsStampCall walks node looking for a CallExpr naming one of
// stampCallNames. It is AST-based, not a text/regex scan over source bytes,
// so a stamp name that appears only in a comment — or in a string literal,
// or as part of an unrelated identifier — does not count: comments are never
// part of an expression tree, and the match below requires the name to be
// the callee of an actual call, not merely mentioned. Used only for the
// finding-3 fallback (a %s-templated SET clause whose mint call lives
// elsewhere in the same function), never as the primary per-write check.
func containsStampCall(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := stampCallName(ce.Fun); ok && stampCallNames[name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// stampCallName extracts the callee name from a plain call (freshRowLock())
// or a package/method-qualified call (issueops.RowLockClause()).
func stampCallName(expr ast.Expr) (string, bool) {
	switch f := expr.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		return f.Sel.Name, true
	}
	return "", false
}

// crossFuncStampCallNames are mint forms whose CallExpr is deliberately NOT
// required to live in the same function as the write it stamps.
// versioncontrolops' automerge conflict resolution splits row_lock's
// plan-building step from its generic plan-apply step: mergeIssuesConflictRow
// (automerge.go) computes the fresh token and stores it into the write-back
// plan with freshRowLockDistinctFrom, and resolveIssuesFieldMerge (also
// automerge.go) later writes whatever columns the plan names — including
// row_lock, whenever present — without ever mentioning the column by name in
// its own source. containsStampCall's function-scoped search can never see
// that pairing, so recognition for this name is intentionally file-scoped
// (see containsCrossFuncStamp) rather than added to stampCallNames, which
// stays function-scoped so the single-function funnels in issueops keep the
// tighter guarantee finding 3 of the earlier review restored.
var crossFuncStampCallNames = map[string]bool{
	"freshRowLockDistinctFrom": true,
}

// containsCrossFuncStamp reports whether file, taken as a whole (every
// function, not just one), contains evidence that SOME function in it mints
// row_lock in a form crossFuncStampCallNames or containsSetColumnRowLockStamp
// recognizes. Called only as a second-level fallback after containsStampCall
// on the single enclosing function has already failed, so it never weakens
// the primary per-write check or the same-function funnel guarantee — it
// only recognizes the specific plan/apply split described above.
func containsCrossFuncStamp(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := stampCallName(ce.Fun); ok && crossFuncStampCallNames[name] {
			found = true
			return false
		}
		return true
	})
	return found || containsSetColumnRowLockStamp(file)
}

// containsSetColumnRowLockStamp reports whether node contains a call of the
// shape `<x>.setColumn("row_lock", ...)` — issuesRowMerge.setColumn
// (automerge.go), the OTHER form mergeIssuesConflictRow uses to stamp
// row_lock into the write-back plan resolveIssuesFieldMerge later applies.
// AST-based, like containsStampCall: the literal "row_lock" must be the
// call's own first argument, not merely mentioned nearby.
func containsSetColumnRowLockStamp(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := stampCallName(ce.Fun)
		if !ok || name != "setColumn" || len(ce.Args) == 0 {
			return true
		}
		lit, ok := ce.Args[0].(*ast.BasicLit)
		if ok && lit.Kind == token.STRING && lit.Value == `"row_lock"` {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestRowLockGuardHasTeeth proves the guard actually flags an unstamped
// issues/wisps write and passes a correctly-stamped one — so a green
// TestAllIssueRowWritesStampRowLock means something. The cases below also
// cover the false positives closed by the adversarial review of ea1256462:
// a noise match on an error-format string (finding 1), a lifecycle write
// hiding behind an aux marker matched only in WHERE (finding 2), a direct
// write riding an unrelated sibling write's stamp in the same function
// (finding 3), and a stamp name that appears only in a comment (finding 4).
func TestRowLockGuardHasTeeth(t *testing.T) {
	stamped := []byte(`package p
func w(tx T, id, s string) {
	tx.ExecContext(ctx, "UPDATE issues SET status = ?, row_lock = ? WHERE id = ?", s, freshRowLock(), id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "stamped.go", stamped); len(v) != 0 || n != 1 {
		t.Errorf("stamped write: got checked=%d violations=%v; want checked=1 violations=none", n, v)
	}

	unstamped := []byte(`package p
func w(tx T, id, s string) {
	tx.ExecContext(ctx, "UPDATE issues SET status = ? WHERE id = ?", s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "unstamped.go", unstamped); n != 1 || len(v) != 1 {
		t.Errorf("unstamped write: got checked=%d violations=%d; want checked=1 violations=1", n, len(v))
	}

	// The tightened matcher: a body that merely MENTIONS row_lock — here a
	// token-preserving self-assignment, but a comment would do — without
	// minting a fresh one must still be flagged. The PR's substring check
	// (strings.Contains(body, "revision")) passed exactly this shape.
	mentionOnly := []byte(`package p
func w(tx T, id, s string) {
	// row_lock intentionally not reminted (WRONG: status writes must mint).
	tx.ExecContext(ctx, "UPDATE issues SET status = ?, row_lock = row_lock WHERE id = ?", s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "mention.go", mentionOnly); n != 1 || len(v) != 1 {
		t.Errorf("mention-only write: got checked=%d violations=%d; want checked=1 violations=1 (substring match must not count as a stamp)", n, len(v))
	}

	// An auxiliary-table write (has issue_id) must be ignored, stamped or not.
	aux := []byte(`package p
func w(tx T, id, s string) {
	tx.ExecContext(ctx, "INSERT INTO %s (id, issue_id, event_type) VALUES (?, ?, ?)", a, b, c)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "aux.go", aux); n != 0 || len(v) != 0 {
		t.Errorf("aux write: got checked=%d violations=%v; want checked=0 violations=none", n, v)
	}

	// An error-format string that echoes the verb+%s shape — like domain/db's
	// issue.go fmt.Errorf("db: Update %s: %w", id, err) — must not be counted
	// as a table write at all (finding 1 of the adversarial review of
	// ea1256462): it names no SET/VALUES/SELECT.
	errorFormatString := []byte(`package p
func w(id string, err error) error {
	return fmt.Errorf("db: Update %s: %w", id, err)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "errfmt.go", errorFormatString); n != 0 || len(v) != 0 {
		t.Errorf("error-format string: got checked=%d violations=%v; want checked=0 violations=none", n, v)
	}

	// A lifecycle write (assigns status/assignee) must not escape via an
	// aux/exempt marker matched only in its WHERE predicate (finding 2): a
	// hypothetical `... WHERE is_blocked = 0` must not wave through a status
	// change the way the real is_blocked-recompute exemption (which never
	// assigns status/assignee/started_at) legitimately does.
	lifecycleEscapeAttempt := []byte(`package p
func w(tx T, id, s string) {
	tx.ExecContext(ctx, "UPDATE issues SET status = ?, assignee = NULL WHERE is_blocked = 0", s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "lifecycle.go", lifecycleEscapeAttempt); n != 1 || len(v) != 1 {
		t.Errorf("lifecycle write behind an is_blocked WHERE marker: got checked=%d violations=%d; want checked=1 violations=1 (a marker in WHERE must not exempt a SET that assigns status/assignee/started_at)", n, len(v))
	}

	// finding 3: the stamp check used to be computed once per FUNCTION, so an
	// unrelated funnel write's stamp call covered a sibling DIRECT write that
	// never stamps at all. The two writes below share one function; the
	// first is a real funnel (RowLockClause() interpolated via %s, stamped)
	// and the second is a plain literal SET with no row_lock anywhere.
	mixedFuncDirectUnstamped := []byte(`package p
func w(tx T, id, s, table string) {
	rowLockClause, rowLockArgs := RowLockClause()
	tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, %s WHERE id = ?", table, rowLockClause), append([]interface{}{s}, rowLockArgs...)...)
	tx.ExecContext(ctx, "UPDATE issues SET status = ? WHERE id = ?", s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "mixeddirect.go", mixedFuncDirectUnstamped); n != 2 || len(v) != 1 {
		t.Errorf("mixed function (one funnel write stamped, one direct write not): got checked=%d violations=%d; want checked=2 violations=1 (the direct write must not ride the funnel write's stamp)", n, len(v))
	}

	// finding 4: the funnel fallback itself must be AST-based, not a text
	// scan over the function body — a stamp name mentioned only in a comment
	// must not count. The write below has a %s-templated SET clause (so it
	// is eligible for the fallback) and no real freshRowLock/RowLockClause
	// call anywhere in the function, only a comment naming one.
	commentOnlyStampCall := []byte(`package p
func w(tx T, id, s, table string) {
	// freshRowLock() is minted elsewhere (WRONG: no such call exists here).
	tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, %s WHERE id = ?", table, "row_lock = ?"), s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "commentonly.go", commentOnlyStampCall); n != 1 || len(v) != 1 {
		t.Errorf("comment-only stamp mention: got checked=%d violations=%d; want checked=1 violations=1 (a stamp name mentioned only in a comment must not satisfy the funnel fallback)", n, len(v))
	}

	// Finding 2 of the dual-vendor review of d10544c96: MySQL backtick-quoted
	// identifiers (versioncontrolops's UPDATE `issues` SET ...) escaped the
	// original bare-identifier regex entirely — a write in that shape was
	// never even matched, let alone checked. Built with string concatenation
	// (not a raw-string literal) because the payload itself needs a literal
	// backtick, which a backtick-delimited Go raw string cannot contain.
	backtickUnstamped := []byte("package p\n" +
		"func w(tx T, id, s string) {\n" +
		"\ttx.ExecContext(ctx, \"UPDATE `issues` SET status = ? WHERE id = ?\", s, id)\n" +
		"}")
	if n, v := scanIssueWriteRowLockStamps(t, "backtickunstamped.go", backtickUnstamped); n != 1 || len(v) != 1 {
		t.Errorf("backtick-quoted unstamped lifecycle write: got checked=%d violations=%d; want checked=1 violations=1 (UPDATE `issues` must match despite backtick quoting)", n, len(v))
	}

	// The backtick-quoting fix must not introduce a false positive on a
	// legitimately stamped backtick-quoted write.
	backtickStamped := []byte("package p\n" +
		"func w(tx T, id, s string) {\n" +
		"\ttx.ExecContext(ctx, \"UPDATE `issues` SET status = ?, row_lock = ? WHERE id = ?\", s, freshRowLock(), id)\n" +
		"}")
	if n, v := scanIssueWriteRowLockStamps(t, "backtickstamped.go", backtickStamped); n != 1 || len(v) != 0 {
		t.Errorf("backtick-quoted stamped write: got checked=%d violations=%v; want checked=1 violations=none", n, v)
	}

	// Finding 1 of the dual-vendor review of d10544c96: versioncontrolops'
	// automerge splits row_lock's plan-building step (which mints the token)
	// from its generic plan-apply step (which writes whatever the plan names,
	// including row_lock, without ever mentioning the column). The two live
	// in SIBLING functions of one file, so the mint call is invisible to a
	// per-function scan; recognition needs to be file-scoped instead
	// (containsCrossFuncStamp), gated on the two call forms that pattern
	// actually uses: freshRowLockDistinctFrom() and setColumn("row_lock", ...).
	crossFuncStampedViaMintCall := []byte(`package p
func buildsPlan(a, b int) int {
	return freshRowLockDistinctFrom(a, b)
}
func w(tx T, id, s, table string) {
	tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, %s WHERE id = ?", table, "row_lock = ?"), s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "crossfuncmint.go", crossFuncStampedViaMintCall); n != 1 || len(v) != 0 {
		t.Errorf("cross-function stamp via a sibling's freshRowLockDistinctFrom call: got checked=%d violations=%v; want checked=1 violations=none", n, v)
	}

	crossFuncStampedViaSetColumn := []byte(`package p
func buildsPlan(m *plan, v int) {
	m.setColumn("row_lock", v)
}
func w(tx T, id, s, table string) {
	tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, %s WHERE id = ?", table, "row_lock = ?"), s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "crossfuncsetcolumn.go", crossFuncStampedViaSetColumn); n != 1 || len(v) != 0 {
		t.Errorf("cross-function stamp via a sibling's setColumn(\"row_lock\", ...) call: got checked=%d violations=%v; want checked=1 violations=none", n, v)
	}

	// The cross-function fallback must still refuse a %s-templated write with
	// no stamp evidence ANYWHERE in the file — a sibling that calls
	// setColumn with an unrelated column name must not be mistaken for a
	// row_lock stamp.
	crossFuncNoStamp := []byte(`package p
func buildsPlan(m *plan, v string) {
	m.setColumn("title", v)
}
func w(tx T, id, s, table string) {
	tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, %s WHERE id = ?", table, "not_row_lock = ?"), s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "crossfuncnostamp.go", crossFuncNoStamp); n != 1 || len(v) != 1 {
		t.Errorf("no stamp evidence anywhere in file: got checked=%d violations=%d; want checked=1 violations=1 (an unrelated setColumn call must not satisfy the cross-function fallback)", n, len(v))
	}

	// funcNameExemptions is keyed to the enclosing function's identity, not a
	// text marker, so it must exempt the exact named function (conflicts.go's
	// whole-row `theirs` adoption) even though its SET clause could assign
	// status/assignee at runtime...
	exemptByName := []byte(`package p
func resolveOneConflictRow(tx T, id, s, table string) {
	tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, assignee = ? WHERE id = ?", table), s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "exemptbyname.go", exemptByName); n != 0 || len(v) != 0 {
		t.Errorf("resolveOneConflictRow func-name exemption: got checked=%d violations=%v; want checked=0 violations=none (a whole-row-adoption write named by function identity must be exempt)", n, v)
	}

	// ...but must NOT exempt a differently-named function with the identical
	// write shape: the exemption is the specific reviewed function, not the
	// SQL shape.
	notExemptByName := []byte(`package p
func someOtherWrite(tx T, id, s, table string) {
	tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, assignee = ? WHERE id = ?", table), s, id)
}`)
	if n, v := scanIssueWriteRowLockStamps(t, "notexemptbyname.go", notExemptByName); n != 1 || len(v) != 1 {
		t.Errorf("non-exempt function with the same write shape: got checked=%d violations=%d; want checked=1 violations=1 (the exemption must be keyed to the specific function name, not the SQL shape)", n, len(v))
	}
}

func hasAnyMarker(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func funcDisplayName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		if t, ok := receiverTypeName(fn.Recv.List[0].Type); ok {
			return "(" + t + ")." + fn.Name.Name
		}
	}
	return fn.Name.Name
}

func receiverTypeName(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name, true
	}
	return "", false
}

func firstSQLLine(window string) string {
	for _, line := range strings.Split(window, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(window)
}

// enclosingSQLLiteral returns the Go string literal (backtick-raw or
// double-quoted) that contains the byte at matchStart, without its delimiters.
// The SQL write statements here never span more than one literal, and neither
// delimiter appears inside these SQL bodies (which use single quotes), so a
// nearest-delimiter scan bounds the statement exactly — no bleed into adjacent
// statements. Falls back to a fixed window if no delimiter is found.
func enclosingSQLLiteral(body string, matchStart int) string {
	open, delim := -1, byte(0)
	for i := matchStart; i >= 0; i-- {
		if body[i] == '`' || body[i] == '"' {
			open, delim = i, body[i]
			break
		}
	}
	if open < 0 {
		end := matchStart + 400
		if end > len(body) {
			end = len(body)
		}
		return body[matchStart:end]
	}
	for i := open + 1; i < len(body); i++ {
		if body[i] == delim && (delim == '`' || body[i-1] != '\\') {
			return body[open+1 : i]
		}
	}
	return body[open+1:]
}
