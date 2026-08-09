package eventsjournal

import (
	"encoding/json"

	"github.com/steveyegge/beads/internal/storage"
)

// The record envelope every consumer of the journal receives, in ONE place.
//
// It started in cmd/bd as the shape `bd events tail` printed, and moved here
// when GET /v0/beads/events published the same records over HTTP. Two encoders
// of the same journal row is exactly the drift this package exists to prevent:
// the CLI's JSONL output is byte-pinned by the protocol corpus, so a second
// struct in internal/httpapi would have satisfied its own tests while quietly
// serving a different `ts` shape, a `dep` that was emitted when empty, or an
// `issue` that was `{}` where the CLI says `null`. A consumer that mirrors a
// workspace over HTTP and reconciles against a CLI export has to see one
// contract, not two that agree today.
//
// The storage row is deliberately NOT this type. storage.EventsJournalRow is
// the substrate's shape — three payload columns as strings, one of which is
// empty most of the time — and Record is the published one. The projection
// between them is the whole content of this file.

// Record is one journal line as a consumer receives it, on the CLI's stdout and
// in a GET /v0/beads/events body alike.
//
// Issue, Dep and Comment are raw JSON so the stored payloads travel unchanged:
// re-encoding them here would reorder members and renormalize numbers against a
// contract that promises the issue exactly as the mutation left it.
type Record struct {
	// Seq is counter-assigned inside the mutation's transaction: gapless,
	// strictly increasing in commit order, never reused or reset.
	Seq int64 `json:"seq"`
	// TS is the UTC insert time stamped inside the committing transaction,
	// normalized to RFC 3339 at the read seam (normalizeEventsTimestamp).
	TS string `json:"ts"`
	// Op is one of create, update, close, delete, dep_add, dep_remove, comment.
	Op string `json:"op"`
	// IssueID is the mutated issue's id.
	IssueID string `json:"issue_id"`
	// Issue is the full issue state AFTER the mutation, and the literal JSON
	// `null` on a delete — never absent. A delete has no surviving row, and a
	// consumer must be able to tell that from a payload this server failed to
	// record, so the member is always present and omitempty is deliberately not
	// set on it.
	Issue json.RawMessage `json:"issue"`
	// Dep is {"kind","target","metadata"} on dep_add and dep_remove, and absent
	// otherwise. Absence rather than null: it says the op has no dependency
	// half at all, which is a different statement from a delete's null issue.
	Dep json.RawMessage `json:"dep,omitempty"`
	// Comment is {"id","author","text","created_at","source"} on comment, and
	// absent otherwise, for Dep's reason.
	Comment json.RawMessage `json:"comment,omitempty"`
}

// NewRecord projects one stored row onto the published envelope.
func NewRecord(row storage.EventsJournalRow) Record {
	rec := Record{
		Seq:     row.Seq,
		TS:      row.TS,
		Op:      row.Op,
		IssueID: row.IssueID,
		Issue:   json.RawMessage("null"),
	}
	if row.IssueJSON != "" {
		rec.Issue = json.RawMessage(row.IssueJSON)
	}
	if row.DepJSON != "" {
		rec.Dep = json.RawMessage(row.DepJSON)
	}
	if row.CommentJSON != "" {
		rec.Comment = json.RawMessage(row.CommentJSON)
	}
	return rec
}

// Records projects a whole page. It returns a non-nil empty slice for an empty
// page, so an HTTP body carries `"records": []` rather than `"records": null` —
// a caught-up consumer reads an empty list, not a missing one.
func Records(rows []storage.EventsJournalRow) []Record {
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, NewRecord(row))
	}
	return out
}
