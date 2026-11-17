package workapi

import (
	"encoding/json"
	"fmt"
	"strings"
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
// — that the lenient ParseTypeList would read as a name matching no row: the
// write check refuses it, and the read also splits it on them.
const typeNameForbiddenRunes = " \t\r\n\"'`[],"

// isTypeNameForbiddenRune reports whether r is one of typeNameForbiddenRunes.
func isTypeNameForbiddenRune(r rune) bool {
	return strings.ContainsRune(typeNameForbiddenRunes, r)
}

// ValidateRetainedTypesValue is the write-time check of a types.retained value
// (issueops.SettingKeyTypesRetained). It checks SYNTAX ONLY: that the value is
// a list ParseTypeList reads the way the operator wrote it. Spelling is the
// reader's job — RetainedTypesFromSetting canonicalizes aliases and the case of
// built-in types, and splits a name on the characters this check refuses — so
// a write route that skips this check, or a server that predates it, can store
// a value that over-protects but never one that silently protects nothing or a
// different type.
//
// It refuses a value that opens with '[' but is not a JSON array of strings, a
// non-blank value that names no type (`null`, `,`, `[""]`), and a name
// containing whitespace, a quote, a bracket or a comma. A well-formed name bd
// does not know is allowed — it matches nothing — so an orchestrator can name a
// type before it registers it. An empty or blank value unsets the key and `[]`
// retains nothing; both are allowed.
func ValidateRetainedTypesValue(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var names []string
		if err := json.Unmarshal([]byte(trimmed), &names); err != nil {
			return fmt.Errorf("%q is not a JSON array of type names (write [\"op\",\"fact\"] or op,fact): %v", value, err)
		}
		if len(names) == 0 {
			return nil
		}
	}
	names := ParseTypeList(trimmed)
	if len(names) == 0 {
		return fmt.Errorf("%q names no type (write [\"op\",\"fact\"] or op,fact; [] retains nothing, and an empty value unsets the key)", value)
	}
	for _, name := range names {
		if strings.ContainsAny(name, typeNameForbiddenRunes) {
			return fmt.Errorf("type name %q contains whitespace, a quote, a bracket or a comma: name each type once, as [\"op\",\"fact\"] or op,fact", name)
		}
	}
	return nil
}
