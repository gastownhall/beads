package creds

import (
	"context"
	"os"
)

// EnvSource reads a static password from an environment variable. It is always a
// KindSecret — an env var carrying an identity would still land in the password
// slot, which is the correct behavior for the plain "I just have a password" case.
type EnvSource struct {
	Var string // env var name; also the provenance slug
}

// Name returns the env var name.
func (s EnvSource) Name() string { return s.Var }

// Resolve returns the password from the env var, or configured=false when it is
// unset or empty.
func (s EnvSource) Resolve(_ context.Context) (Credential, bool, error) {
	v := os.Getenv(s.Var)
	if v == "" {
		return Credential{}, false, nil
	}
	return Credential{Value: v, Kind: KindSecret, Source: s.Var}, true, nil
}
