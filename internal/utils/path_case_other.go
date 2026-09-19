//go:build !darwin

package utils

import "errors"

// errNoFastCanonicalCase reports that no kernel-assisted true-case query is
// available on this platform. resolveCanonicalCase only consults the fast path
// on darwin, so this exists purely to keep the package compiling elsewhere.
var errNoFastCanonicalCase = errors.New("canonical-case fast path unsupported on this platform")

func canonicalCaseFast(string) (string, error) {
	return "", errNoFastCanonicalCase
}
