package utils

import (
	"fmt"
	"strings"
)

// ExtractIssuePrefix extracts the prefix from an issue ID like "bd-123" -> "bd"
func ExtractIssuePrefix(issueID string) string {
	idx := strings.LastIndex(issueID, "-")
	if idx <= 0 {
		return ""
	}
	return issueID[:idx]
}

// ExtractIssueNumber extracts the number from an issue ID like "bd-123" -> 123
func ExtractIssueNumber(issueID string) int {
	idx := strings.LastIndex(issueID, "-")
	if idx < 0 || idx == len(issueID)-1 {
		return 0
	}
	var num int
	fmt.Sscanf(issueID[idx+1:], "%d", &num)
	return num
}
