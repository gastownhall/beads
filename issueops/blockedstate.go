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
// widens its window or an operator runs the full repair. This is the ONE clause
// under which a stale column is admissible, and no role method can produce that
// state.
//
// THE NON-PERTURBATION CLAUSES. Settling blocked state is not a user edit: it
// never bumps a row's updated_at, so a flip does not make a synced row look
// hand-edited to a stale-guard or a conflict-guard. It is idempotent on a
// consistent database — a verb that changes nothing settles nothing — and it
// never reaches rows outside the affected set, so an unrelated blocked row is
// still blocked, for the same reason, after a neighboring claim or comment.
const BlockedStateInvariant = "is_blocked equals the fixpoint of the blocking predicate over the current graph, on both planes, " +
	"at the end of every committing mutation, without bumping updated_at"
