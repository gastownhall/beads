// Package depid derives the deterministic primary-key id of a dependency edge.
//
// Background (gastownhall/beads#4259): the dependencies (and wisp_dependencies)
// tables carry a surrogate primary key `id CHAR(36)`. It was originally filled
// by a DB-side DEFAULT (UUID()), which mints a *different* random value on every
// clone. Because beads syncs by merging independent Dolt clones, two clones that
// create the same logical edge — or that migrate independently — end up with the
// same row under two different primary keys. Dolt then either fails the merge
// ("different primary keys in its common ancestor") or pulls in both rows and
// trips the uk_dep_* unique key. Either way `bd dolt pull` breaks unrecoverably.
//
// The fix is to derive `id` deterministically from the edge's natural identity,
// so the same edge gets the same id on every clone and merges cleanly. The
// natural identity is (issue_id, target) where target is the single non-null of
// depends_on_issue_id / depends_on_wisp_id / depends_on_external — exactly the
// columns the uk_dep_* unique keys enforce. The dependency `type` is deliberately
// NOT part of the identity (it is not in any unique key).
//
// This package is the single source of truth for that derivation: every insert
// path and the upgrade backfill call New so a row's id never depends on which
// clone or which code path created it.
package depid

import "github.com/google/uuid"

// Namespace is the fixed UUIDv5 namespace for beads dependency ids. It was
// generated once as uuid.NewSHA1(uuid.NameSpaceURL,
// "https://github.com/gastownhall/beads#dependency-id") and is hardcoded here
// forever: changing it would re-key every existing edge and re-introduce the
// cross-clone divergence this package exists to prevent.
var Namespace = uuid.MustParse("bdd74eb9-a20a-554a-985b-54b8f3e64d8b")

// sep separates the two key components. It is ASCII Unit Separator (0x1f), which
// cannot occur in an issue id or a dependency target, so the encoding is
// unambiguous (no (issueID, target) pair can collide with another).
const sep = "\x1f"

// New returns the deterministic CHAR(36) primary key for the dependency edge
// (issueID -> target). target must be the resolved, non-null dependency target
// (an issue id, a wisp id, or an "external:..." string) — i.e. the value the
// uk_dep_* unique keys see, not the typed column name.
//
// The same (issueID, target) yields the same id on every clone and in every
// process, which is what makes the dependencies table merge-safe across Dolt
// clones. It is a valid RFC-4122 v5 UUID.
func New(issueID, target string) string {
	return uuid.NewSHA1(Namespace, []byte(issueID+sep+target)).String()
}
