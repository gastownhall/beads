package utils

import (
	"fmt"
	"strings"
)

// ExtractIssuePrefix extracts the prefix from an issue ID like "bd-123" -> "bd"
func ExtractIssuePrefix(issueID string) string {
	parts := strings.SplitN(issueID, "-", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// ExtractIssueNumber extracts the number from an issue ID like "bd-123" -> 123
func ExtractIssueNumber(issueID string) int {
	parts := strings.SplitN(issueID, "-", 2)
	if len(parts) < 2 {
		return 0
	}
	var num int
	fmt.Sscanf(parts[1], "%d", &num)
	return num
}
