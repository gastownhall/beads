// Package labelns implements exclusive label namespaces (bd-7u5ki): a
// workspace may declare label prefixes (e.g. "tier:") for which an issue
// carries at most one label. Labels are otherwise free-form strings, so
// without enforcement a second label in a routing namespace (tier:fable +
// tier:opus) silently narrows fleet eligibility to zero instead of widening
// it — ready-front listings match the label they query for while claim
// filters exclude on the other one, leaving the bead invisible to dispatch.
//
// Enforcement is opt-in: the ConfigKey value is a comma-separated prefix
// list, and an empty or unset value (the default) leaves every label
// namespace multi-valued, exactly the pre-feature behavior.
package labelns

import (
	"fmt"
	"strings"
)

// ConfigKey is the workspace config key holding the comma-separated list of
// exclusive label prefixes (e.g. "tier:,review:"). Empty or unset means no
// namespace is exclusive.
const ConfigKey = "labels.exclusive-prefixes"

// ParsePrefixes parses a ConfigKey value into normalized prefixes: entries
// are trimmed, empty entries dropped, duplicates removed, and every prefix is
// guaranteed to end with ":" so "tier" and "tier:" configure the same
// namespace. Malformed entries are skipped rather than rejected — read paths
// must stay permissive because the config value may predate stricter
// validation; ValidatePrefixes is the strict gate for writes.
func ParsePrefixes(raw string) []string {
	parts := strings.Split(raw, ",")
	prefixes := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == ":" {
			continue
		}
		if !strings.HasSuffix(part, ":") {
			part += ":"
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		prefixes = append(prefixes, part)
	}
	return prefixes
}

// ValidatePrefixes checks a candidate ConfigKey value, returning the parsed
// prefixes or an error describing the first invalid entry. Used by
// 'bd config set' so a bad value is rejected at write time.
func ValidatePrefixes(raw string) ([]string, error) {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := strings.TrimSuffix(part, ":")
		if name == "" {
			return nil, fmt.Errorf("prefix %q has no name before the colon", part)
		}
		if strings.Contains(name, ":") {
			return nil, fmt.Errorf("prefix %q may contain at most one colon, at the end", part)
		}
		if strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("prefix %q contains whitespace", part)
		}
		if name == "provides" {
			return nil, fmt.Errorf("'provides:' labels are reserved for cross-project capabilities and are inherently multi-valued")
		}
	}
	prefixes := ParsePrefixes(raw)
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("value contains no prefixes (unset the key to disable enforcement)")
	}
	return prefixes, nil
}

// Match returns the configured exclusive prefix the label falls under, or ""
// when the label is not in any exclusive namespace. With overlapping prefixes
// configured, the first match in config order wins.
func Match(prefixes []string, label string) string {
	for _, prefix := range prefixes {
		if strings.HasPrefix(label, prefix) {
			return prefix
		}
	}
	return ""
}

// Conflict records an exclusive-namespace violation: two or more distinct
// labels sharing one exclusive prefix.
type Conflict struct {
	Prefix string
	Labels []string
}

// Conflicts returns one Conflict per exclusive prefix that more than one
// distinct label falls under. Conflicts follow the config order of prefixes;
// each Conflict's labels keep their input order, deduplicated, so callers get
// deterministic error text.
func Conflicts(prefixes, labels []string) []Conflict {
	if len(prefixes) == 0 || len(labels) < 2 {
		return nil
	}
	byPrefix := make(map[string][]string)
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if _, dup := seen[label]; dup {
			continue
		}
		seen[label] = struct{}{}
		if prefix := Match(prefixes, label); prefix != "" {
			byPrefix[prefix] = append(byPrefix[prefix], label)
		}
	}
	var conflicts []Conflict
	for _, prefix := range prefixes {
		if len(byPrefix[prefix]) > 1 {
			conflicts = append(conflicts, Conflict{Prefix: prefix, Labels: byPrefix[prefix]})
		}
	}
	return conflicts
}

// ResolveCreateLabels settles exclusive-namespace collisions for a create
// that inherits labels from a parent. An explicit label wins over an
// inherited one in the same namespace: the inherited label is dropped, so a
// child can override e.g. a tier: routing label without needing
// --no-inherit-labels. It returns the inherited labels to keep, in their
// input order.
//
// Two collisions are refused instead: explicit labels that conflict among
// themselves, and inherited labels that conflict among themselves (a parent
// labeled before the namespace became exclusive, or imported with a kept
// violation). parentID only names the parent in the second error, so the
// caller learns which labels it never typed. After the drop no namespace
// holds both explicit and inherited labels, so a nil error means the merged
// label set is conflict-free.
func ResolveCreateLabels(prefixes, explicit, inherited []string, parentID string) ([]string, error) {
	if len(prefixes) == 0 {
		return inherited, nil
	}
	if conflicts := Conflicts(prefixes, explicit); len(conflicts) > 0 {
		c := conflicts[0]
		return nil, fmt.Errorf("namespace %q is exclusive (%s) and allows at most one label, got %s",
			c.Prefix, ConfigKey, strings.Join(c.Labels, ", "))
	}
	taken := make(map[string]bool)
	for _, label := range explicit {
		if prefix := Match(prefixes, label); prefix != "" {
			taken[prefix] = true
		}
	}
	kept := make([]string, 0, len(inherited))
	for _, label := range inherited {
		if prefix := Match(prefixes, label); prefix != "" && taken[prefix] {
			continue
		}
		kept = append(kept, label)
	}
	if conflicts := Conflicts(prefixes, kept); len(conflicts) > 0 {
		c := conflicts[0]
		return nil, fmt.Errorf("namespace %q is exclusive (%s) and allows at most one label, but parent %s carries %s — choose one with -l, or skip inheritance with --no-inherit-labels",
			c.Prefix, ConfigKey, parentID, strings.Join(c.Labels, ", "))
	}
	return kept, nil
}
