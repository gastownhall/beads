package doctor

import (
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int
	}{
		{"equal versions", "1.0.0", "1.0.0", 0},
		{"v1 less than v2 major", "1.0.0", "2.0.0", -1},
		{"v1 greater than v2 major", "2.0.0", "1.0.0", 1},
		{"v1 less than v2 minor", "1.1.0", "1.2.0", -1},
		{"v1 greater than v2 minor", "1.2.0", "1.1.0", 1},
		{"v1 less than v2 patch", "1.0.1", "1.0.2", -1},
		{"v1 greater than v2 patch", "1.0.2", "1.0.1", 1},
		{"different length v1 shorter", "1.0", "1.0.0", 0},
		{"different length v1 longer", "1.0.0", "1.0", 0},
		{"v1 shorter but greater", "1.1", "1.0.5", 1},
		{"v1 shorter but less", "1.0", "1.0.5", -1},
		{"real version comparison", "0.29.0", "0.30.0", -1},
		{"real version comparison 2", "0.30.1", "0.30.0", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CompareVersions(tt.v1, tt.v2)
			if result != tt.expected {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, result, tt.expected)
			}
		})
	}
}

func TestIsValidSemver(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected bool
	}{
		{"valid 3 part", "1.2.3", true},
		{"valid 2 part", "1.2", true},
		{"valid 1 part", "1", true},
		{"valid with zeros", "0.0.0", true},
		{"valid large numbers", "100.200.300", true},
		{"empty string", "", false},
		{"invalid letters", "1.2.a", false},
		{"invalid format", "v1.2.3", false},
		{"trailing dot", "1.2.", false},
		{"leading dot", ".1.2", false},
		{"double dots", "1..2", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidSemver(tt.version)
			if result != tt.expected {
				t.Errorf("IsValidSemver(%q) = %v, want %v", tt.version, result, tt.expected)
			}
		})
	}
}

func TestUpgradeCommandForPath(t *testing.T) {
	tests := []struct {
		name     string
		execPath string
		expected string
	}{
		{"homebrew apple silicon", "/opt/homebrew/Cellar/beads/0.49.4/bin/bd", "brew upgrade beads"},
		{"homebrew intel mac", "/usr/local/Cellar/beads/0.49.4/bin/bd", "brew upgrade beads"},
		{"homebrew linux", "/home/linuxbrew/.linuxbrew/Cellar/beads/0.49.4/bin/bd", "brew upgrade beads"},
		{"legacy tap formula", "/opt/homebrew/Cellar/bd/0.49.0/bin/bd", "brew upgrade beads"},
		{"usr local bin symlink", "/usr/local/bin/bd", installScriptCommand},
		{"go install", "/home/user/go/bin/bd", installScriptCommand},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := upgradeCommandForPath(tt.execPath)
			if result != tt.expected {
				t.Errorf("upgradeCommandForPath(%q)\n  got:  %q\n  want: %q", tt.execPath, result, tt.expected)
			}
		})
	}
}

func TestParseVersionParts(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected []int
	}{
		{"3 parts", "1.2.3", []int{1, 2, 3}},
		{"2 parts", "1.2", []int{1, 2}},
		{"1 part", "5", []int{5}},
		{"large numbers", "100.200.300", []int{100, 200, 300}},
		{"zeros", "0.0.0", []int{0, 0, 0}},
		{"invalid stops at letter", "1.2.a", []int{1, 2}},
		{"empty returns empty", "", []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseVersionParts(tt.version)
			if len(result) != len(tt.expected) {
				t.Errorf("ParseVersionParts(%q) length = %d, want %d", tt.version, len(result), len(tt.expected))
				return
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("ParseVersionParts(%q)[%d] = %d, want %d", tt.version, i, result[i], tt.expected[i])
				}
			}
		})
	}
}
