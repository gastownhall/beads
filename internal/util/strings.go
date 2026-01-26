package util

import "strings"

// issueTypeAliases maps shorthand type names to canonical types
var issueTypeAliases = map[string]string{
	"mr":          "merge-request",
	"feat":        "feature",
	"mol":         "molecule",
	"enhancement": "feature",
}

// NormalizeIssueType expands type aliases to their canonical forms.
// For example: "mr" -> "merge-request", "feat" -> "feature", "mol" -> "molecule"
// Returns the input unchanged if it's not an alias.
func NormalizeIssueType(t string) string {
	if canonical, ok := issueTypeAliases[strings.ToLower(t)]; ok {
		return canonical
	}
	return t
}

// NormalizeLabels trims whitespace, removes empty strings, and deduplicates labels
// while preserving order.
func NormalizeLabels(ss []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
