package setup

import (
	"encoding/json"
	"strings"
)

// parseHooksJSON unmarshals an agent hooks.json body into a generic map. Empty
// or whitespace-only input yields an empty (non-nil) map with no error, matching
// how both the Codex and Cursor installers treat a missing/blank file. File I/O
// (and absent-file handling) stays with the caller so each agent keeps its own
// read mechanism.
func parseHooksJSON(data []byte) (map[string]interface{}, error) {
	config := map[string]interface{}{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, err
		}
	}
	return config, nil
}

// marshalHooksJSON renders an agent hooks.json config with two-space indent and
// a trailing newline (the on-disk format shared by the Codex and Cursor
// installers).
func marshalHooksJSON(config map[string]interface{}) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
