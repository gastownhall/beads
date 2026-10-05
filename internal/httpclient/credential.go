// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/credential.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// CredentialProvider authorizes outbound requests to a bd serve.
//
// It is the wire package's interface, aliased here because design D5 places it
// on this package: any embedder-supplied auth scheme (token sources, mutual
// TLS helpers, gateway auto-detection) implements it against this name. One
// type, two spellings.
type CredentialProvider = wire.CredentialProvider

const (
	// TokenEnv carries the bearer token itself, the highest rung.
	// #nosec G101 -- the NAME of an environment variable, not a credential.
	TokenEnv = "BEADS_HTTP_TOKEN"
	// TokenCommandEnv names a helper that prints a token, either bare or in the
	// kubectl ExecCredential envelope {"token","expirationTimestamp"}.
	// #nosec G101 -- the NAME of an environment variable, not a credential.
	TokenCommandEnv = "BEADS_HTTP_TOKEN_COMMAND"
)

// BearerProvider is the default credential ladder for the http backend
// (design D5): BEADS_HTTP_TOKEN, then BEADS_HTTP_TOKEN_COMMAND, then the
// credentials file's [host:port] section, then no credential at all — which is
// the tip OSS server's loopback-trust posture and a legitimate answer, not a
// failure.
//
// The ladder fails closed. A rung the operator configured that then errors
// aborts the request; it never falls through to a lower rung, because the
// difference between a broken token command and an unauthenticated request is
// exactly what nobody notices.
//
// The resolved token is held in memory for the life of the provider and is
// never logged: the only place it appears is the Authorization header.
type BearerProvider struct {
	// endpoint is the server this provider authorizes against, rendered for the
	// posture warning only. host/port key the credentials-file rung.
	endpoint string
	host     string
	port     int
	// insecure records a bearer bound for a non-loopback host over plain http.
	insecure bool
	warnOnce sync.Once
	warnTo   io.Writer

	mu       sync.Mutex
	resolved bool
	token    string
	// source is the provenance slug of the rung that yielded token — the env var
	// name, the token-command env var, or credentialsFileSourceName — so a 401 can
	// name which credential the server refused (design D7). Never the token.
	source string
}

// NewBearerProvider builds the default provider for base.
func NewBearerProvider(base *url.URL) *BearerProvider {
	p := &BearerProvider{warnTo: os.Stderr}
	if base == nil {
		return p
	}
	p.endpoint = base.Redacted()
	p.host = base.Hostname()
	p.port = endpointPort(base)
	p.insecure = base.Scheme == "http" && !configfile.IsLocalHostString(p.host)
	return p
}

// endpointPort is the credentials-file section key's port half. The file is
// keyed [host:port] with a literal number, so a URL that leans on its scheme's
// default port needs that default spelled out or it could never have an entry.
func endpointPort(base *url.URL) int {
	if raw := base.Port(); raw != "" {
		if port, err := strconv.Atoi(raw); err == nil {
			return port
		}
		return 0
	}
	if base.Scheme == "https" {
		return 443
	}
	return 80
}

// Authorize sets the bearer header when a credential is configured. No
// credential configured is not an error: it is how this client talks to the
// unauthenticated loopback server upstream ships.
func (p *BearerProvider) Authorize(ctx context.Context, req *http.Request) error {
	token, err := p.current(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	p.warnInsecure()
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// Refresh re-walks the ladder after a 401 and reports whether the request is
// worth re-issuing.
//
// This is the client half of the server's rotation contract: the server re-reads
// its token file on a ~1s gate, so rotation is write {new,old}, roll the
// clients, drop old — and a client that rolled mid-flight 401s once. Re-walking
// the ladder is what "rolled" means on this side, since every rung is re-read.
//
// A credential that did not change is not retried. Sending the same token that
// was just refused would turn a clear 401 into a second one, and the one retry
// the wire client allows is spent for nothing.
func (p *BearerProvider) Refresh(ctx context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fresh, source, err := p.resolve(ctx)
	if err != nil {
		return false, err
	}
	changed := !p.resolved || fresh != p.token
	p.token, p.source, p.resolved = fresh, source, true
	// A ladder that now resolves to nothing has nothing to retry with either:
	// an unauthenticated retry against a server that just answered 401 is a
	// round trip whose answer is already known.
	return changed && fresh != "", nil
}

func (p *BearerProvider) current(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resolved {
		return p.token, nil
	}
	token, source, err := p.resolve(ctx)
	if err != nil {
		return "", err
	}
	p.token, p.source, p.resolved = token, source, true
	return p.token, nil
}

// Source names the ladder rung the current credential came from, in the terms an
// operator configured it (design D7): the env var, the token-command env var, or
// the credentials file keyed by [host:port]. It is what lets a refused-credential
// 401 say WHICH source to rotate or fix. It never returns the credential itself,
// and returns "" both before a token has been resolved and when the ladder
// yielded none — the no-auth posture, where there is no source to name.
func (p *BearerProvider) Source() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.source {
	case "":
		return ""
	case credentialsFileSourceName:
		return fmt.Sprintf("the credentials file [%s:%d]", p.host, p.port)
	default:
		// The env and token-command rungs carry their env var name as the slug,
		// which is exactly the source to name.
		return p.source
	}
}

// resolve walks the ladder. The caller holds mu, so a burst of concurrent
// requests runs the token command once rather than once each. It returns the
// resolved token and the provenance slug of the rung that yielded it (empty for
// both when the ladder is unconfigured).
func (p *BearerProvider) resolve(ctx context.Context) (token, source string, err error) {
	cred, ok, err := creds.ResolveLadder(ctx,
		envTokenSource{Var: TokenEnv},
		commandTokenSource{Var: TokenCommandEnv},
		credentialsFileTokenSource{Host: p.host, Port: p.port},
	)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", nil
	}
	return cred.Value, cred.Source, nil
}

// warnInsecure fires once per provider, and only when a token is actually about
// to travel. bd serve has no TLS of its own, so a bearer bound anywhere but
// loopback crosses the network in the clear; that is an operator decision to
// make knowingly, in front of a reverse proxy that terminates TLS.
func (p *BearerProvider) warnInsecure() {
	if !p.insecure {
		return
	}
	p.warnOnce.Do(func() {
		fmt.Fprintf(p.warnTo, "Warning: sending a bearer token to %s over plain http; the token crosses the network in the clear. Put bd serve behind TLS, or bind it to loopback.\n", p.endpoint)
	})
}

// envTokenSource is the token-in-the-environment rung. The value is read at
// resolution time rather than at construction, so Refresh sees a token the
// process was handed after this provider was built.
type envTokenSource struct{ Var string }

func (s envTokenSource) Name() string { return s.Var }

func (s envTokenSource) Resolve(context.Context) (creds.Credential, bool, error) {
	value := strings.TrimSpace(os.Getenv(s.Var))
	if value == "" {
		return creds.Credential{}, false, nil
	}
	return creds.Credential{Value: value, Kind: creds.KindIdentity, Source: s.Var}, true, nil
}

// commandTokenSource runs an operator-named helper and reads a token from its
// stdout — the credential-process idiom bd already uses for database passwords.
//
// The helper owns its own error text: internal/creds folds the helper's stderr
// into the failure, exactly as it does on the postgres ladder, so a helper must
// keep secrets off stderr the way `bd __gw-credential` does with its canned
// messages.
type commandTokenSource struct{ Var string }

func (s commandTokenSource) Name() string { return s.Var }

func (s commandTokenSource) Resolve(ctx context.Context) (creds.Credential, bool, error) {
	command := strings.TrimSpace(os.Getenv(s.Var))
	if command == "" {
		return creds.Credential{}, false, nil
	}
	return creds.CommandSource{Command: command, Kind: creds.KindIdentity, Label: s.Var}.Resolve(ctx)
}

// credentialsFileTokenSource reads the bearer from the shared credentials file's
// [host:port] section, under the same `password` key every other endpoint uses
// there. It is the lowest configured rung and, like the postgres ladder's copy,
// opportunistic: a missing file or a section without an entry is
// indistinguishable from "not configured here" and falls through rather than
// failing the walk. Fail-closed is for rungs an operator named explicitly.
type credentialsFileTokenSource struct {
	Host string
	Port int
}

// credentialsFileSourceName is the provenance slug for the file rung, shared by
// the source's Name and BearerProvider.Source so the two cannot drift.
const credentialsFileSourceName = "credentials-file"

func (s credentialsFileTokenSource) Name() string { return credentialsFileSourceName }

func (s credentialsFileTokenSource) Resolve(context.Context) (creds.Credential, bool, error) {
	if s.Host == "" || s.Port <= 0 {
		return creds.Credential{}, false, nil
	}
	// LookupCredentialsPassword warns on a group- or world-readable file, which
	// is the 0600 posture this rung inherits rather than restates.
	token := strings.TrimSpace(configfile.LookupCredentialsPassword(s.Host, s.Port))
	if token == "" {
		return creds.Credential{}, false, nil
	}
	return creds.Credential{Value: token, Kind: creds.KindIdentity, Source: s.Name()}, true, nil
}
