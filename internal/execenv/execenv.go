// Package execenv provides helpers for constructing explicit subprocess
// environments with the same key identity rules as os/exec.
package execenv

import (
	"runtime"
	"strings"
)

// A single host seam lets package tests exercise the exported wrappers for
// both key policies without changing the process environment.
// Process-wide seam: tests that swap it must not run in parallel.
var hostOS = runtime.GOOS

// KeyIdentityForOS returns the key identity used by os/exec on goos. Callers
// doing repeated membership checks can normalize once; POSIX keys stay exact.
func KeyIdentityForOS(key, goos string) string {
	return keyIdentityForWindows(key, goos == "windows")
}

// KeyEqual reports whether left and right identify the same environment key
// for a subprocess on the current host. Windows keys are case-insensitive;
// keys on other hosts are exact.
func KeyEqual(left, right string) bool {
	return KeyEqualForOS(left, right, hostOS)
}

// KeyEqualForOS compares environment keys using goos subprocess semantics.
func KeyEqualForOS(left, right, goos string) bool {
	return keyEqualForWindows(left, right, goos == "windows")
}

// KeyHasPrefixForOS compares a key, rather than an environment entry, with a
// prefix using goos subprocess semantics. Valueless-entry policy belongs to
// the caller.
func KeyHasPrefixForOS(key, prefix, goos string) bool {
	windows := goos == "windows"
	return strings.HasPrefix(keyIdentityForWindows(key, windows), keyIdentityForWindows(prefix, windows))
}

func keyEqualForWindows(left, right string, windows bool) bool {
	return keyIdentityForWindows(left, windows) == keyIdentityForWindows(right, windows)
}

// ContainsKeyWithPrefix reports whether env contains a key beginning with one
// of prefixes, using the current host's environment-key semantics.
func ContainsKeyWithPrefix(env []string, prefixes ...string) bool {
	return containsKeyWithPrefixForWindows(env, hostOS == "windows", prefixes...)
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
	return lookupForWindows(env, key, hostOS == "windows")
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
	return WithoutForOS(env, hostOS, keys...)
}

// WithoutForOS is Without with explicit goos environment-key semantics.
func WithoutForOS(env []string, goos string, keys ...string) []string {
	return withoutForWindows(env, goos == "windows", keys...)
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

// EntryKey returns an entry's key using the same split rule as os/exec,
// including drive pseudo-variables. A bare entry is returned unchanged so
// callers retain authority over their valueless-entry policy.
func EntryKey(entry string) string {
	if key, _, ok := split(entry); ok {
		return key
	}
	return entry
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
