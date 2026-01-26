package sqlite

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestValidatePriority(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"valid priority 0", 0, false},
		{"valid priority 1", 1, false},
		{"valid priority 2", 2, false},
		{"valid priority 3", 3, false},
		{"valid priority 4", 4, false},
		{"invalid negative", -1, true},
		{"invalid too high", 5, true},
		{"non-int ignored", "not an int", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePriority(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePriority() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateStatus(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"valid open", string(types.StatusOpen), false},
		{"valid in_progress", string(types.StatusInProgress), false},
		{"valid blocked", string(types.StatusBlocked), false},
		{"valid closed", string(types.StatusClosed), false},
		{"invalid status", "invalid", true},
		{"non-string ignored", 123, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStatus(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStatus() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateIssueType(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"valid bug", string(types.TypeBug), false},
		{"valid feature", string(types.TypeFeature), false},
		{"valid task", string(types.TypeTask), false},
		{"valid epic", string(types.TypeEpic), false},
		{"valid chore", string(types.TypeChore), false},
		{"invalid type", "invalid", true},
		{"non-string ignored", 123, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIssueType(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateIssueType() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTitle(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"valid title", "Valid Title", false},
		{"empty title", "", true},
		{"max length title", string(make([]byte, 500)), false},
		{"too long title", string(make([]byte, 501)), true},
		{"non-string ignored", 123, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTitle(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTitle() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateEstimatedMinutes(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"valid zero", 0, false},
		{"valid positive", 60, false},
		{"invalid negative", -1, true},
		{"non-int ignored", "not an int", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEstimatedMinutes(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateEstimatedMinutes() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateFieldUpdate(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   interface{}
		wantErr bool
	}{
		{"valid priority", "priority", 1, false},
		{"invalid priority", "priority", 5, true},
		{"valid status", "status", string(types.StatusOpen), false},
		{"invalid status", "status", "invalid", true},
		{"unknown field", "unknown_field", "any value", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFieldUpdate(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFieldUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateFieldUpdateWithCustomStatuses(t *testing.T) {
	customStatuses := []string{"awaiting_review", "awaiting_testing"}

	tests := []struct {
		name           string
		key            string
		value          interface{}
		customStatuses []string
		wantErr        bool
	}{
		// Built-in statuses work with or without custom statuses
		{"built-in status no custom", "status", string(types.StatusOpen), nil, false},
		{"built-in status with custom", "status", string(types.StatusOpen), customStatuses, false},
		{"built-in closed with custom", "status", string(types.StatusClosed), customStatuses, false},

		// Custom statuses work when configured
		{"custom status configured", "status", "awaiting_review", customStatuses, false},
		{"custom status awaiting_testing", "status", "awaiting_testing", customStatuses, false},

		// Custom statuses fail without config
		{"custom status not configured", "status", "awaiting_review", nil, true},
		{"custom status not in list", "status", "unknown_status", customStatuses, true},

		// Non-status fields work as before
		{"valid priority", "priority", 1, customStatuses, false},
		{"invalid priority", "priority", 5, customStatuses, true},
		{"unknown field", "unknown_field", "any value", customStatuses, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFieldUpdateWithCustomStatuses(tt.key, tt.value, tt.customStatuses)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFieldUpdateWithCustomStatuses() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateFieldUpdateWithCustom(t *testing.T) {
	customStatuses := []string{"awaiting_review", "awaiting_testing"}
	customTypes := []string{"molecule", "agent", "convoy", "role", "event"}

	tests := []struct {
		name           string
		key            string
		value          interface{}
		customStatuses []string
		customTypes    []string
		wantErr        bool
	}{
		// Custom type validation
		{"valid custom type agent", "issue_type", "agent", customStatuses, customTypes, false},
		{"valid custom type molecule", "issue_type", "molecule", customStatuses, customTypes, false},
		{"valid custom type convoy", "issue_type", "convoy", customStatuses, customTypes, false},
		{"valid built-in type task", "issue_type", "task", customStatuses, customTypes, false},
		{"valid built-in type bug", "issue_type", "bug", customStatuses, customTypes, false},
		{"invalid type", "issue_type", "invalid_type", customStatuses, customTypes, true},
		{"valid type without custom types", "issue_type", "task", customStatuses, nil, false},
		{"custom type without custom types configured", "issue_type", "agent", customStatuses, nil, true},

		// Custom status validation
		{"valid custom status", "status", "awaiting_review", customStatuses, customTypes, false},
		{"valid built-in status", "status", "open", customStatuses, customTypes, false},
		{"invalid status", "status", "invalid_status", customStatuses, customTypes, true},

		// Other field validation
		{"valid priority", "priority", 3, customStatuses, customTypes, false},
		{"invalid priority", "priority", 5, customStatuses, customTypes, true},
		{"valid title", "title", "Test title", customStatuses, customTypes, false},
		{"empty title", "title", "", customStatuses, customTypes, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFieldUpdateWithCustom(tt.key, tt.value, tt.customStatuses, tt.customTypes)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFieldUpdateWithCustom() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseCustomStatuses(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{"empty string", "", nil},
		{"single status", "awaiting_review", []string{"awaiting_review"}},
		{"multiple statuses", "awaiting_review,awaiting_testing", []string{"awaiting_review", "awaiting_testing"}},
		{"with spaces", "awaiting_review, awaiting_testing, awaiting_docs", []string{"awaiting_review", "awaiting_testing", "awaiting_docs"}},
		{"empty entries filtered", "awaiting_review,,awaiting_testing", []string{"awaiting_review", "awaiting_testing"}},
		{"whitespace only entries", "awaiting_review, , awaiting_testing", []string{"awaiting_review", "awaiting_testing"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommaSeparatedList(tt.value)
			if len(got) != len(tt.want) {
				t.Errorf("parseCommaSeparatedList() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseCommaSeparatedList()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
