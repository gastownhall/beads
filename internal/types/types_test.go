package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestIssueValidation(t *testing.T) {
	tests := []struct {
		name    string
		issue   Issue
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid issue",
			issue: Issue{
				ID:          "test-1",
				Title:       "Valid issue",
				Description: "Description",
				Status:      StatusOpen,
				Priority:    2,
				IssueType:   TypeFeature,
			},
			wantErr: false,
		},
		{
			name: "missing title",
			issue: Issue{
				ID:        "test-1",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "title is required",
		},
		{
			name: "title too long",
			issue: Issue{
				ID:        "test-1",
				Title:     string(make([]byte, 501)), // 501 characters
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "title must be 500 characters or less",
		},
		{
			name: "invalid priority too low",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  -1,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "priority must be between 0 and 4",
		},
		{
			name: "invalid priority too high",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  5,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "priority must be between 0 and 4",
		},
		{
			name: "invalid status",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    Status("invalid"),
				Priority:  2,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "invalid status",
		},
		{
			name: "invalid issue type",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: IssueType("invalid"),
			},
			wantErr: true,
			errMsg:  "invalid issue type",
		},
		{
			name: "negative estimated minutes",
			issue: Issue{
				ID:               "test-1",
				Title:            "Test",
				Status:           StatusOpen,
				Priority:         2,
				IssueType:        TypeFeature,
				EstimatedMinutes: intPtr(-10),
			},
			wantErr: true,
			errMsg:  "estimated_minutes cannot be negative",
		},
		{
			name: "valid estimated minutes",
			issue: Issue{
				ID:               "test-1",
				Title:            "Test",
				Status:           StatusOpen,
				Priority:         2,
				IssueType:        TypeFeature,
				EstimatedMinutes: intPtr(60),
			},
			wantErr: false,
		},
		{
			name: "closed issue without closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusClosed,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  nil,
			},
			wantErr: true,
			errMsg:  "closed issues must have closed_at timestamp",
		},
		{
			name: "open issue with closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  timePtr(time.Now()),
			},
			wantErr: true,
			errMsg:  "non-closed issues cannot have closed_at timestamp",
		},
		{
			name: "in_progress issue with closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusInProgress,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  timePtr(time.Now()),
			},
			wantErr: true,
			errMsg:  "non-closed issues cannot have closed_at timestamp",
		},
		{
			name: "closed issue with closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusClosed,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  timePtr(time.Now()),
			},
			wantErr: false,
		},
		{
			name: "ephemeral and no_history both set",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
				Ephemeral: true,
				NoHistory: true,
			},
			wantErr: true,
			errMsg:  "ephemeral and no_history are mutually exclusive",
		},
		{
			name: "ephemeral without no_history",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
				Ephemeral: true,
			},
			wantErr: false,
		},
		{
			name: "no_history without ephemeral",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
				NoHistory: true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.issue.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error containing %q, got nil", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestStatusIsValid(t *testing.T) {
	tests := []struct {
		status Status
		valid  bool
	}{
		{StatusOpen, true},
		{StatusInProgress, true},
		{StatusBlocked, true},
		{StatusClosed, true},
		{Status("invalid"), false},
		{Status(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsValid(); got != tt.valid {
				t.Errorf("Status(%q).IsValid() = %v, want %v", tt.status, got, tt.valid)
			}
		})
	}
}

func TestStatusIsValidWithCustom(t *testing.T) {
	customStatuses := []string{"awaiting_review", "awaiting_testing", "awaiting_docs"}

	tests := []struct {
		name           string
		status         Status
		customStatuses []string
		valid          bool
	}{
		// Built-in statuses should always be valid
		{"built-in open", StatusOpen, nil, true},
		{"built-in open with custom", StatusOpen, customStatuses, true},
		{"built-in closed", StatusClosed, customStatuses, true},

		// Custom statuses with config
		{"custom awaiting_review", Status("awaiting_review"), customStatuses, true},
		{"custom awaiting_testing", Status("awaiting_testing"), customStatuses, true},
		{"custom awaiting_docs", Status("awaiting_docs"), customStatuses, true},

		// Custom statuses without config (should fail)
		{"custom without config", Status("awaiting_review"), nil, false},
		{"custom without config empty", Status("awaiting_review"), []string{}, false},

		// Invalid statuses
		{"invalid status", Status("not_a_status"), customStatuses, false},
		{"empty status", Status(""), customStatuses, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.IsValidWithCustom(tt.customStatuses); got != tt.valid {
				t.Errorf("Status(%q).IsValidWithCustom(%v) = %v, want %v", tt.status, tt.customStatuses, got, tt.valid)
			}
		})
	}
}

func TestValidateWithCustomStatuses(t *testing.T) {
	customStatuses := []string{"awaiting_review", "awaiting_testing"}

	tests := []struct {
		name           string
		issue          Issue
		customStatuses []string
		wantErr        bool
	}{
		{
			name: "valid issue with built-in status",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: nil,
			wantErr:        false,
		},
		{
			name: "valid issue with custom status",
			issue: Issue{
				Title:     "Test Issue",
				Status:    Status("awaiting_review"),
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: customStatuses,
			wantErr:        false,
		},
		{
			name: "invalid custom status without config",
			issue: Issue{
				Title:     "Test Issue",
				Status:    Status("awaiting_review"),
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: nil,
			wantErr:        true,
		},
		{
			name: "invalid custom status not in config",
			issue: Issue{
				Title:     "Test Issue",
				Status:    Status("unknown_status"),
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: customStatuses,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.issue.ValidateWithCustomStatuses(tt.customStatuses)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateWithCustomStatuses() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateForImport tests the federation trust model (bd-9ji4z):
// - Built-in types are validated (catch typos)
// - Non-built-in types are trusted (child repo already validated)
func TestValidateForImport(t *testing.T) {
	tests := []struct {
		name    string
		issue   Issue
		wantErr bool
		errMsg  string
	}{
		{
			name: "built-in type task passes",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: TypeTask,
			},
			wantErr: false,
		},
		{
			name: "built-in type bug passes",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: TypeBug,
			},
			wantErr: false,
		},
		{
			name: "custom type pm is trusted (not in parent config)",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: IssueType("pm"), // Custom type from child repo
			},
			wantErr: false, // Should pass - federation trust model
		},
		{
			name: "custom type llm is trusted",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: IssueType("llm"), // Custom type from child repo
			},
			wantErr: false, // Should pass - federation trust model
		},
		{
			name: "custom type passes (federation trust)",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: IssueType("agent"), // Custom type (no longer built-in)
			},
			wantErr: false,
		},
		{
			name: "empty type defaults to task (handled by SetDefaults)",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: IssueType(""), // Empty is allowed
			},
			wantErr: false,
		},
		{
			name: "other validations still run - missing title",
			issue: Issue{
				Title:     "", // Missing required field
				Status:    StatusOpen,
				Priority:  1,
				IssueType: IssueType("pm"),
			},
			wantErr: true,
			errMsg:  "title is required",
		},
		{
			name: "other validations still run - invalid priority",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  10, // Invalid
				IssueType: IssueType("pm"),
			},
			wantErr: true,
			errMsg:  "priority must be between 0 and 4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.issue.ValidateForImport(nil) // No custom statuses needed for these tests
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateForImport() expected error, got nil")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateForImport() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateForImport() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestValidateForImportVsValidateWithCustom contrasts the two validation modes
func TestValidateForImportVsValidateWithCustom(t *testing.T) {
	// Issue with custom type that's NOT in customTypes list
	issue := Issue{
		Title:     "Test Issue",
		Status:    StatusOpen,
		Priority:  1,
		IssueType: IssueType("pm"), // Custom type not configured in parent
	}

	// ValidateWithCustom (normal mode): should fail without pm in customTypes
	err := issue.ValidateWithCustom(nil, nil)
	if err == nil {
		t.Error("ValidateWithCustom() should fail for custom type without config")
	}

	// ValidateWithCustom: should pass with pm in customTypes
	err = issue.ValidateWithCustom(nil, []string{"pm"})
	if err != nil {
		t.Errorf("ValidateWithCustom() with pm config should pass, got: %v", err)
	}

	// ValidateForImport (federation trust mode): should pass without any config
	err = issue.ValidateForImport(nil)
	if err != nil {
		t.Errorf("ValidateForImport() should trust custom type, got: %v", err)
	}
}

func TestIssueTypeIsValid(t *testing.T) {
	tests := []struct {
		issueType IssueType
		valid     bool
	}{
		// Core work types are always valid
		{TypeBug, true},
		{TypeFeature, true},
		{TypeTask, true},
		{TypeEpic, true},
		{TypeChore, true},
		{TypeDecision, true},
		{TypeMessage, true},
		// Molecule is a core type (used by swarm create)
		{IssueType("molecule"), true},
		// Gate is a core type (used by bd gate, formula gates — GH#3213)
		{IssueType("gate"), true},
		// Remaining orchestrator types are custom types (not built-in)
		{IssueType("merge-request"), false},
		{IssueType("agent"), false},
		{IssueType("role"), false},
		{IssueType("convoy"), false},
		{TypeEvent, false},
		{IssueType("slot"), false},
		{IssueType("rig"), false},
		// Invalid types
		{IssueType("invalid"), false},
		{IssueType(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.issueType), func(t *testing.T) {
			if got := tt.issueType.IsValid(); got != tt.valid {
				t.Errorf("IssueType(%q).IsValid() = %v, want %v", tt.issueType, got, tt.valid)
			}
		})
	}
}

// TestEventTypeValidation verifies that event type is accepted by validation
// even without being in types.custom, since set-state creates event beads
// internally for audit trail (GH#1356).
func TestEventTypeValidation(t *testing.T) {
	now := time.Now()
	event := Issue{
		Title:     "state change event",
		Status:    StatusOpen,
		Priority:  4,
		IssueType: TypeEvent,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// event is not a core work type
	if TypeEvent.IsValid() {
		t.Fatal("event should not be a core work type")
	}

	// event is an internal built-in type
	if !TypeEvent.IsBuiltIn() {
		t.Error("TypeEvent.IsBuiltIn() = false, want true")
	}

	// event should be accepted by IsValidWithCustom without explicit config
	if !TypeEvent.IsValidWithCustom(nil) {
		t.Error("TypeEvent.IsValidWithCustom(nil) = false, want true")
	}

	// ValidateWithCustom should accept event without custom types config
	if err := event.ValidateWithCustom(nil, nil); err != nil {
		t.Errorf("ValidateWithCustom() should accept event type, got: %v", err)
	}

	// event should also work alongside other custom types
	if !TypeEvent.IsValidWithCustom([]string{"molecule", "gate"}) {
		t.Error("TypeEvent.IsValidWithCustom(custom list) = false, want true")
	}

	// molecule is now a built-in type (used by swarm create)
	if !IssueType("molecule").IsBuiltIn() {
		t.Error("IssueType(molecule).IsBuiltIn() = false, want true")
	}
	// gate is now a built-in type (used by bd gate, formula gates — GH#3213)
	if !IssueType("gate").IsBuiltIn() {
		t.Error("IssueType(gate).IsBuiltIn() = false, want true")
	}

	// Normalize must not map event to a core type
	if TypeEvent.Normalize() != TypeEvent {
		t.Errorf("TypeEvent.Normalize() = %q, want %q", TypeEvent.Normalize(), TypeEvent)
	}

	// decision aliases
	if IssueType("dec").Normalize() != TypeDecision {
		t.Errorf("IssueType(dec).Normalize() = %q, want %q", IssueType("dec").Normalize(), TypeDecision)
	}
	if IssueType("adr").Normalize() != TypeDecision {
		t.Errorf("IssueType(adr).Normalize() = %q, want %q", IssueType("adr").Normalize(), TypeDecision)
	}
}

func TestIssueTypeRequiredSections(t *testing.T) {
	tests := []struct {
		issueType     IssueType
		expectCount   int
		expectHeading string // First heading if any
	}{
		{TypeBug, 2, "## Steps to Reproduce"},
		{TypeFeature, 1, "## Acceptance Criteria"},
		{TypeTask, 1, "## Acceptance Criteria"},
		{TypeEpic, 1, "## Success Criteria"},
		{TypeDecision, 3, "## Decision"},
		{TypeChore, 0, ""},
		{TypeMessage, 0, ""},
		// Orchestrator types are now custom and have no required sections
		{IssueType("molecule"), 0, ""},
		{IssueType("gate"), 0, ""},
		{TypeEvent, 0, ""},
		{IssueType("merge-request"), 0, ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.issueType), func(t *testing.T) {
			sections := tt.issueType.RequiredSections()
			if len(sections) != tt.expectCount {
				t.Errorf("IssueType(%q).RequiredSections() returned %d sections, want %d",
					tt.issueType, len(sections), tt.expectCount)
			}
			if tt.expectCount > 0 && sections[0].Heading != tt.expectHeading {
				t.Errorf("IssueType(%q).RequiredSections()[0].Heading = %q, want %q",
					tt.issueType, sections[0].Heading, tt.expectHeading)
			}
		})
	}
}

func TestMolTypeIsValid(t *testing.T) {
	cases := []struct {
		name  string
		type_ MolType
		want  bool
	}{
		{"swarm", MolTypeSwarm, true},
		{"patrol", MolTypePatrol, true},
		{"work", MolTypeWork, true},
		{"empty", MolType(""), true},
		{"unknown", MolType("custom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.type_.IsValid(); got != tc.want {
				t.Fatalf("MolType(%q).IsValid() = %v, want %v", tc.type_, got, tc.want)
			}
		})
	}
}

func TestIssueCompoundHelpers(t *testing.T) {
	issue := &Issue{}
	if issue.IsCompound() {
		t.Fatalf("issue with no bonded refs should not be compound")
	}
	if constituents := issue.GetConstituents(); constituents != nil {
		t.Fatalf("expected nil constituents for non-compound issue")
	}

	bonded := &Issue{BondedFrom: []BondRef{{SourceID: "proto-1", BondType: BondTypeSequential}}}
	if !bonded.IsCompound() {
		t.Fatalf("issue with bonded refs should be compound")
	}
	refs := bonded.GetConstituents()
	if len(refs) != 1 || refs[0].SourceID != "proto-1" {
		t.Fatalf("unexpected constituents: %#v", refs)
	}
}

func TestDependencyTypeIsValid(t *testing.T) {
	// IsValid now accepts any non-empty string up to 50 chars (Decision 004)
	tests := []struct {
		depType DependencyType
		valid   bool
	}{
		{DepBlocks, true},
		{DepRelated, true},
		{DepParentChild, true},
		{DepDiscoveredFrom, true},
		{DepRepliesTo, true},
		{DepRelatesTo, true},
		{DepDuplicates, true},
		{DepSupersedes, true},
		{DepAuthoredBy, true},
		{DepAssignedTo, true},
		{DepApprovedBy, true},
		{DependencyType("custom-type"), true}, // Custom types are now valid
		{DependencyType("any-string"), true},  // Any non-empty string is valid
		{DependencyType(""), false},           // Empty is still invalid
		{DependencyType("this-is-a-very-long-dependency-type-that-exceeds-fifty-characters"), false}, // Too long
	}

	for _, tt := range tests {
		t.Run(string(tt.depType), func(t *testing.T) {
			if got := tt.depType.IsValid(); got != tt.valid {
				t.Errorf("DependencyType(%q).IsValid() = %v, want %v", tt.depType, got, tt.valid)
			}
		})
	}
}

func TestDependencyTypeIsWellKnown(t *testing.T) {
	tests := []struct {
		depType   DependencyType
		wellKnown bool
	}{
		{DepBlocks, true},
		{DepRelated, true},
		{DepParentChild, true},
		{DepDiscoveredFrom, true},
		{DepRepliesTo, true},
		{DepRelatesTo, true},
		{DepDuplicates, true},
		{DepSupersedes, true},
		{DepAuthoredBy, true},
		{DepAssignedTo, true},
		{DepApprovedBy, true},
		{DepAttests, true},
		{DepTracks, true},
		{DepUntil, true},
		{DepCausedBy, true},
		{DepValidates, true},
		{DependencyType("custom-type"), false},
		{DependencyType("unknown"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.depType), func(t *testing.T) {
			if got := tt.depType.IsWellKnown(); got != tt.wellKnown {
				t.Errorf("DependencyType(%q).IsWellKnown() = %v, want %v", tt.depType, got, tt.wellKnown)
			}
		})
	}
}

func TestDependencyTypeAffectsReadyWork(t *testing.T) {
	tests := []struct {
		depType DependencyType
		affects bool
	}{
		{DepBlocks, true},
		{DepParentChild, true},
		{DepConditionalBlocks, true},
		{DepWaitsFor, true},
		{DepRelated, false},
		{DepDiscoveredFrom, false},
		{DepRepliesTo, false},
		{DepRelatesTo, false},
		{DepDuplicates, false},
		{DepSupersedes, false},
		{DepAuthoredBy, false},
		{DepAssignedTo, false},
		{DepApprovedBy, false},
		{DepAttests, false},
		{DepTracks, false},
		{DepUntil, false},
		{DepCausedBy, false},
		{DepValidates, false},
		{DependencyType("custom-type"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.depType), func(t *testing.T) {
			if got := tt.depType.AffectsReadyWork(); got != tt.affects {
				t.Errorf("DependencyType(%q).AffectsReadyWork() = %v, want %v", tt.depType, got, tt.affects)
			}
		})
	}
}

func TestParseWaitsForGateMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     string
	}{
		{
			name:     "empty defaults to all-children",
			metadata: "",
			want:     WaitsForAllChildren,
		},
		{
			name:     "invalid json defaults to all-children",
			metadata: "{bad",
			want:     WaitsForAllChildren,
		},
		{
			name:     "all-children metadata",
			metadata: `{"gate":"all-children"}`,
			want:     WaitsForAllChildren,
		},
		{
			name:     "any-children metadata",
			metadata: `{"gate":"any-children"}`,
			want:     WaitsForAnyChildren,
		},
		{
			name:     "unknown gate defaults to all-children",
			metadata: `{"gate":"something-else"}`,
			want:     WaitsForAllChildren,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseWaitsForGateMetadata(tt.metadata)
			if got != tt.want {
				t.Fatalf("ParseWaitsForGateMetadata(%q) = %q, want %q", tt.metadata, got, tt.want)
			}
		})
	}
}

func TestIsFailureClose(t *testing.T) {
	tests := []struct {
		name        string
		closeReason string
		isFailure   bool
	}{
		// Failure keywords
		{"failed", "Task failed due to timeout", true},
		{"rejected", "PR was rejected by reviewer", true},
		{"wontfix", "Closed as wontfix", true},
		{"won't fix", "Won't fix - by design", true},
		{"cancelled", "Work cancelled", true},
		{"canceled", "Work canceled", true},
		{"abandoned", "Abandoned feature", true},
		{"blocked", "Blocked by external dependency", true},
		{"error", "Encountered error during execution", true},
		{"timeout", "Test timeout exceeded", true},
		{"aborted", "Build aborted", true},

		// Case insensitive
		{"FAILED upper", "FAILED", true},
		{"Failed mixed", "Failed to build", true},

		// Success cases (no failure keywords)
		{"completed", "Completed successfully", false},
		{"done", "Done", false},
		{"merged", "Merged to main", false},
		{"fixed", "Bug fixed", false},
		{"implemented", "Feature implemented", false},
		{"empty", "", false},

		// Partial matches should work
		{"prefixed", "prefailed", true}, // contains "failed"
		{"suffixed", "failedtest", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFailureClose(tt.closeReason); got != tt.isFailure {
				t.Errorf("IsFailureClose(%q) = %v, want %v", tt.closeReason, got, tt.isFailure)
			}
		})
	}
}

func TestIssueStructFields(t *testing.T) {
	// Test that all time fields work correctly
	now := time.Now()
	closedAt := now.Add(time.Hour)

	issue := Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		Status:      StatusClosed,
		Priority:    1,
		IssueType:   TypeBug,
		CreatedAt:   now,
		UpdatedAt:   now,
		ClosedAt:    &closedAt,
	}

	if issue.CreatedAt != now {
		t.Errorf("CreatedAt = %v, want %v", issue.CreatedAt, now)
	}
	if issue.ClosedAt == nil || *issue.ClosedAt != closedAt {
		t.Errorf("ClosedAt = %v, want %v", issue.ClosedAt, closedAt)
	}
}

// TestIssueLeaseJSONSerialization verifies the leasing columns (migration 0054)
// are surfaced in JSON when present and omitted when absent, while the internal
// row_lock is never exposed. Backs `bd show <id> --json` (wy-9cdw).
func TestIssueLeaseJSONSerialization(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(15 * time.Minute)

	// With an active lease: both keys appear, row_lock never does.
	leased := IssueDetails{Issue: Issue{
		ID:             "test-1",
		Title:          "Leased",
		Status:         StatusInProgress,
		LeaseExpiresAt: &expires,
		HeartbeatAt:    &now,
	}}
	b, err := json.Marshal(leased)
	if err != nil {
		t.Fatalf("marshal leased issue: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["lease_expires_at"]; !ok {
		t.Errorf("expected lease_expires_at in JSON, got: %s", b)
	}
	if _, ok := m["heartbeat_at"]; !ok {
		t.Errorf("expected heartbeat_at in JSON, got: %s", b)
	}
	if _, ok := m["row_lock"]; ok {
		t.Errorf("row_lock must never be surfaced, got: %s", b)
	}

	// Without a lease (the common case): keys are omitted, not null.
	unleased := IssueDetails{Issue: Issue{ID: "test-2", Title: "Open", Status: StatusOpen}}
	b2, err := json.Marshal(unleased)
	if err != nil {
		t.Fatalf("marshal unleased issue: %v", err)
	}
	if strings.Contains(string(b2), "lease_expires_at") {
		t.Errorf("lease_expires_at should be omitted when nil, got: %s", b2)
	}
	if strings.Contains(string(b2), "heartbeat_at") {
		t.Errorf("heartbeat_at should be omitted when nil, got: %s", b2)
	}
}

// TestRowVersionNeverSerialized locks in the Go-only decision for RowVersion:
// it is a live Go field (library call sites build an optimistic-concurrency
// token from it) but json:"-", so its random-per-write value never reaches any
// bd --json surface or bd export. Serializing it would break stable protocol
// goldens and export round-trips because the value is regenerated on every
// write. The value must be absent from the bare Issue and from the two
// embedding wrappers that back show/list/ready (IssueDetails, IssueWithCounts).
func TestRowVersionNeverSerialized(t *testing.T) {
	iss := Issue{ID: "test-1", Title: "Versioned", Status: StatusOpen, RowVersion: 123456789}

	// The Go field stays populated — this is what library call sites read.
	if iss.RowVersion != 123456789 {
		t.Fatalf("RowVersion Go field = %d, want 123456789", iss.RowVersion)
	}

	surfaces := []struct {
		name string
		v    any
	}{
		{"Issue", iss},
		{"IssueDetails", IssueDetails{Issue: iss}},
		{"IssueWithCounts", IssueWithCounts{Issue: &iss}},
	}
	for _, tc := range surfaces {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("marshal %s: %v", tc.name, err)
		}
		s := string(b)
		for _, forbidden := range []string{"row_version", "RowVersion", "row_lock", "123456789"} {
			if strings.Contains(s, forbidden) {
				t.Errorf("%s JSON must not contain %q, got: %s", tc.name, forbidden, s)
			}
		}
	}
}

func TestReclaimedLeaseJSONSerialization(t *testing.T) {
	b, err := json.Marshal(ReclaimedLease{ID: "bd-1", PreviousOwner: "worker-a"})
	if err != nil {
		t.Fatalf("marshal reclaimed lease: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal reclaimed lease: %v", err)
	}
	if m["id"] != "bd-1" || m["previous_owner"] != "worker-a" {
		t.Fatalf("reclaimed lease JSON = %s, want snake_case id/previous_owner", b)
	}
	if _, ok := m["ID"]; ok {
		t.Fatalf("reclaimed lease JSON leaked Go field name: %s", b)
	}
	if _, ok := m["PreviousOwner"]; ok {
		t.Fatalf("reclaimed lease JSON leaked Go field name: %s", b)
	}
}

func TestBlockedIssueEmbedding(t *testing.T) {
	blocked := BlockedIssue{
		Issue: Issue{
			ID:        "test-1",
			Title:     "Blocked issue",
			Status:    StatusBlocked,
			Priority:  2,
			IssueType: TypeFeature,
		},
		BlockedByCount: 2,
		BlockedBy:      []string{"test-2", "test-3"},
	}

	// Test that embedded Issue fields are accessible
	if blocked.ID != "test-1" {
		t.Errorf("BlockedIssue.ID = %q, want %q", blocked.ID, "test-1")
	}
	if blocked.BlockedByCount != 2 {
		t.Errorf("BlockedByCount = %d, want 2", blocked.BlockedByCount)
	}
	if len(blocked.BlockedBy) != 2 {
		t.Errorf("len(BlockedBy) = %d, want 2", len(blocked.BlockedBy))
	}
}

func TestTreeNodeEmbedding(t *testing.T) {
	node := TreeNode{
		Issue: Issue{
			ID:        "test-1",
			Title:     "Root node",
			Status:    StatusOpen,
			Priority:  1,
			IssueType: TypeEpic,
		},
		Depth:     0,
		Truncated: false,
	}

	// Test that embedded Issue fields are accessible
	if node.ID != "test-1" {
		t.Errorf("TreeNode.ID = %q, want %q", node.ID, "test-1")
	}
	if node.Depth != 0 {
		t.Errorf("Depth = %d, want 0", node.Depth)
	}
}

func TestComputeContentHash(t *testing.T) {
	issue1 := Issue{
		ID:               "test-1",
		Title:            "Test Issue",
		Description:      "Description",
		Status:           StatusOpen,
		Priority:         2,
		IssueType:        TypeFeature,
		EstimatedMinutes: intPtr(60),
	}

	// Same content should produce same hash
	issue2 := Issue{
		ID:               "test-2", // Different ID
		Title:            "Test Issue",
		Description:      "Description",
		Status:           StatusOpen,
		Priority:         2,
		IssueType:        TypeFeature,
		EstimatedMinutes: intPtr(60),
		CreatedAt:        time.Now(), // Different timestamp
	}

	hash1 := issue1.ComputeContentHash()
	hash2 := issue2.ComputeContentHash()

	if hash1 != hash2 {
		t.Errorf("Expected same hash for identical content, got %s and %s", hash1, hash2)
	}

	// Different content should produce different hash
	issue3 := issue1
	issue3.Title = "Different Title"
	hash3 := issue3.ComputeContentHash()

	if hash1 == hash3 {
		t.Errorf("Expected different hash for different content")
	}

	// Test with external ref
	externalRef := "EXT-123"
	issue4 := issue1
	issue4.ExternalRef = &externalRef
	hash4 := issue4.ComputeContentHash()

	if hash1 == hash4 {
		t.Errorf("Expected different hash when external ref is present")
	}
}

func TestSortPolicyIsValid(t *testing.T) {
	tests := []struct {
		policy SortPolicy
		valid  bool
	}{
		{SortPolicyHybrid, true},
		{SortPolicyPriority, true},
		{SortPolicyOldest, true},
		{SortPolicy(""), true}, // empty is valid
		{SortPolicy("invalid"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.policy), func(t *testing.T) {
			if got := tt.policy.IsValid(); got != tt.valid {
				t.Errorf("SortPolicy(%q).IsValid() = %v, want %v", tt.policy, got, tt.valid)
			}
		})
	}
}

// Helper functions

func intPtr(i int) *int {
	return &i
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSetDefaults(t *testing.T) {
	tests := []struct {
		name           string
		issue          Issue
		expectedStatus Status
		expectedType   IssueType
	}{
		{
			name:           "empty fields get defaults",
			issue:          Issue{Title: "Test"},
			expectedStatus: StatusOpen,
			expectedType:   TypeTask,
		},
		{
			name: "existing status preserved",
			issue: Issue{
				Title:  "Test",
				Status: StatusInProgress,
			},
			expectedStatus: StatusInProgress,
			expectedType:   TypeTask,
		},
		{
			name: "existing type preserved",
			issue: Issue{
				Title:     "Test",
				IssueType: TypeBug,
			},
			expectedStatus: StatusOpen,
			expectedType:   TypeBug,
		},
		{
			name: "all fields set - no changes",
			issue: Issue{
				Title:     "Test",
				Status:    StatusClosed,
				IssueType: TypeFeature,
				ClosedAt:  timePtr(time.Now()),
			},
			expectedStatus: StatusClosed,
			expectedType:   TypeFeature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := tt.issue
			issue.SetDefaults()

			if issue.Status != tt.expectedStatus {
				t.Errorf("SetDefaults() Status = %v, want %v", issue.Status, tt.expectedStatus)
			}
			if issue.IssueType != tt.expectedType {
				t.Errorf("SetDefaults() IssueType = %v, want %v", issue.IssueType, tt.expectedType)
			}
		})
	}
}

func TestParseCustomStatusConfig(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []CustomStatus
		wantErr string
	}{
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  nil,
		},
		{
			name:  "single flat status (legacy format)",
			input: "review",
			want:  []CustomStatus{{Name: "review", Category: CategoryUnspecified}},
		},
		{
			name:  "multiple flat statuses (legacy format)",
			input: "review,qa,on-hold",
			want: []CustomStatus{
				{Name: "review", Category: CategoryUnspecified},
				{Name: "qa", Category: CategoryUnspecified},
				{Name: "on-hold", Category: CategoryUnspecified},
			},
		},
		{
			name:  "single categorized status",
			input: "review:active",
			want:  []CustomStatus{{Name: "review", Category: CategoryActive}},
		},
		{
			name:  "all category types",
			input: "review:active,testing:wip,done-review:done,on-ice:frozen",
			want: []CustomStatus{
				{Name: "review", Category: CategoryActive},
				{Name: "testing", Category: CategoryWIP},
				{Name: "done-review", Category: CategoryDone},
				{Name: "on-ice", Category: CategoryFrozen},
			},
		},
		{
			name:  "mixed legacy and categorized",
			input: "review,testing:wip,qa",
			want: []CustomStatus{
				{Name: "review", Category: CategoryUnspecified},
				{Name: "testing", Category: CategoryWIP},
				{Name: "qa", Category: CategoryUnspecified},
			},
		},
		{
			name:  "whitespace around entries",
			input: " review:active , testing:wip , qa ",
			want: []CustomStatus{
				{Name: "review", Category: CategoryActive},
				{Name: "testing", Category: CategoryWIP},
				{Name: "qa", Category: CategoryUnspecified},
			},
		},
		{
			name:  "trailing comma ignored",
			input: "review:active,",
			want:  []CustomStatus{{Name: "review", Category: CategoryActive}},
		},
		{
			name:    "trailing colon with empty category",
			input:   "review:",
			wantErr: "trailing colon with empty category",
		},
		{
			name:    "invalid category",
			input:   "review:invalid",
			wantErr: "invalid category",
		},
		{
			name:    "uppercase in name",
			input:   "Review:active",
			wantErr: "must match",
		},
		{
			name:    "space in name",
			input:   "my status:active",
			wantErr: "must match",
		},
		{
			name:    "digit-first name",
			input:   "1review:active",
			wantErr: "must match",
		},
		{
			name:    "hyphen-first name",
			input:   "-review:active",
			wantErr: "must match",
		},
		{
			name:  "empty name from leading comma",
			input: ",review:active",
			want:  []CustomStatus{{Name: "review", Category: CategoryActive}},
		},
		{
			name:    "collision with built-in open",
			input:   "open:active",
			wantErr: "collides with built-in",
		},
		{
			name:    "collision with built-in closed",
			input:   "closed:done",
			wantErr: "collides with built-in",
		},
		{
			name:    "collision with built-in in_progress",
			input:   "in_progress:wip",
			wantErr: "collides with built-in",
		},
		{
			name:    "duplicate name",
			input:   "review:active,review:wip",
			wantErr: "duplicate",
		},
		{
			name:  "name with underscores and hyphens",
			input: "in-review:active,needs_qa:wip",
			want: []CustomStatus{
				{Name: "in-review", Category: CategoryActive},
				{Name: "needs_qa", Category: CategoryWIP},
			},
		},
		{
			name:  "name with digits after first letter",
			input: "stage2:active,qa3-check:wip",
			want: []CustomStatus{
				{Name: "stage2", Category: CategoryActive},
				{Name: "qa3-check", Category: CategoryWIP},
			},
		},
		{
			name:    "colon in category portion (first-colon split)",
			input:   "review:active:extra",
			wantErr: "invalid category",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCustomStatusConfig(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statuses, want %d", len(got), len(tt.want))
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name || g.Category != tt.want[i].Category {
					t.Errorf("status[%d] = {%q, %q}, want {%q, %q}",
						i, g.Name, g.Category, tt.want[i].Name, tt.want[i].Category)
				}
			}
		})
	}
}

func TestParseCustomStatusConfigMaxLimit(t *testing.T) {
	// Build a config string with 51 statuses
	parts := make([]string, 51)
	for i := range parts {
		parts[i] = fmt.Sprintf("s%d", i)
	}
	input := strings.Join(parts, ",")
	_, err := ParseCustomStatusConfig(input)
	if err == nil {
		t.Fatal("expected error for >50 custom statuses")
	}
	if !contains(err.Error(), "too many") {
		t.Fatalf("expected 'too many' error, got %q", err.Error())
	}
}

func TestCustomStatusNames(t *testing.T) {
	statuses := []CustomStatus{
		{Name: "review", Category: CategoryActive},
		{Name: "testing", Category: CategoryWIP},
	}
	names := CustomStatusNames(statuses)
	if len(names) != 2 || names[0] != "review" || names[1] != "testing" {
		t.Errorf("got %v, want [review testing]", names)
	}

	// nil input
	if got := CustomStatusNames(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestCustomStatusesByCategory(t *testing.T) {
	statuses := []CustomStatus{
		{Name: "review", Category: CategoryActive},
		{Name: "testing", Category: CategoryWIP},
		{Name: "qa", Category: CategoryActive},
		{Name: "archived", Category: CategoryDone},
	}

	active := CustomStatusesByCategory(statuses, CategoryActive)
	if len(active) != 2 || active[0].Name != "review" || active[1].Name != "qa" {
		t.Errorf("active = %v, want [review, qa]", active)
	}

	done := CustomStatusesByCategory(statuses, CategoryDone)
	if len(done) != 1 || done[0].Name != "archived" {
		t.Errorf("done = %v, want [archived]", done)
	}

	frozen := CustomStatusesByCategory(statuses, CategoryFrozen)
	if len(frozen) != 0 {
		t.Errorf("frozen = %v, want []", frozen)
	}
}

func TestBuiltInStatusCategory(t *testing.T) {
	tests := []struct {
		status Status
		want   StatusCategory
	}{
		{StatusOpen, CategoryActive},
		{StatusInProgress, CategoryWIP},
		{StatusBlocked, CategoryWIP},
		{StatusHooked, CategoryWIP},
		{StatusClosed, CategoryDone},
		{StatusDeferred, CategoryFrozen},
		{StatusPinned, CategoryFrozen},
	}
	for _, tt := range tests {
		got := BuiltInStatusCategory(tt.status)
		if got != tt.want {
			t.Errorf("BuiltInStatusCategory(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestIsValidWithCustomStatuses(t *testing.T) {
	customs := []CustomStatus{
		{Name: "review", Category: CategoryActive},
		{Name: "testing", Category: CategoryWIP},
	}

	// Built-in status is always valid
	if !Status("open").IsValidWithCustomStatuses(customs) {
		t.Error("open should be valid")
	}

	// Custom status is valid
	if !Status("review").IsValidWithCustomStatuses(customs) {
		t.Error("review should be valid")
	}

	// Unknown status is not valid
	if Status("unknown").IsValidWithCustomStatuses(customs) {
		t.Error("unknown should not be valid")
	}
}

func TestParseCustomStatusConfigEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []CustomStatus
		wantErr string
	}{
		{
			name:    "trailing colon rejected",
			input:   "review:",
			wantErr: "trailing colon with empty category",
		},
		{
			name:    "double colon invalid category",
			input:   "review::active",
			wantErr: "invalid category",
		},
		{
			name:  "name with numbers v2-review",
			input: "v2-review:active",
			want:  []CustomStatus{{Name: "v2-review", Category: CategoryActive}},
		},
		{
			name:    "name starting with digit",
			input:   "2review:active",
			wantErr: "must match",
		},
		{
			name:  "very long valid name",
			input: "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnop:active",
			want:  []CustomStatus{{Name: "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnop", Category: CategoryActive}},
		},
		{
			name:    "unicode in name rejected",
			input:   "über:active",
			wantErr: "must match",
		},
		{
			name:    "emoji in name rejected",
			input:   "review🔥:active",
			wantErr: "must match",
		},
		{
			name:  "single char name",
			input: "r:active",
			want:  []CustomStatus{{Name: "r", Category: CategoryActive}},
		},
		{
			name:    "underscore-first name rejected",
			input:   "_review:active",
			wantErr: "must match",
		},
		{
			name:  "multiple empty entries filtered",
			input: ",,review:active,,testing:wip,,",
			want: []CustomStatus{
				{Name: "review", Category: CategoryActive},
				{Name: "testing", Category: CategoryWIP},
			},
		},
		{
			name:    "category unspecified not user-assignable",
			input:   "review:unspecified",
			wantErr: "invalid category",
		},
		{
			name:    "all built-in collisions",
			input:   "blocked:wip",
			wantErr: "collides with built-in",
		},
		{
			name:    "hooked built-in collision",
			input:   "hooked:wip",
			wantErr: "collides with built-in",
		},
		{
			name:    "deferred built-in collision",
			input:   "deferred:frozen",
			wantErr: "collides with built-in",
		},
		{
			name:    "pinned built-in collision",
			input:   "pinned:frozen",
			wantErr: "collides with built-in",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCustomStatusConfig(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statuses, want %d", len(got), len(tt.want))
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name || g.Category != tt.want[i].Category {
					t.Errorf("status[%d] = {%q, %q}, want {%q, %q}",
						i, g.Name, g.Category, tt.want[i].Name, tt.want[i].Category)
				}
			}
		})
	}
}

func TestCommentUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantID     string
		wantAuthor string
		wantText   string
		wantErr    bool
	}{
		{
			name:       "string ID (v1.0+)",
			input:      `{"id":"uuid-abc","author":"alice","text":"hello","created_at":"2025-01-01T00:00:00Z"}`,
			wantID:     "uuid-abc",
			wantAuthor: "alice",
			wantText:   "hello",
		},
		{
			name:       "numeric ID (pre-v1.0)",
			input:      `{"id":42,"author":"bob","text":"old comment","created_at":"2025-01-01T00:00:00Z"}`,
			wantID:     "42",
			wantAuthor: "bob",
			wantText:   "old comment",
		},
		{
			name:   "zero numeric ID",
			input:  `{"id":0,"author":"sys","text":"auto","created_at":"2025-01-01T00:00:00Z"}`,
			wantID: "0",
		},
		{
			name:   "large numeric ID",
			input:  `{"id":9999999,"author":"alice","text":"big","created_at":"2025-01-01T00:00:00Z"}`,
			wantID: "9999999",
		},
		{
			name:   "missing ID field",
			input:  `{"author":"alice","text":"no id","created_at":"2025-01-01T00:00:00Z"}`,
			wantID: "",
		},
		{
			name:    "invalid JSON",
			input:   `{not valid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Comment
			err := json.Unmarshal([]byte(tt.input), &c)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", c.ID, tt.wantID)
			}
			if tt.wantAuthor != "" && c.Author != tt.wantAuthor {
				t.Errorf("Author = %q, want %q", c.Author, tt.wantAuthor)
			}
			if tt.wantText != "" && c.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", c.Text, tt.wantText)
			}
		})
	}
}

func TestBondRefUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantSourceID string
		wantBondType string
		wantErr      bool
	}{
		{
			name:         "current format with source_id",
			input:        `{"source_id":"bd-src1","bond_type":"sequential"}`,
			wantSourceID: "bd-src1",
			wantBondType: "sequential",
		},
		{
			name:         "legacy format with proto_id",
			input:        `{"proto_id":"bd-old1","bond_type":"parallel"}`,
			wantSourceID: "bd-old1",
			wantBondType: "parallel",
		},
		{
			name:         "both fields — source_id takes precedence",
			input:        `{"source_id":"bd-new","proto_id":"bd-old","bond_type":"conditional"}`,
			wantSourceID: "bd-new",
			wantBondType: "conditional",
		},
		{
			name:         "neither field present",
			input:        `{"bond_type":"sequential"}`,
			wantSourceID: "",
			wantBondType: "sequential",
		},
		{
			name:    "invalid JSON",
			input:   `{not valid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b BondRef
			err := json.Unmarshal([]byte(tt.input), &b)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if b.SourceID != tt.wantSourceID {
				t.Errorf("SourceID = %q, want %q", b.SourceID, tt.wantSourceID)
			}
			if b.BondType != tt.wantBondType {
				t.Errorf("BondType = %q, want %q", b.BondType, tt.wantBondType)
			}
		})
	}
}

// validIssue returns a minimal issue that passes ValidateWithCustom, so
// field-length tests below isolate the assignee/owner bound.
func validIssue() Issue {
	return Issue{
		Title:     "Test Issue",
		Status:    StatusOpen,
		Priority:  1,
		IssueType: TypeTask,
	}
}

// TestValidateFieldLength proves ValidateWithCustom bounds assignee and owner at
// MaxFieldLen and that the bound is measured in runes, not bytes: a 255-rune
// multibyte value (~510 bytes) fits the VARCHAR(255) column and passes, while a
// 256-rune value is rejected with a typed ErrFieldTooLong.
func TestValidateFieldLength(t *testing.T) {
	// "é" (U+00E9) encodes as 2 bytes, so 255 of them is 255 runes / 510 bytes.
	const multibyte = "é"

	tests := []struct {
		name    string
		mutate  func(*Issue)
		wantErr bool
	}{
		{
			name:    "255-rune assignee passes",
			mutate:  func(i *Issue) { i.Assignee = strings.Repeat("a", MaxFieldLen) },
			wantErr: false,
		},
		{
			name:    "256-rune assignee fails",
			mutate:  func(i *Issue) { i.Assignee = strings.Repeat("a", MaxFieldLen+1) },
			wantErr: true,
		},
		{
			name:    "255-rune owner passes",
			mutate:  func(i *Issue) { i.Owner = strings.Repeat("o", MaxFieldLen) },
			wantErr: false,
		},
		{
			name:    "256-rune owner fails",
			mutate:  func(i *Issue) { i.Owner = strings.Repeat("o", MaxFieldLen+1) },
			wantErr: true,
		},
		{
			name:    "255-rune multibyte assignee passes (rune-count, not byte-count)",
			mutate:  func(i *Issue) { i.Assignee = strings.Repeat(multibyte, MaxFieldLen) },
			wantErr: false,
		},
		{
			name:    "256-rune multibyte assignee fails",
			mutate:  func(i *Issue) { i.Assignee = strings.Repeat(multibyte, MaxFieldLen+1) },
			wantErr: true,
		},
		{
			name:    "255-rune multibyte owner passes (rune-count, not byte-count)",
			mutate:  func(i *Issue) { i.Owner = strings.Repeat(multibyte, MaxFieldLen) },
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := validIssue()
			tt.mutate(&issue)
			err := issue.ValidateWithCustom(nil, nil)
			if tt.wantErr {
				if !errors.Is(err, ErrFieldTooLong) {
					t.Errorf("ValidateWithCustom() error = %v, want errors.Is(ErrFieldTooLong)", err)
				}
			} else if err != nil {
				t.Errorf("ValidateWithCustom() error = %v, want nil", err)
			}
		})
	}
}

// TestValidateForImportFieldLength proves the import path bounds assignee and
// owner too, so a federated import can't smuggle in an over-length value that
// the backend would otherwise reject with a raw "data too long" error.
func TestValidateForImportFieldLength(t *testing.T) {
	t.Run("256-rune assignee fails", func(t *testing.T) {
		issue := validIssue()
		issue.Assignee = strings.Repeat("a", MaxFieldLen+1)
		if err := issue.ValidateForImport(nil); !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("ValidateForImport() error = %v, want errors.Is(ErrFieldTooLong)", err)
		}
	})
	t.Run("256-rune owner fails", func(t *testing.T) {
		issue := validIssue()
		issue.Owner = strings.Repeat("o", MaxFieldLen+1)
		if err := issue.ValidateForImport(nil); !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("ValidateForImport() error = %v, want errors.Is(ErrFieldTooLong)", err)
		}
	})
	t.Run("255-rune multibyte assignee passes", func(t *testing.T) {
		issue := validIssue()
		issue.Assignee = strings.Repeat("é", MaxFieldLen)
		if err := issue.ValidateForImport(nil); err != nil {
			t.Errorf("ValidateForImport() error = %v, want nil", err)
		}
	})
}

// TestCheckFieldLen unit-tests the helper directly, including the rune vs byte
// boundary and the wrapped, typed error it returns.
func TestCheckFieldLen(t *testing.T) {
	if err := CheckFieldLen("assignee", strings.Repeat("a", MaxFieldLen)); err != nil {
		t.Errorf("CheckFieldLen(255 runes) = %v, want nil", err)
	}
	if err := CheckFieldLen("assignee", strings.Repeat("é", MaxFieldLen)); err != nil {
		t.Errorf("CheckFieldLen(255 multibyte runes / %d bytes) = %v, want nil",
			len(strings.Repeat("é", MaxFieldLen)), err)
	}
	err := CheckFieldLen("assignee", strings.Repeat("é", MaxFieldLen+1))
	if !errors.Is(err, ErrFieldTooLong) {
		t.Errorf("CheckFieldLen(256 runes) = %v, want errors.Is(ErrFieldTooLong)", err)
	}
}
