package workapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// SettingKeyWispProtectedLabels names the labels that make a wisp
// unreclaimable by `bd mol wisp gc`, in every mode of that command (age, --all,
// --closed) and on every route. A wisp carrying any one of them is never
// selected; `bd delete` still deletes one on purpose.
//
// The value is a list of label names, in the JSON-array or comma-separated
// form types.custom takes, matched exactly and case-sensitively. Unset, blank
// and `[]` protect nothing: core ships the extension point, and which records
// are irreplaceable is the workspace's policy. A stored value REPLACES the
// previous list; `wisp gc --exclude-label` adds to it for one run.
//
// The setting is read from the workspace database only — never from
// config.yaml or the environment — for the reason types.retained is: every
// client sweeping a shared workspace must apply the same rule. Other sweeps
// (bd purge, bd gc, bd admin cleanup) and a bd from before this setting
// ignore it.
const SettingKeyWispProtectedLabels = "wisp.protected_labels"

// ParseProtectedLabels parses a wisp.protected_labels value into its label
// names, trimmed and deduplicated in first-seen order. It is both the write
// check (ValidateSettingWrite) and the read: GC refuses to run on a value it
// cannot parse, because guessing at a malformed protection list under-protects.
//
// An empty or blank value and `[]` are nil. It refuses a value that opens
// with '[' but is not a JSON array of strings, a non-blank value that names no
// label (`,`, `[""]`), and a name containing whitespace — labels never carry
// it, so `a b` is a mis-spelled list. Other punctuation is part of a name: a
// JSON-encoded `"say:\"hi\""` is one label.
func ParseProtectedLabels(value string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	var names []string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &names); err != nil {
			return nil, fmt.Errorf("%q is not a JSON array of label names (write [\"keep:mail\",\"keep:alert\"] or keep:mail,keep:alert): %v", value, err)
		}
		if len(names) == 0 {
			return nil, nil
		}
	} else {
		names = strings.Split(trimmed, ",")
	}
	seen := make(map[string]bool, len(names))
	var labels []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if strings.IndexFunc(name, unicode.IsSpace) >= 0 {
			return nil, fmt.Errorf("label name %q contains whitespace: name each label once, as [\"keep:mail\",\"keep:alert\"] or keep:mail,keep:alert", name)
		}
		seen[name] = true
		labels = append(labels, name)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("%q names no label (write [\"keep:mail\"] or keep:mail; [] protects nothing, and an empty value unsets the key)", value)
	}
	return labels, nil
}
