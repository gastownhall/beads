package storage

import "context"

// ChildRenumberMapping is one identity change the offline-collision repair
// (GH#4796) would make — or made: the colliding local child id, and the fresh
// id allocated for it above both clones' high-water marks. Root is true when
// the id itself collided and was allocated a new suffix, false when the id
// merely sat inside a renamed root's subtree and was mapped by prefix
// (P.71.2 under P.71 -> P.74.2 under P.74; the exact boundary never rewrites
// P.710).
type ChildRenumberMapping struct {
	OldID string `json:"old_id"`
	NewID string `json:"new_id"`
	Root  bool   `json:"root"`
}

// ChildRenumberMention is one exact-boundary mention of a renamed id in local
// text: the bead whose field or comment carries the mention, which field or
// comment, the renamed id as it appears, and the id it maps to now. The repair
// reports mentions but never rewrites them — the prose's intent is ambiguous,
// so it stays for the operator to update.
type ChildRenumberMention struct {
	IssueID   string `json:"issue_id"`
	Field     string `json:"field"`
	CommentID string `json:"comment_id,omitempty"`
	MatchedID string `json:"matched_id"`
	NewID     string `json:"new_id"`
}

// ChildRenumberPlan is the read-only repair proposal: the pinned heads it was
// computed from, the identity changes it would make, and the child-counter
// floor each allocation parent must be raised to (never lowered) once the
// repair runs. A plain pull prints it as a best-effort hint; the authorized
// repair recomputes and revalidates it rather than trusting the preview.
type ChildRenumberPlan struct {
	Remote     string                 `json:"remote"`
	RemoteRef  string                 `json:"remote_ref"`
	RemoteHead string                 `json:"remote_head"`
	LocalHead  string                 `json:"local_head"`
	Base       string                 `json:"base"`
	Mappings   []ChildRenumberMapping `json:"mappings"`
	// CounterFloors maps each allocation parent to the last_child value the
	// repair must guarantee: every suffix both sides already used or
	// allocated, so the next local create starts above the collision.
	CounterFloors map[string]int64 `json:"counter_floors"`
}

// Empty reports whether the plan proposes no identity changes — the pull has
// no eligible hierarchical add/add collision for the repair to clear.
func (p *ChildRenumberPlan) Empty() bool {
	return p == nil || len(p.Mappings) == 0
}

// ChildRenumberResult reports what one authorized --renumber-children pull
// did. On a merge that still fails after the renames (AC-6), it is returned
// non-nil WITH the error: the rename commit is retained, its mappings and
// hash are the partial result the retry reports instead of re-renaming.
type ChildRenumberResult struct {
	// Plan is the revalidated plan the repair executed; nil when no
	// collision needed repairing and no earlier repair was retained.
	Plan *ChildRenumberPlan `json:"plan,omitempty"`
	// RenameCommit is the hash of the commit carrying the renames; empty
	// when nothing was renamed.
	RenameCommit string `json:"rename_commit,omitempty"`
	// Retained is true when RenameCommit was created by an earlier
	// invocation against the same remote revision (the retry case).
	Retained bool `json:"retained"`
	// Mentions are the exact-boundary mentions of the renamed ids the
	// repair found in local title/description/design/acceptance criteria/
	// notes/comments. The text itself is unchanged.
	Mentions []ChildRenumberMention `json:"mentions"`
}

// ChildRenumberPlanner is the read-only half of the optional capability beside
// VersionControl's Pull for GH#4796: computing the repair proposal a plain
// pull reports as a hint. Implemented by the Dolt-backed stores only;
// callers must type-assert, and a backend without it simply prints no hint.
type ChildRenumberPlanner interface {
	// PlanChildRenumbers computes the offline-collision repair proposal for a
	// pull from remote ("" is the store's default remote). It reads only the
	// local branch and the current remote-tracking ref — no fetch, no writes,
	// no renames — so a plain pull that already fetched can call it after
	// its failure without changing any state.
	PlanChildRenumbers(ctx context.Context, remote string) (*ChildRenumberPlan, error)
}

// ChildRenumberPuller is the authorized half of the same capability, for
// `bd dolt pull --renumber-children`: the operator's explicit consent to
// rename the local colliding children above both sides' high-water marks,
// commit the renames, and merge the pinned remote revision through normal
// settlement. Implemented by the Dolt-backed stores only; callers must
// type-assert and report a clear "not supported" error otherwise.
type ChildRenumberPuller interface {
	ChildRenumberPlanner
	// PullWithChildRenumbers performs the authorized repair for a pull from
	// remote ("" is the store's default remote): rename the colliding local
	// children in one transaction, commit the renames, then pull and merge
	// the pinned remote revision. On a merge failure after the renames, the
	// result is returned non-nil with the error (the rename commit is
	// retained; see ChildRenumberResult).
	PullWithChildRenumbers(ctx context.Context, remote string) (*ChildRenumberResult, error)
}
