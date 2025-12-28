package validation

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestParsePriority(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		// Numeric format
		{"0", 0},
		{"1", 1},
		{"2", 2},
		{"3", 3},
		{"4", 4},

		// P-prefix format (uppercase)
		{"P0", 0},
		{"P1", 1},
		{"P2", 2},
		{"P3", 3},
		{"P4", 4},

		// P-prefix format (lowercase)
		{"p0", 0},
		{"p1", 1},
		{"p2", 2},

		// With whitespace
		{" 1 ", 1},
		{" P1 ", 1},

		// Invalid cases (returns -1)
		{"5", -1},      // Out of range
		{"-1", -1},     // Negative
		{"P5", -1},     // Out of range with prefix
		{"abc", -1},    // Not a number
		{"P", -1},      // Just the prefix
		{"PP1", -1},    // Double prefix
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParsePriority(tt.input)
			if got != tt.expected {
				t.Errorf("ParsePriority(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestValidatePriority(t *testing.T) {
	tests := []struct {
		input     string
		wantValue int
		wantError bool
	}{
		{"0", 0, false},
		{"2", 2, false},
		{"P1", 1, false},
		{"5", -1, true},
		{"abc", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ValidatePriority(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidatePriority(%q) error = %v, wantError %v", tt.input, err, tt.wantError)
				return
			}
			if got != tt.wantValue {
				t.Errorf("ValidatePriority(%q) = %d, want %d", tt.input, got, tt.wantValue)
			}
		})
	}
}

func TestValidateIDFormat(t *testing.T) {
	tests := []struct {
		input      string
		wantPrefix string
		wantError  bool
	}{
		{"", "", false},
		{"bd-a3f8e9", "bd", false},
		{"bd-42", "bd", false},
		{"bd-a3f8e9.1", "bd", false},
		{"foo-bar", "foo", false},
		{"nohyphen", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ValidateIDFormat(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateIDFormat(%q) error = %v, wantError %v", tt.input, err, tt.wantError)
				return
			}
			if got != tt.wantPrefix {
				t.Errorf("ValidateIDFormat(%q) = %q, want %q", tt.input, got, tt.wantPrefix)
			}
		})
	}
}

func TestParseIssueType(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantType     types.IssueType
		wantError    bool
		errorContains string
	}{
		// Valid issue types
		{"bug type", "bug", types.TypeBug, false, ""},
		{"feature type", "feature", types.TypeFeature, false, ""},
		{"task type", "task", types.TypeTask, false, ""},
		{"epic type", "epic", types.TypeEpic, false, ""},
		{"chore type", "chore", types.TypeChore, false, ""},
		{"merge-request type", "merge-request", types.TypeMergeRequest, false, ""},
		{"molecule type", "molecule", types.TypeMolecule, false, ""},
		{"gate type", "gate", types.TypeGate, false, ""},
		{"agent type", "agent", types.TypeAgent, false, ""},
		{"role type", "role", types.TypeRole, false, ""},
		{"message type", "message", types.TypeMessage, false, ""},

		// Case sensitivity (function is case-sensitive)
		{"uppercase bug", "BUG", types.TypeTask, true, "invalid issue type"},
		{"mixed case feature", "FeAtUrE", types.TypeTask, true, "invalid issue type"},

		// With whitespace
		{"bug with spaces", "  bug  ", types.TypeBug, false, ""},
		{"feature with tabs", "\tfeature\t", types.TypeFeature, false, ""},

		// Invalid issue types
		{"invalid type", "invalid", types.TypeTask, true, "invalid issue type"},
		{"empty string", "", types.TypeTask, true, "invalid issue type"},
		{"whitespace only", "   ", types.TypeTask, true, "invalid issue type"},
		{"numeric type", "123", types.TypeTask, true, "invalid issue type"},
		{"special chars", "bug!", types.TypeTask, true, "invalid issue type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueType(tt.input)
			
			// Check error conditions
			if (err != nil) != tt.wantError {
				t.Errorf("ParseIssueType(%q) error = %v, wantError %v", tt.input, err, tt.wantError)
				return
			}
			
			if err != nil && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("ParseIssueType(%q) error message = %q, should contain %q", tt.input, err.Error(), tt.errorContains)
				}
				return
			}
			
			// Check return value
			if got != tt.wantType {
				t.Errorf("ParseIssueType(%q) = %v, want %v", tt.input, got, tt.wantType)
			}
		})
	}
}

func TestValidatePrefix(t *testing.T) {
	tests := []struct {
		name            string
		requestedPrefix string
		dbPrefix        string
		force           bool
		wantError       bool
	}{
		{"matching prefixes", "bd", "bd", false, false},
		{"empty db prefix", "bd", "", false, false},
		{"mismatched with force", "foo", "bd", true, false},
		{"mismatched without force", "foo", "bd", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrefix(tt.requestedPrefix, tt.dbPrefix, tt.force)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidatePrefix() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestValidateAgentID(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		wantError     bool
		errorContains string
	}{
		// Town-level agents (no rig)
		{"valid mayor", "gt-mayor", false, ""},
		{"valid deacon", "gt-deacon", false, ""},

		// Per-rig agents (witness, refinery)
		{"valid witness gastown", "gt-witness-gastown", false, ""},
		{"valid refinery beads", "gt-refinery-beads", false, ""},
		// Note: hyphenated rig names like "my-rig" are ambiguous and not supported
		// for witness/refinery. Use simple rig names.

		// Named agents (crew, polecat)
		{"valid polecat", "gt-polecat-gastown-nux", false, ""},
		{"valid crew", "gt-crew-beads-dave", false, ""},
		{"valid polecat with complex name", "gt-polecat-gastown-war-boy-1", false, ""},

		// Invalid: wrong prefix
		{"wrong prefix bd", "bd-mayor", true, "must start with 'gt-'"},
		{"wrong prefix empty", "mayor", true, "must start with 'gt-'"},

		// Invalid: empty
		{"empty id", "", true, "agent ID is required"},

		// Invalid: unknown role
		{"unknown role", "gt-admin-gastown", true, "invalid agent role"},
		{"unknown role foo", "gt-foo", true, "invalid agent role"},

		// Invalid: town-level with rig
		{"mayor with rig", "gt-mayor-gastown", true, "cannot have rig suffix"},
		{"deacon with rig", "gt-deacon-beads", true, "cannot have rig suffix"},

		// Invalid: per-rig without rig
		{"witness no rig", "gt-witness", true, "requires rig"},
		{"refinery no rig", "gt-refinery", true, "requires rig"},

		// Invalid: named agent without name
		{"polecat no name", "gt-polecat-gastown", true, "requires name"},
		{"crew no name", "gt-crew-beads", true, "requires name"},

		// Invalid: witness/refinery with extra parts
		{"witness with name", "gt-witness-gastown-extra", true, "takes only rig"},
		{"refinery with name", "gt-refinery-beads-extra", true, "takes only rig"},

		// Invalid: empty components
		{"empty rig", "gt-witness-", true, "rig name cannot be empty"},
		{"just gt", "gt-", true, "must include role type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentID(tt.id)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateAgentID(%q) error = %v, wantError %v", tt.id, err, tt.wantError)
				return
			}
			if err != nil && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("ValidateAgentID(%q) error = %q, should contain %q", tt.id, err.Error(), tt.errorContains)
				}
			}
		})
	}
}
