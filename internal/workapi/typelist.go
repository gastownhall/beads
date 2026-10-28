package workapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// ParseTypeList parses a list-valued type setting — types.custom and
// types.retained — accepting both the JSON-array form `bd config set` and
// pour write (["duty","ops"]) and the legacy comma-separated form (duty,ops).
// Elements are trimmed and empties dropped; an empty or blank value is nil.
//
// It is the one parser of that form: issueops.ParseTypesConfigValue delegates
// here, so a reader of types.custom and the sweep's reader of types.retained
// cannot drift apart. It is lenient by design, because it reads what is
// already stored; what may be stored for types.retained is decided at write
// time by ValidateSettingWrite.
func ParseTypeList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(value), &names); err == nil {
		var out []string
		for _, name := range names {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	return ParseCommaSeparatedList(value)
}

// ParseCommaSeparatedList splits a comma-separated string into its trimmed,
// non-empty entries. An empty value is nil; a value with no non-empty entry is
// an empty, non-nil slice.
func ParseCommaSeparatedList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// typeNameForbiddenRunes are the characters no issue type name carries. A
// stored name holding one is a mis-spelled list — `[op]`, `"op"`, `op fact`
// — that the lenient ParseTypeList would read as a name matching no row.
const typeNameForbiddenRunes = " \t\r\n\"'`[],"

// ValidateRetainedTypesValue is the write-time check of a types.retained value
// (issueops.SettingKeyTypesRetained). It FAILS CLOSED, because the setting is
// a safety rule: the sweep reads what is stored through the lenient
// ParseTypeList, and a value it parses into names no row carries would hold
// back nothing while the operator believes the type is protected.
//
// It refuses a value that opens with '[' but is not a JSON array of strings, a
// name containing whitespace, a quote, a bracket or a comma, and an alias of a
// built-in type (rows store the canonical type, so `feat` would never match a
// `feature` row). A well-formed name bd does not know is allowed — it matches
// nothing — so an orchestrator can name a type before it registers it. An
// empty value unsets the key and is allowed.
func ValidateRetainedTypesValue(value string) error {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "[") {
		var names []string
		if err := json.Unmarshal([]byte(trimmed), &names); err != nil {
			return fmt.Errorf("%q is not a JSON array of type names (write [\"op\",\"fact\"] or op,fact): %v", value, err)
		}
	}
	for _, name := range ParseTypeList(trimmed) {
		if strings.ContainsAny(name, typeNameForbiddenRunes) {
			return fmt.Errorf("type name %q contains whitespace, a quote, a bracket or a comma: name each type once, as [\"op\",\"fact\"] or op,fact", name)
		}
		if canonical := types.IssueType(name).Normalize(); canonical != types.IssueType(name) {
			return fmt.Errorf("%q is an alias of %q, and rows store the canonical type; use %q", name, canonical, canonical)
		}
	}
	return nil
}
