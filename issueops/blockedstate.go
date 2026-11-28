package issueops

// BlockedStateInvariant is the canonical statement of what the mutating roles
// in this package promise about BLOCKED STATE, and the only statement a
// conformance case may assert against. Cite it BY SYMBOL: it is a doc anchor
// precisely so a citation cannot rot the way a file:line one does.
//
// Blocked state is DERIVED AND PERSISTED. It is a function of the current
// dependency graph and the current statuses of both planes, and it is also
// stored on the row, which is what makes it assertable at all — and what makes
// asking a role "is this issue blocked?" the wrong way to check it. A backend
// that answered correctly from the live graph and never wrote the column would
// satisfy every role answer and none of the clauses below.
//
// THE PREDICATE. A row that is closed or pinned is never blocked. Any other row
// is blocked exactly when at least one of these holds:
//
//   - it has a blocks or conditional-blocks edge onto a target that is itself
//     neither closed nor pinned;
//   - it is the child of a parent-child edge whose parent is blocked, so
//     blockage is INHERITED down a hierarchy transitively;
//   - it has a waits-for edge whose gate over the spawner's children is not yet
//     satisfied.
//
// Edges and inheritance cross the two planes in BOTH directions: an issue may
// be blocked by a wisp and a wisp by an issue, and a parent in either plane
// propagates to a child in either. The exact gate vocabulary (all-children,
// any-children, also_blocks) is the derivation engine's, not this doc's; a role
// contract pins that a gate is CONSULTED, not how it reads.
//
// THE LOCAL-WRITE CLAUSE — transactional, not eventual. Every role method that
// commits a mutation leaves blocked state consistent with the predicate FOR THE
// ROWS ITS MUTATION COULD HAVE AFFECTED, inside its own transaction, before it
// commits. There is no queue, no follow-up pass and no window: a caller that
// reads the column immediately after the verb returns reads the settled value.
// The affected rows are not only the row named in the request — closing a
// blocker settles its dependers, and settling a row settles that row's
// parent-child descendants, in both planes, to a fixpoint.
//
// THE MERGE CLAUSE — eventual, with a named repair. A merge taken during a pull
// bypasses the local write paths, so blocked state after a merge is settled by
// a scoped recompute that follows the merge rather than by the merge itself. If
// that recompute is skipped or fails, stale values persist until the next pull
// widens its window or an operator runs the full repair. This and the
// partial-view clause below are the only clauses under which the column may
// differ from the predicate read over this database's rows, and no role method
// can produce a stale value under this one.
//
// THE PARTIAL-VIEW CLAUSE — what a database cannot see, it leaves as found.
// Wisps live only in the database that created them, while the edges onto them
// replicate, so a database can hold a dependency on a wisp it has no row for:
// every clone of a database that blocks issues on wisps does. Such an edge is
// UNKNOWN, neither a reason nor the absence of one, because the wisp may be
// open where it lives. A row is UNDECIDED when its value hangs on one:
//
//   - it holds such an edge, of a kind the predicate reads a wisp for (blocks,
//     conditional-blocks, parent-child, waits-for); a waits-for edge onto an
//     absent spawner is unknown even beside a visible open child of that
//     spawner; or
//   - it is the parent-child child of a blocked parent that holds one and has
//     no reason the database can see placing its blockedness outside its own
//     subtree, or of a blocked parent with a blocking reason of its own below
//     such a parent, with nothing plainly exogenous between them. Such a
//     parent darkens no child by inheritance, and brightens none either.
//
// The hold reaches no further down. A parent with no blocking reason of its
// own is never such a parent, even while it is undecided itself: its flag is
// inherited, and every database passes it to the parent's children
// unchanged. So its children are decided from the flag it holds, which while
// it is undecided is the value the database that has the wisp gave it, and
// from which that database derives the same value for them. A parent stored
// at 0 is not blocked, so it holds nothing below it either: its children are
// decided as under any parent that is not blocked.
//
// An undecided row that is closed or pinned is still never blocked. Otherwise
// it is blocked when the predicate, read over what the database can see after
// every decided row has settled, finds a reason for it, and else it KEEPS THE
// VALUE IT HAD, whoever set it: the last merge, or a local blocker that has
// since closed. It stays so until a merge brings the value of the database
// that has the wisp, or the edge is removed. Settling never clears an
// undecided row on a guess, and never marks one on a value it would itself
// have cleared. A database that holds every wisp its edges name has no
// undecided row, and the predicate decides every row exactly as above.
//
// Three partial views leave no edge to see, and still decide rows from what is
// here: a waits-for gate on a durable spawner whose children are wisps (their
// edges live in the wisp plane); a close gate whose target hangs below the epic
// through an absent wisp (the walk up stops at the wisp, so the reason reads as
// outside the epic); and an any-children gate on a durable spawner whose
// closed child is an absent wisp.
//
// THE NON-PERTURBATION CLAUSES. Settling blocked state is not a user edit: it
// never bumps a row's updated_at, so a flip does not make a synced row look
// hand-edited to a stale-guard or a conflict-guard. It is idempotent on a
// consistent database — a verb that changes nothing settles nothing — and it
// never reaches rows outside the affected set, so an unrelated blocked row is
// still blocked, for the same reason, after a neighboring claim or comment.
const BlockedStateInvariant = "is_blocked equals the fixpoint of the blocking predicate over the current graph, on both planes, " +
	"at the end of every committing mutation, without bumping updated_at"
