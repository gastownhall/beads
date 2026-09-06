// Package execenv provides helpers for constructing explicit subprocess
// environments with the same key identity rules as os/exec.
package execenv

import (
	"runtime"
	"strings"
)

// KeyEqual reports whether left and right identify the same environment key
// for a subprocess on the current host. Windows keys are case-insensitive;
// keys on other hosts are exact.
func KeyEqual(left, right string) bool {
	return keyEqualForWindows(left, right, runtime.GOOS == "windows")
}

func keyEqualForWindows(left, right string, windows bool) bool {
	return keyIdentityForWindows(left, windows) == keyIdentityForWindows(right, windows)
}

// ContainsKeyWithPrefix reports whether env contains a key beginning with one
// of prefixes, using the current host's environment-key semantics.
func ContainsKeyWithPrefix(env []string, prefixes ...string) bool {
	return containsKeyWithPrefixForWindows(env, runtime.GOOS == "windows", prefixes...)
}

func containsKeyWithPrefixForWindows(env []string, windows bool, prefixes ...string) bool {
	identities := make([]string, len(prefixes))
	for i, prefix := range prefixes {
		identities[i] = keyIdentityForWindows(prefix, windows)
	}
	for _, entry := range env {
		key, _, ok := split(entry)
		if !ok {
			continue
		}
		key = keyIdentityForWindows(key, windows)
		for _, prefix := range identities {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
	}
	return false
}

// Lookup returns the last value for key in env, matching os/exec's last-wins
// handling of duplicate effective keys.
func Lookup(env []string, key string) (string, bool) {
	return lookupForWindows(env, key, runtime.GOOS == "windows")
}

func lookupForWindows(env []string, key string, windows bool) (string, bool) {
	wanted := keyIdentityForWindows(key, windows)
	var value string
	var found bool
	for _, entry := range env {
		entryKey, entryValue, ok := split(entry)
		if ok && keyIdentityForWindows(entryKey, windows) == wanted {
			value, found = entryValue, true
		}
	}
	return value, found
}

// Without returns a copy of env without entries matching keys. Unrelated
// duplicates, malformed entries, and Windows drive pseudo-variables are
// preserved in their original order. The input slice is not modified.
func Without(env []string, keys ...string) []string {
	return withoutForWindows(env, runtime.GOOS == "windows", keys...)
}

func withoutForWindows(env []string, windows bool, keys ...string) []string {
	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[keyIdentityForWindows(key, windows)] = struct{}{}
	}

	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := split(entry)
		if ok {
			if _, remove := drop[keyIdentityForWindows(key, windows)]; remove {
				continue
			}
		}
		out = append(out, entry)
	}
	return out
}

// keyIdentityForWindows mirrors the key normalization in os/exec.dedupEnvCase.
// strings.ToLower is intentional: EqualFold would collapse Unicode
// near-collisions such as s and ſ that os/exec keeps distinct.
func keyIdentityForWindows(key string, windows bool) string {
	if windows {
		return strings.ToLower(key)
	}
	return key
}

// split mirrors os/exec's handling of Windows drive pseudo-variables such as
// =C:=C:\work, whose key includes the leading equals sign.
func split(entry string) (key, value string, ok bool) {
	separator := strings.IndexByte(entry, '=')
	if separator == 0 {
		next := strings.IndexByte(entry[1:], '=')
		if next >= 0 {
			separator = next + 1
		}
	}
	if separator < 0 {
		return "", "", false
	}
	return entry[:separator], entry[separator+1:], true
}
