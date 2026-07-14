// Package creds resolves the credential bd uses to open a protected database at
// command time. It is backend- and issuer-neutral: a Source yields a typed
// Credential — a secret that lands in the password slot of a direct connection, or
// an identity presented as the connection username to an authenticating gateway
// server — and ResolveLadder walks an ordered chain, first configured hit wins,
// failing closed when a configured source errors.
//
// The rungs are CommandSource (an external helper), EnvSource (a static env var),
// and FileSource (the credentials file, keyed by [host:port]). The exec engine
// (CommandSource) is the vendor-neutral credential-process idiom (kubectl
// ExecCredential / AWS credential_process / git credential helper). It was ported
// from the credential-command work on the feat/dolt-credential-command branch
// (dolt/credcmd.go there), generalized here so every backend shares one resolver.
package creds

import (
	"context"
	"fmt"
	"time"
)

// Kind declares what a resolved credential IS. It is decided by the config slot
// that produced the credential, never inferred from the value — so an identity
// token can never be mistaken for a password.
type Kind uint8

const (
	// KindSecret is a password: it lands in the password slot of the connection.
	KindSecret Kind = iota
	// KindIdentity is presented as the connection username; an authenticating gateway
	// server verifies it and connects to the database on the client's behalf.
	KindIdentity
)

// Credential is one resolved credential.
type Credential struct {
	Value    string    // the secret or identity token
	Username string    // optional username override (e.g. a Vault dynamic user/pass pair); empty leaves the DSN user
	Kind     Kind      // set by the source slot at construction
	Expiry   time.Time // zero means static (never expires)
	Source   string    // provenance slug for logs/doctor; never the secret itself
}

// Source is one rung of the resolution ladder.
type Source interface {
	// Name is the provenance slug (env var, file, or command label).
	Name() string
	// Resolve returns the credential when this source is configured. A
	// configured=false result means "not set here, try the next rung". A non-nil
	// error means "configured but failed" and aborts the walk — the ladder never
	// falls through to a lower-priority rung after an error.
	Resolve(ctx context.Context) (cred Credential, configured bool, err error)
}

// ResolveLadder walks sources in priority order and returns the first configured
// credential. It fails closed: any source error stops the walk and propagates, so a
// configured-but-broken helper can never silently downgrade to a lower rung. When no
// source is configured it returns configured=false with no error, letting the caller
// fall through to a driver-native default (PGPASSWORD, ~/.pgpass, and the like).
func ResolveLadder(ctx context.Context, sources ...Source) (Credential, bool, error) {
	for _, s := range sources {
		cred, configured, err := s.Resolve(ctx)
		if err != nil {
			return Credential{}, true, fmt.Errorf("credential source %s: %w", s.Name(), err)
		}
		if configured {
			if cred.Source == "" {
				cred.Source = s.Name()
			}
			return cred, true, nil
		}
	}
	return Credential{}, false, nil
}
