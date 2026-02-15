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
func validateStatusWithCustom(value interface{}, customStatuses []string) error {
	if status, ok := value.(string); ok {
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

// validateFieldUpdateWithCustom validates a field update value,
// allowing custom statuses and custom types for their respective field validations.
func validateFieldUpdateWithCustom(key string, value interface{}, customStatuses, customTypes []string) error {
	// Special handling for status field to support custom statuses
	if key == "status" {
		return validateStatusWithCustom(value, customStatuses)
	}
	// Special handling for issue_type field to support custom types (federation trust model)
	if key == "issue_type" {
		return validateIssueTypeWithCustom(value, customTypes)
	}
	if validator, ok := fieldValidators[key]; ok {
		return validator(value)
	}
	return nil
}

// validateFieldUpdateWithCustomStatuses validates a field update value,
// allowing custom statuses for status field validation.
func validateFieldUpdateWithCustomStatuses(key string, value interface{}, customStatuses []string) error {
	return validateFieldUpdateWithCustom(key, value, customStatuses, nil)
}

// validateIssueTypeWithCustom validates an issue type value, allowing custom types.
func validateIssueTypeWithCustom(value interface{}, customTypes []string) error {
	if issueType, ok := value.(string); ok {
		// Normalize first to support aliases like "enhancement" -> "feature"
		normalized := types.IssueType(issueType).Normalize()
		if !normalized.IsValidWithCustom(customTypes) {
			return fmt.Errorf("invalid issue type: %s", issueType)
		}
	}
	return nil
}
