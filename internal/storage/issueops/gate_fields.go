package issueops

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// MaxAwaitTypeLen is the await_type column's width in characters
// (VARCHAR(32), migration 0001).
const MaxAwaitTypeLen = 32

// ValidateGateFields checks an issue's gate members against the stored
// columns, for every create shape of the role: PreparePublicCreateRequest
// runs it, so Lifecycle.Create, BatchCreator items and BatchApplier create
// items share it on every backend.
//
// The widths are the columns' and no more (see publicops.GateFieldError):
// a Dolt column already refused an oversized value with a raw driver error,
// and a Postgres text column accepted it, so this converges the two on one
// typed refusal. The one rule that is not a width, a non-negative timeout, is
// no column's (timeout_ns is a BIGINT); the wire publishes it as minimum 0.
// The raw store creates (CreateIssue, CreateIssues) and import do not run
// this check, so bd gate create --timeout=-5m, a formula step's gate.timeout
// and a template clone still store a negative timeout until they move onto
// the role.
func ValidateGateFields(issue *types.Issue) error {
	if issue == nil {
		return nil
	}
	if err := validateAwaitType(issue.AwaitType); err != nil {
		return err
	}
	if err := validateAwaitID(issue.AwaitID); err != nil {
		return err
	}
	if err := validateGateTimeout(issue.Timeout); err != nil {
		return err
	}
	return validateWaiters(issue.Waiters)
}

// ValidateGatePatch checks the gate members an IssuePatch can set — await_id
// and waiters — with ValidateGateFields' rules. await_type and timeout are not
// patch members: no update route has ever changed them.
func ValidateGatePatch(patch publicops.IssuePatch) error {
	if patch.AwaitID.Set {
		if err := validateAwaitID(patch.AwaitID.Value); err != nil {
			return err
		}
	}
	if patch.Waiters.Set {
		if err := validateWaiters(patch.Waiters.Value); err != nil {
			return err
		}
	}
	return nil
}

func gateFieldError(field, format string, args ...any) error {
	return &publicops.GateFieldError{
		Field:  field,
		Detail: fmt.Sprintf("%s: %s", publicops.ErrValidation.Error(), fmt.Sprintf(format, args...)),
	}
}

func validateAwaitType(value string) error {
	if n := utf8.RuneCountInString(value); n > MaxAwaitTypeLen {
		return gateFieldError(publicops.GateFieldAwaitType, "await_type is %d characters (max %d)", n, MaxAwaitTypeLen)
	}
	return nil
}

func validateAwaitID(value string) error {
	if n := utf8.RuneCountInString(value); n > types.MaxFieldLen {
		return gateFieldError(publicops.GateFieldAwaitID, "await_id is %d characters (max %d)", n, types.MaxFieldLen)
	}
	return nil
}

func validateGateTimeout(value time.Duration) error {
	if value < 0 {
		return gateFieldError(publicops.GateFieldTimeout, "timeout cannot be negative (got %s)", value)
	}
	return nil
}

// validateWaiters bounds the list by the TEXT column it is stored in, as the
// JSON array every backend writes. Entries are otherwise opaque: blank and
// repeated entries are stored as given, as local updates always stored them.
func validateWaiters(waiters []string) error {
	if len(waiters) == 0 {
		return nil
	}
	encoded, err := json.Marshal(waiters)
	if err != nil {
		return gateFieldError(publicops.GateFieldWaiters, "waiters cannot be encoded: %v", err)
	}
	if n := len(encoded); n > types.MaxTextBytes {
		return gateFieldError(publicops.GateFieldWaiters, "waiters encode to %d bytes (max %d)", n, types.MaxTextBytes)
	}
	return nil
}
