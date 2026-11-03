package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// reportCreateOfRetiredID renders a create refused because its explicit id was
// RETIRED by a retaining retention sweep (issueops.IdempotencyExpiredError),
// and reports whether err was one. It is the one renderer the direct, proxied
// and http create routes share, so the refusal reads the same on all three.
//
// A consumer must be able to branch on this without parsing prose — the
// create that used the id already happened, and retrying it, or minting a new
// id for the same intent, is exactly the re-execution the retention exists to
// rule out — so --json carries the stable code beside the id and the closed_at
// the tombstone recorded, the way `bd events` carries
// events_journal_truncated. Exit status 1 in both modes.
func reportCreateOfRetiredID(err error) (handled bool, rendered error) {
	var expired *issueops.IdempotencyExpiredError
	if !errors.As(err, &expired) {
		return false, nil
	}
	closed := ""
	if !expired.ClosedAt.IsZero() {
		closed = " (closed " + expired.ClosedAt.UTC().Format(time.RFC3339) + ")"
	}
	message := fmt.Sprintf("%s was retired by a retention sweep%s; the create that used it already happened and never runs again",
		expired.ID, closed)
	if jsonOutput {
		payload := map[string]any{
			"error": message,
			"code":  issueops.IdempotencyExpiredCode,
			"id":    expired.ID,
		}
		if !expired.ClosedAt.IsZero() {
			payload["closed_at"] = expired.ClosedAt.UTC().Format(time.RFC3339)
		}
		if encErr := outputJSON(payload); encErr != nil {
			return true, encErr
		}
		return true, &exitError{Code: 1}
	}
	return true, HandleError("%s", message)
}

// createRefusalText renders a create refusal, naming the override a prefix
// mismatch earns. Both local front doors decide whether to re-offer the
// create with --force from errors.Is rather than from the message, but a
// caller reading only the rendered text cannot act on a bare "prefix
// mismatch" — so the one refusal that has an override says so.
func createRefusalText(err error) string {
	if errors.Is(err, storage.ErrPrefixMismatch) {
		return err.Error() + " (use --force to override)"
	}
	return err.Error()
}
