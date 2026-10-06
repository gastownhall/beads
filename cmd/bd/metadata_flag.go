package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// readMetadataFlag resolves a --metadata flag value (inline JSON, or
// @file.json to read it from a file) and requires it to be a JSON object.
//
// create and update share it so both commands, proxied or not, agree on the
// shape (GH#6035). `{}` is accepted; a bare string, number, boolean, array,
// or null is refused here with a clear message instead of being stored as-is
// (create) or failing late in the merge (update).
//
// That is close to, but not the same as, what the storage merge enforced for
// update. storage.MergeMetadataJSON unmarshals the incoming document into a
// map, which rejects a string, number, boolean or array with an internal
// error -- but JSON null unmarshals into a map with no error at all, so
// `bd update --metadata null` used to exit 0 having merged nothing. Refusing
// null up front is therefore a deliberate (small) behavior change on update,
// not a restatement of the merge's rule.
//
// Values inside the object are opaque, so a string value that happens to look
// like JSON is kept verbatim.
func readMetadataFlag(value string) (json.RawMessage, error) {
	metadataJSON := value
	if strings.HasPrefix(value, "@") {
		filePath := value[1:]
		// #nosec G304 -- user explicitly provides file path via @file.json syntax
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read metadata file %s: %w", filePath, err)
		}
		metadataJSON = string(data)
	}
	if !json.Valid([]byte(metadataJSON)) {
		return nil, fmt.Errorf("invalid JSON in --metadata: must be valid JSON")
	}
	var object map[string]json.RawMessage
	// The nil check is the arm that catches `null`: unmarshalling the JSON
	// literal null into a map zeroes the map and reports no error, so testing
	// err alone would let null through. Same idiom as the metadata checks in
	// internal/httpapi/batch_apply.go.
	if err := json.Unmarshal([]byte(metadataJSON), &object); err != nil || object == nil {
		return nil, fmt.Errorf(`invalid --metadata: must be a JSON object such as {"key":"value"}, got %s`, jsonKind(metadataJSON))
	}
	return json.RawMessage(metadataJSON), nil
}

// jsonKind names the top-level kind of a valid JSON document for error text.
//
// The "empty input" arm is defensive, not a reachable case for the caller
// above: json.Valid is false for both "" and whitespace-only input, so a
// document that has already passed it can never trim to empty. It keeps the
// byte index below safe for any future caller that has not validated first.
func jsonKind(doc string) string {
	trimmed := strings.TrimSpace(doc)
	if trimmed == "" {
		return "empty input"
	}
	switch trimmed[0] {
	case '"':
		return "a JSON string"
	case '[':
		return "a JSON array"
	case 't', 'f':
		return "a JSON boolean"
	case 'n':
		return "JSON null"
	case '{':
		return "a JSON object"
	default:
		return "a JSON number"
	}
}
