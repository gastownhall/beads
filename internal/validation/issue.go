package validation

import (
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// IssueValidator validates an issue and returns an error if validation fails.
// Validators can be composed using Chain() for complex validation logic.
type IssueValidator func(id string, issue *types.Issue) error

// Chain composes multiple validators into a single validator.
// Validators are executed in order and the first error stops the chain.
func Chain(validators ...IssueValidator) IssueValidator {
	return func(id string, issue *types.Issue) error {
		for _, v := range validators {
			if err := v(id, issue); err != nil {
				return err
			}
		}
		return nil
	}
}

// Exists validates that an issue is not nil.
func Exists() IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return fmt.Errorf("issue %s not found", id)
		}
		return nil
	}
}

// NotTemplate validates that an issue is not a template.
// Templates are read-only and cannot be modified.
func NotTemplate() IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil // Let Exists() handle nil check if needed
		}
		if issue.IsTemplate {
			return fmt.Errorf("cannot modify template %s: templates are read-only; use 'bd mol pour' to create a work item", id)
		}
		return nil
	}
}

// NotPinned validates that an issue is not pinned.
// Returns an error if the issue is pinned, unless force is true.
func NotPinned(force bool) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil // Let Exists() handle nil check if needed
		}
		if !force && issue.Status == types.StatusPinned {
			return fmt.Errorf("cannot modify pinned issue %s (use --force to override)", id)
		}
		return nil
	}
}

// NotClosed validates that an issue is not already closed.
func NotClosed() IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		if issue.Status == types.StatusClosed {
			return fmt.Errorf("issue %s is already closed", id)
		}
		return nil
	}
}

// NotHooked validates that an issue is not in hooked status.
func NotHooked(force bool) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		if !force && issue.Status == types.StatusHooked {
			return fmt.Errorf("cannot modify hooked issue %s (use --force to override)", id)
		}
		return nil
	}
}

// HasStatus validates that an issue has one of the allowed statuses.
func HasStatus(allowed ...types.Status) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		for _, status := range allowed {
			if issue.Status == status {
				return nil
			}
		}
		return fmt.Errorf("issue %s has status %s, expected one of: %v", id, issue.Status, allowed)
	}
}

// HasType validates that an issue has one of the allowed types.
func HasType(allowed ...types.IssueType) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		for _, t := range allowed {
			if issue.IssueType == t {
				return nil
			}
		}
		return fmt.Errorf("issue %s has type %s, expected one of: %v", id, issue.IssueType, allowed)
	}
}

// forUpdate returns a validator chain for update operations.
// Validates: issue exists and is not a template.
func forUpdate() IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
	)
}

// forClose returns a validator chain for close operations.
// Validates: issue exists, is not a template, and is not pinned (unless force).
func forClose(force bool) IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
		NotPinned(force),
	)
}

// forDelete returns a validator chain for delete operations.
// Validates: issue exists and is not a template.
func forDelete() IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
	)
}

// forReopen returns a validator chain for reopen operations.
// Validates: issue exists, is not a template, and is closed.
func forReopen() IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
		HasStatus(types.StatusClosed),
	)
}
