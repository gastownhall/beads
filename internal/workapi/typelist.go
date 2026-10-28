package workapi

import (
	"encoding/json"
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
