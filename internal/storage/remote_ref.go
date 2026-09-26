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
	return GitRefFromParams(params)
}

// GitRefFromParams extracts GitRefParam from an already-decoded dolt_remotes
// params object. It is the one reader of that key: GitRefFromParamsJSON uses
// it for the SQL params column and doltutil.PersistedRemotes for the same
// object in repo_state.json, so the two planes cannot drift.
//
// An absent key and a JSON null mean the default ref. A key that is present
// but is not a string is an error, for the same reason a params value that is
// not a JSON object is: a recorded ref bd cannot read could be a custom ref
// the mirror is pinned to, and reading it as the default would push the
// remote's data onto DefaultGitDataRef. Dolt only ever writes a string here,
// so this is the fail-closed edge of that invariant, not a live shape.
func GitRefFromParams(params map[string]any) (string, error) {
	value, ok := params[GitRefParam]
	if !ok || value == nil {
		return "", nil
	}
	ref, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("remote parameter %s is %T, not a string", GitRefParam, value)
	}
	return strings.TrimSpace(ref), nil
}
