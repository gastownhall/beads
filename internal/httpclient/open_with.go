// Written fresh for OSS beads S6 (no bd-enterprise source copied). A sibling
// enterprise implementation takes over credential selection with its own
// Options.Credentials hook; OSS instead threads the backends.OpenOptions.Credential
// seam S1 defined, which backend_credential.go's ResolveCredential (S2) was
// built to resolve but which, until this file, had no production caller.
// OpenWith is that caller.
package httpclient

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// OpenWith is the backends.Backend.OpenWith hook backend/http (bdhttp)
// installs: the per-open seam that honors opts.Credential, opts.HTTPClient and
// opts.UserAgent for exactly THIS open, rather than the process-wide dialer
// Open/OpenReadOnly (NewFromConfig/NewReadOnlyFromConfig) use via the
// package-level registered dialer.
//
// That distinction is the whole reason this is a separate code path: the
// registered dialer is installed once, at process start, from one fixed
// DialOptions — exactly what a single-tenant CLI process wants, but useless
// to a multi-tenant embedder (gc, Gas City) that needs a different credential
// per workspace it opens in the same process. OpenWith dials fresh every call.
//
// base supplies the Register-time defaults (bdhttp.Options.UserAgent /
// HTTPClient) that a non-empty opts.UserAgent or non-nil opts.HTTPClient
// overrides for this one open.
//
// require is bdhttp.Options.RequireCredential: when true, a nil opts.Credential
// is ErrCredentialRequired rather than a silent fall-through to the ambient
// bearer ladder (BEADS_HTTP_TOKEN, BEADS_HTTP_TOKEN_COMMAND, the credentials
// file) — process-global ambient state a multi-tenant embedder cannot trust to
// name the right tenant. See ResolveCredential.
//
// Unlike Open/OpenReadOnly, OpenWith never consults BEADS_HTTP_CA_FILE: CA
// resolution is pinned to target.CAFile alone via DialOptionsForTarget,
// because an embedder juggling several targets in one process must not have
// one silently renarrowed (or widened) by an ambient value an unrelated open
// exported. See DialOptionsForTarget's own doc for why that rung is skipped
// here specifically.
func OpenWith(ctx context.Context, beadsDir string, opts backends.OpenOptions, base DialOptions, require bool) (storage.DoltStorage, error) {
	target, err := LoadTarget(beadsDir)
	if err != nil {
		return nil, err
	}
	creds, err := ResolveCredential(opts, target.BaseURL, require)
	if err != nil {
		return nil, err
	}
	dialOpts := base
	if opts.UserAgent != "" {
		dialOpts.UserAgent = opts.UserAgent
	}
	if opts.HTTPClient != nil {
		dialOpts.HTTPClient = opts.HTTPClient
	}
	dialOpts = DialOptionsForTarget(target, dialOpts)
	conn, err := DialWith(target, creds, dialOpts)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", target, err)
	}
	s := New(target, conn, nil)
	s.local = newLocalMetadata(beadsDir)
	return s, nil
}
