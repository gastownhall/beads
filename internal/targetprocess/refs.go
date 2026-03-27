package targetprocess

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/tracker"
)

var tpAPIRefPattern = regexp.MustCompile(`(?i)/api/v1/(?:assignables|userstories|bugs)/(\d+)$`)

func IsExternalRef(ref, baseURL string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	if strings.HasPrefix(ref, "targetprocess:") {
		_, err := strconv.Atoi(strings.TrimPrefix(ref, "targetprocess:"))
		return err == nil
	}
	if !tpAPIRefPattern.MatchString(strings.TrimRight(ref, "/")) {
		return false
	}
	if baseURL == "" {
		return true
	}
	return strings.HasPrefix(strings.TrimRight(ref, "/"), strings.TrimRight(baseURL, "/"))
}

func ExtractIdentifier(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "targetprocess:") {
		return strings.TrimPrefix(ref, "targetprocess:")
	}
	matches := tpAPIRefPattern.FindStringSubmatch(strings.TrimRight(ref, "/"))
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func BuildExternalRef(baseURL string, issue *tracker.TrackerIssue) string {
	identifier := ""
	if issue != nil {
		if issue.URL != "" {
			return issue.URL
		}
		identifier = issue.Identifier
		if identifier == "" {
			identifier = issue.ID
		}
	}

	if strings.TrimSpace(baseURL) == "" {
		return fmt.Sprintf("targetprocess:%s", identifier)
	}
	return strings.TrimRight(baseURL, "/") + "/api/v1/Assignables/" + identifier
}
