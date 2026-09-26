package storage

import (
	"context"
	"time"
)

// IssueVersion is one accepted, immutable durable state of one issue, as
// issue_versions records it.
//
// Revision is a PER-STORE ORDINAL, not a portable address: two disconnected
// clones can both hold revision 8 for the same issue (migration 0067's own
// header says so, and the flag is single-writer-only until the version_id
// primary key lands). Callers must not present it as a citable address.
//
// Its JSON key is "local_revision", NOT "revision", for two reasons that
// compound. types.Issue already ships `json:"revision"` (types.go:1206) and
// that is the row-lock CAS token, an unrelated thing -- and it is a STRING
// there while this is an int64, so a script that learned one shape breaks on
// the other. The name also carries the non-citability with the value, where a
// machine reader sees it, instead of only in a footer no script parses. It
// needs no rename when version_id lands beside it. (bee-ghosttrack, #5898
// consumer read, 2026-09-20.)
//
// It lives here rather than in issueops for the same reason HistoryEntry
// does: issueops imports storage, so the shared row type has to sit on this
// side of that edge.
type IssueVersion struct {
	IssueID            string     `json:"issue_id"`
	Revision           int64      `json:"local_revision"`
	Epoch              int64      `json:"epoch"`
	ChangeActor        string     `json:"change_actor"`
	ChangeAgent        string     `json:"change_agent"`
	ChangeMessage      string     `json:"change_message"`
	ChangeAt           time.Time  `json:"change_at"`
	AttributionStatus  string     `json:"attribution_status"`
	RemovedAt          *time.Time `json:"removed_at"`
	RemovedReason      string     `json:"removed_reason"`
	RemovedRestriction string     `json:"removed_restriction"`
	StateBytes         int64      `json:"state_bytes"`
}

// Removed reports whether this version is no longer served -- retention,
// erasure or an epoch reorganization removed it. A removed version keeps its
// row (so the gap is legible) but its durable state is gone, and callers must
// render it as a restriction rather than as an empty version.
func (v IssueVersion) Removed() bool { return v.RemovedAt != nil }

// VersionLister is implemented by backends that can serve an issue's
// versioned history from the issue_versions table (migration 0067+).
//
// This is a narrow capability rather than a method on Storage on purpose: a
// backend that has no issue_versions table -- a proxied client, a no-db
// workspace, a non-Dolt backend -- should not be forced to implement it, and
// a caller that asserts for it learns the truth instead of receiving a stub
// that answers "no versions" for a store that simply cannot know. Same
// reasoning as ExternalRefHistoryQuerier in history_viewer.go.
//
// Holding this capability says the backend can READ versions. It says nothing
// about whether version recording is switched on -- that is
// VersionedHistoryConfigurer, and a store can legitimately implement both
// while the feature is off, in which case ListVersions truthfully returns
// nothing.
type VersionLister interface {
	// ListVersions returns every retained version of issueID, newest first.
	// An empty result means no retained versions, never "unsupported" --
	// unsupported is the failed type assertion.
	ListVersions(ctx context.Context, issueID string) ([]IssueVersion, error)
}
