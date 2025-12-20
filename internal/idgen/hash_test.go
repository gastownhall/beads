package idgen

import (
	"testing"
	"time"
)

func TestGenerateHashIDMatchesJiraVector(t *testing.T) {
	timestamp := time.Date(2024, 1, 2, 3, 4, 5, 6*1_000_000, time.UTC)
	prefix := "bd"
	title := "Fix login"
	description := "Details"
	creator := "jira-import"

	tests := map[int]string{
		3: "bd-ryl",
		4: "bd-itxc",
		5: "bd-9wt4w",
		6: "bd-39wt4w",
		7: "bd-rahb6w2",
		8: "bd-7rahb6w2",
	}

	for length, expected := range tests {
		got := GenerateHashID(prefix, title, description, creator, timestamp, length, 0)
		if got != expected {
			t.Fatalf("length %d: got %s, want %s", length, got, expected)
		}
	}
}
