package issueops

import (
	"encoding/json"

	"github.com/steveyegge/beads/internal/types"
)

// updateKeyAliases maps the update-map spellings that differ from the Issue's
// JSON field names (the aliases applyUpdateField accepts) onto those names, so
// a pre-image keyed by Issue fields names the same field the update touched.
var updateKeyAliases = map[string]string{
	"event_actor":    "actor",
	"event_target":   "target",
	"event_payload":  "payload",
	"event_category": "event_kind",
}

// ChangedFieldsPreImage is the old_value an update-shaped event (updated,
// status_changed, closed, reopened, claimed) records: the old issue projected
// onto the fields the update touches, keyed and encoded as the Issue marshals
// them, with keys sorted. A field the old issue did not carry (an omitempty
// zero, such as the empty assignee before a claim) is recorded as null, so the
// pre-image names every field the update changed.
//
// Before this, old_value was the WHOLE old issue (5-6 KB per row on a workspace
// with sizeable descriptions) while new_value was only the updates map, so a
// claimed or status_changed event cost two orders of magnitude more than the
// change it recorded, and `bd history` printed an unreadable blob beside a
// two-field "New". The pre-image is the symmetric diff: Old and New name the
// same fields. Nothing in bd parses old_value back into an Issue; the events
// journal (bd_events_journal.issue_json) is the replayable full snapshot.
func ChangedFieldsPreImage(oldIssue *types.Issue, updates map[string]interface{}) string {
	if oldIssue == nil || len(updates) == 0 {
		return "{}"
	}
	full, err := json.Marshal(oldIssue)
	if err != nil {
		return "{}"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(full, &fields); err != nil {
		return "{}"
	}
	pre := make(map[string]json.RawMessage, len(updates))
	for key := range updates {
		name := key
		if alias, ok := updateKeyAliases[key]; ok {
			name = alias
		}
		if v, ok := fields[name]; ok {
			pre[name] = v
		} else {
			pre[name] = json.RawMessage("null")
		}
	}
	out, err := json.Marshal(pre)
	if err != nil {
		return "{}"
	}
	return string(out)
}
