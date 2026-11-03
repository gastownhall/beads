package main

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage"
)

// createRefusalText spells a create role refusal for the CLI. A prefix
// mismatch gets the --force hint the direct route's own explicit-ID precheck
// (validation.ValidateIDPrefixAllowed) already prints: the role's message is
// shared with every front door and so does not name a CLI flag (GH#7965). The
// sentinel is matched with errors.Is rather than by reading the message.
func createRefusalText(err error) string {
	if errors.Is(err, storage.ErrPrefixMismatch) {
		return err.Error() + " (use --force to override)"
	}
	return err.Error()
}

// reportCreateRoleError reports a refused Lifecycle.Create on the direct
// route. explicitID is the caller's --id, empty when the role minted one.
func reportCreateRoleError(err error, explicitID string) error {
	if handled, rendered := reportCreateOfRetiredID(err); handled {
		return rendered
	}
	// RULING R1: an occupied --id is a refusal, not a silent full-row
	// upsert reported as success.
	if errors.Is(err, storage.ErrAlreadyExists) && explicitID != "" {
		return HandleErrorRespectJSON("%s already exists; use bd update, or bd import for upsert semantics", explicitID)
	}
	return HandleErrorRespectJSON("%s", createRefusalText(err))
}
