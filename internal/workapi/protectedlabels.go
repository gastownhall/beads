package workapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SettingKeyWispProtectedLabels names the labels `bd mol wisp gc` must never
// reclaim, however stale the wisp and in whichever mode the sweep runs. Which
// records are irreplaceable is orchestration policy, so the workspace states
// it explicitly: there is no built-in label, and an empty value or [] means
// no protection is configured.
//
// Like types.retained this is workspace policy in the database, read through
// the settings plane so every client sweeping one workspace shares one rule —
// config.yaml and the environment are deliberately not consulted, and a
// stored value REPLACES the prior list rather than extending it. The
// --exclude-label flag adds labels to the configured set for one invocation
// only.
const SettingKeyWispProtectedLabels = "wisp.protected_labels"

// ParseProtectedLabelsValue parses a wisp.protected_labels value, accepting
// the JSON array form `bd config set` writes (["keep:mail"]) and the
// comma-separated form (keep:mail). Names are trimmed and deduplicated and
// then matched exactly and case-sensitively; other punctuation is kept
// verbatim, so unlike a type name a label may carry quotes or brackets when
// it is properly encoded in JSON (["a,b"]) — only what cannot be read back
// unambiguously is refused: internal whitespace, a name-encoding mistake the
// lenient reader would turn into a label matching no row.
//
// It is strict on purpose, and it is the one parser of this value: the same
// function decides what `bd config set` may store (through
// ValidateSettingWrite) and what a GC run reads, so a value the sweep cannot
// read as written is refused at the door rather than stored and then
// silently protecting nothing. An empty or blank value and [] parse to no
// labels; a nonblank value that names no label, a malformed JSON array, a
// non-string element, or a name containing whitespace is an error.
func ParseProtectedLabelsValue(value string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	var names []string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &names); err != nil {
			return nil, fmt.Errorf("%q is not a JSON array of label names (write [\"keep:mail\"] or keep:mail): %v", value, err)
		}
		if len(names) == 0 {
			return nil, nil
		}
	} else {
		names = strings.Split(trimmed, ",")
	}
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.ContainsAny(name, " \t\r\n") {
			return nil, fmt.Errorf("label name %q contains whitespace: name each label once, as [\"keep:mail\"] or keep:mail (use JSON for a name that itself contains a comma)", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%q names no label (write [\"keep:mail\"] or keep:mail; [] protects nothing, and an empty value unsets the key)", value)
	}
	return out, nil
}
