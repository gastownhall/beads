package notion

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var notionPageIDPattern = regexp.MustCompile("(?i)[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")

// IsNotionExternalRef reports whether ref can be resolved to a Notion page.
func IsNotionExternalRef(ref string) bool {
	_, ok := CanonicalizeNotionExternalRef(ref)
	return ok
}

// CanonicalizeNotionExternalRef normalizes URLs to a stable Notion page URL and page ids to notion:<page_id>.
func CanonicalizeNotionExternalRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}

	if strings.HasPrefix(strings.ToLower(ref), "notion:") {
		normalized, ok := normalizeNotionPageID(strings.TrimSpace(ref[len("notion:"):]))
		if !ok {
			return "", false
		}
		return "notion:" + normalized, true
	}

	if normalized, ok := normalizeNotionPageID(ref); ok {
		return "notion:" + normalized, true
	}

	parsed, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Host)
	if host != "www.notion.so" && host != "notion.so" {
		return "", false
	}

	pageID := extractNotionPageID(parsed.Path)
	if pageID == "" {
		return "", false
	}
	return fmt.Sprintf("https://www.notion.so/%s", compactNotionPageID(pageID)), true
}

// CanonicalizeNotionPageURL normalizes any supported Notion page reference to a canonical page URL.
func CanonicalizeNotionPageURL(ref string) (string, bool) {
	pageID := ExtractNotionIdentifier(ref)
	if pageID == "" {
		return "", false
	}
	return fmt.Sprintf("https://www.notion.so/%s", compactNotionPageID(pageID)), true
}

// ExtractNotionIdentifier returns the normalized hyphenated page id.
func ExtractNotionIdentifier(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(strings.ToLower(ref), "notion:") {
		normalized, ok := normalizeNotionPageID(strings.TrimSpace(ref[len("notion:"):]))
		if ok {
			return normalized
		}
		return ""
	}

	if normalized, ok := normalizeNotionPageID(ref); ok {
		return normalized
	}

	parsed, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return extractNotionPageID(parsed.Path)
}

// BuildNotionExternalRef chooses the canonical external ref for a pulled issue.
func BuildNotionExternalRef(issue *PulledIssue) string {
	if issue == nil {
		return ""
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.ExternalRef); ok {
		return canonical
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.NotionPageID); ok {
		return canonical
	}
	if canonical, ok := CanonicalizeNotionExternalRef(issue.ExternalRef); ok {
		return canonical
	}
	return ""
}

func extractNotionPageID(path string) string {
	match := notionPageIDPattern.FindString(path)
	if match == "" {
		return ""
	}
	normalized, ok := normalizeNotionPageID(match)
	if !ok {
		return ""
	}
	return normalized
}

func normalizeNotionPageID(value string) (string, bool) {
	cleaned := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	if len(cleaned) != 32 {
		return "", false
	}
	for _, r := range cleaned {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", false
		}
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		cleaned[0:8],
		cleaned[8:12],
		cleaned[12:16],
		cleaned[16:20],
		cleaned[20:32],
	), true
}

func compactNotionPageID(pageID string) string {
	return strings.ReplaceAll(pageID, "-", "")
}
