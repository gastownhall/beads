package issueops

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// DueRequiredKey is the workspace switch that turns a due date from an option
// into a create-time invariant.
//
// It defaults OFF: beads does not decide that your workspace needs deadlines.
// A team that wants the invariant turns it on once, in config.yaml, and every
// create surface then demands a deadline.
const DueRequiredKey = "due.required"

// DueRequiredEnabled reports whether this workspace requires a due date on new
// work.
func DueRequiredEnabled() bool {
	return config.GetBool(DueRequiredKey)
}

// DueRequiredExempt reports whether an issue sits outside the mandatory-due
// invariant because it is not work anyone is meant to finish by a date: an
// audit event, a wisp-plane record, a read-only molecule template, or a row a
// federation adapter authored elsewhere and this database only mirrors.
//
// `bd import` — the backfill path — is exempt at its CALL SITE instead, through
// BatchCreateOptions.SkipDueRequired, because an imported row's exemption is a
// property of the caller restoring it, not of the row itself: the same row
// created by hand still needs a due date.
func DueRequiredExempt(issue *types.Issue) bool {
	return DueRequiredExemptReason(issue) != ""
}

// DueRequiredExemptReason names the class that exempts an issue from the
// mandatory-due invariant — "event", "wisp", "template" or "federated" — or ""
// when the issue is held to it. It is the one predicate behind
// DueRequiredExempt, exposed so a report can say WHY a row was skipped.
func DueRequiredExemptReason(issue *types.Issue) string {
	switch {
	case issue == nil:
		return "nil"
	case issue.IssueType == types.TypeEvent:
		return "event"
	// The wisp plane is spelled three ways here because a create may arrive
	// with any one of them set — either flag, or the class marker alone.
	case issue.Ephemeral || issue.NoHistory || issue.WispType != "" ||
		issue.StorageClass == types.StorageClassEphemeral:
		return "wisp"
	case issue.IsTemplate:
		return "template"
	case issue.SourceSystem != "":
		return "federated"
	}
	return ""
}

// ValidateDueRequired refuses a create that would land work with no deadline.
// It is the storage-boundary half of the invariant: every create surface — CLI,
// HTTP, MCP, the public issueops facade — reaches one of its two call sites, so
// the rule cannot be sidestepped by picking a different front door.
func ValidateDueRequired(issue *types.Issue) error {
	if !DueRequiredEnabled() || DueRequiredExempt(issue) || issue.DueAt != nil {
		return nil
	}
	return fmt.Errorf("%w: due date is required for %s %q (pass --due, or set `%s: false` in config.yaml)",
		storage.ErrValidation, issue.IssueType.Normalize(), issue.Title, DueRequiredKey)
}

// OpDueClearReason carries the reason for clearing a due date into a generic
// update, the way OpForceClosePolicy carries the close-policy override: a
// map key that is not a column, popped by the write funnels before the field
// allowlist sees it. It is set by UpdateRequest.DueClearReason.
const OpDueClearReason = "_due_clear_reason"

// PopDueClearReason removes OpDueClearReason from updates and returns it,
// trimmed; an absent or non-string value reads as no reason.
func PopDueClearReason(updates map[string]interface{}) string {
	raw, present := updates[OpDueClearReason]
	if !present {
		return ""
	}
	delete(updates, OpDueClearReason)
	reason, _ := raw.(string)
	return strings.TrimSpace(reason)
}

// ValidateDueClear refuses an update that clears a due date without saying
// why, while the workspace requires due dates and the row is held to the rule.
// It is the one statement of the gate both write funnels and every transport
// (CLI --force-no-due --reason, HTTP due_clear_reason) answer to; the funnels
// call it after the no-op filter, so clearing an already-empty due date needs
// no reason.
func ValidateDueClear(oldIssue *types.Issue, updates map[string]interface{}, reason string) error {
	value, clearing := updates["due_at"]
	if !clearing || value != nil {
		return nil
	}
	if !DueRequiredEnabled() || DueRequiredExempt(oldIssue) || reason != "" {
		return nil
	}
	return fmt.Errorf("%w: clearing a due date needs a reason while %s is on (bd update --due \"\" --force-no-due --reason \"<why>\", or due_clear_reason over HTTP)",
		storage.ErrValidation, DueRequiredKey)
}
