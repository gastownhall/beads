package sqlite

import (
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// validatePriority validates a priority value
func validatePriority(value interface{}) error {
	if priority, ok := value.(int); ok {
		if priority < 0 || priority > 4 {
			return fmt.Errorf("priority must be between 0 and 4 (got %d)", priority)
		}
	}
	return nil
}

// validateStatus validates a status value (built-in statuses only)
func validateStatus(value interface{}) error {
	return validateStatusWithCustom(value, nil)
}

// validateStatusWithCustom validates a status value, allowing custom statuses.
// Note: tombstone status is blocked here (bd-y68) - use bd delete instead of bd update --status=tombstone
func validateStatusWithCustom(value interface{}, customStatuses []string) error {
	if status, ok := value.(string); ok {
		// Block direct status update to tombstone (bd-y68)
		// Tombstones should only be created via bd delete, not bd update --status=tombstone
		if types.Status(status) == types.StatusTombstone {
			return fmt.Errorf("cannot set status to tombstone directly; use 'bd delete' instead")
		}
		if !types.Status(status).IsValidWithCustom(customStatuses) {
			return fmt.Errorf("invalid status: %s", status)
		}
	}
	return nil
}

// validateIssueType validates an issue type value
func validateIssueType(value interface{}) error {
	if issueType, ok := value.(string); ok {
		// Normalize first to support aliases like "enhancement" -> "feature"
		if !types.IssueType(issueType).Normalize().IsValid() {
			return fmt.Errorf("invalid issue type: %s", issueType)
		}
	}
	return nil
}

// validateTitle validates a title value
func validateTitle(value interface{}) error {
	if title, ok := value.(string); ok {
		if len(title) == 0 || len(title) > 500 {
			return fmt.Errorf("title must be 1-500 characters")
		}
	}
	return nil
}

// validateEstimatedMinutes validates an estimated_minutes value
func validateEstimatedMinutes(value interface{}) error {
	if mins, ok := value.(int); ok {
		if mins < 0 {
			return fmt.Errorf("estimated_minutes cannot be negative")
		}
	}
	return nil
}

// fieldValidators maps field names to their validation functions
var fieldValidators = map[string]func(interface{}) error{
	"priority":          validatePriority,
	"status":            validateStatus,
	"issue_type":        validateIssueType,
	"title":             validateTitle,
	"estimated_minutes": validateEstimatedMinutes,
}

// validateFieldUpdate validates a field update value (built-in statuses only)
func validateFieldUpdate(key string, value interface{}) error {
	return validateFieldUpdateWithCustomStatuses(key, value, nil)
}

// validateFieldUpdateWithCustomStatuses validates a field update value,
// allowing custom statuses for status field validation.
func validateFieldUpdateWithCustomStatuses(key string, value interface{}, customStatuses []string) error {
	// Special handling for status field to support custom statuses
	if key == "status" {
		return validateStatusWithCustom(value, customStatuses)
	}
	if validator, ok := fieldValidators[key]; ok {
		return validator(value)
	}
	return nil
}
