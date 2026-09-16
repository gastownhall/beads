package storage

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A git-backed Dolt remote keeps its data on one git ref of the remote
// repository. Dolt records the ref as the remote parameter GitRefParam and
// falls back to DefaultGitDataRef when the parameter is absent.
const (
	GitRefParam       = "git_ref"
	DefaultGitDataRef = "refs/dolt/data"
)

// EffectiveGitDataRef returns ref, or DefaultGitDataRef when ref is empty.
func EffectiveGitDataRef(ref string) string {
	if ref = strings.TrimSpace(ref); ref != "" {
		return ref
	}
	return DefaultGitDataRef
}

// RemoteRefsMatch reports whether two git data refs name the same ref. An
// empty ref stands for DefaultGitDataRef, so an unset ref and an explicit
// default compare equal.
func RemoteRefsMatch(a, b string) bool {
	return EffectiveGitDataRef(a) == EffectiveGitDataRef(b)
}

// GitRefFromParamsJSON extracts GitRefParam from a dolt_remotes params value.
// "", "null", an empty object, and an object without the key mean the
// default ref; anything that is not a JSON object is an error rather than a
// silent default.
func GitRefFromParamsJSON(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return "", fmt.Errorf("parse dolt_remotes params %q: %w", raw, err)
	}
	ref, _ := params[GitRefParam].(string)
	return strings.TrimSpace(ref), nil
}
