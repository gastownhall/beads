package creds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// CommandSource runs a configured command and reads a credential from its stdout —
// the external credential-process idiom (kubectl ExecCredential / AWS
// credential_process / git credential helper). bd knows nothing of the issuer: the
// operator points Command at whatever mints the credential — a token-issuing CLI for
// an identity, or `vault read ...` / `aws rds generate-db-auth-token ...` for a secret.
// Kind is fixed at construction from the config slot, so the resolved value's role is
// never ambiguous.
//
// The result is cached per-command until near expiry so repeated opens don't
// re-spawn the helper; the cache lives for the process and dies with it.
type CommandSource struct {
	Command string
	Kind    Kind
	Label   string // provenance slug (the config-slot name); defaults to "credential-command"
}

// Name returns the provenance slug.
func (s CommandSource) Name() string {
	if s.Label != "" {
		return s.Label
	}
	return "credential-command"
}

// Resolve runs the command (or a cached result) and returns the credential. An
// empty Command means "not configured"; a helper failure is a configured error and
// aborts the ladder.
func (s CommandSource) Resolve(ctx context.Context) (Credential, bool, error) {
	if s.Command == "" {
		return Credential{}, false, nil
	}
	tok, user, expiry, err := resolveCredentialToken(ctx, s.Command)
	if err != nil {
		return Credential{}, true, err
	}
	return Credential{Value: tok, Username: user, Kind: s.Kind, Expiry: expiry, Source: s.Name()}, true, nil
}

const (
	credCommandTimeout = 30 * time.Second // a helper that hangs must not wedge an open
	credDefaultTTL     = 60 * time.Second // cache window when the helper reports no expiry
	credExpirySkew     = 10 * time.Second // refresh this long before the reported expiry
)

// execCredential is the union of stdout envelopes accepted: the kubectl
// ExecCredential subset {token, expirationTimestamp}, the OAuth-style
// {access_token, expires_in} shape, and an optional username for dynamic user/password
// pairs (e.g. Vault). A helper may instead print a bare token — see parseCredential.
type execCredential struct {
	Token               string `json:"token"`        //nolint:gosec // G117: ExecCredential/OAuth envelope field name (wire format), not an embedded secret
	AccessToken         string `json:"access_token"` //nolint:gosec // G117: ExecCredential/OAuth envelope field name (wire format), not an embedded secret
	Username            string `json:"username"`
	ExpirationTimestamp string `json:"expirationTimestamp"`
	ExpiresIn           int64  `json:"expires_in"`
}

type cachedCred struct {
	token    string
	username string
	expires  time.Time
}

var (
	credCacheMu sync.Mutex
	credCache   = map[string]cachedCred{}

	// credRunner runs the helper; a package var so tests can stub it without a shell.
	credRunner = func(ctx context.Context, command string) ([]byte, error) {
		// POSIX shells parse the helper command; native Windows has no `sh`, so
		// dispatch through cmd.exe there so a bare Windows bd does not hard-fail
		// every *_PASSWORD_COMMAND / CREDENTIAL_COMMAND in the fail-closed ladder.
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd.exe", "/C", command)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", command)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("%w: %s", err, msg)
			}
			return nil, err
		}
		return stdout.Bytes(), nil
	}
)

// resolveCredentialToken returns the token (and any username/expiry) for the given
// helper command, using a process-level cache keyed by the command so repeated opens
// don't re-spawn the helper until the token is near expiry. It is concurrency-safe.
func resolveCredentialToken(ctx context.Context, command string) (token, username string, expiry time.Time, err error) {
	now := time.Now()

	credCacheMu.Lock()
	if c, ok := credCache[command]; ok && now.Before(c.expires.Add(-credExpirySkew)) {
		tok, user, exp := c.token, c.username, c.expires
		credCacheMu.Unlock()
		return tok, user, exp, nil
	}
	credCacheMu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, credCommandTimeout)
	defer cancel()
	raw, err := credRunner(runCtx, command)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("credential command failed: %w", err)
	}
	token, username, expiry, err = parseCredential(raw)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if expiry.IsZero() {
		expiry = now.Add(credDefaultTTL)
	}

	credCacheMu.Lock()
	credCache[command] = cachedCred{token: token, username: username, expires: expiry}
	credCacheMu.Unlock()
	return token, username, expiry, nil
}

// parseCredential extracts the token (and any username/expiry) from a helper's
// stdout. A JSON object is read as the ExecCredential/getToken envelope; otherwise
// the trimmed output is taken as a bare token. A bare value containing whitespace is
// rejected — it is almost always an error message, and using it as a credential
// would only fail confusingly downstream.
func parseCredential(raw []byte) (token, username string, expiry time.Time, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", "", time.Time{}, fmt.Errorf("credential command produced no output")
	}

	if trimmed[0] == '{' {
		var c execCredential
		if jerr := json.Unmarshal(trimmed, &c); jerr != nil {
			return "", "", time.Time{}, fmt.Errorf("credential command returned unparseable JSON: %w", jerr)
		}
		token = c.Token
		if token == "" {
			token = c.AccessToken
		}
		if token == "" {
			return "", "", time.Time{}, fmt.Errorf("credential command JSON has no token/access_token field")
		}
		switch {
		case c.ExpirationTimestamp != "":
			if t, perr := time.Parse(time.RFC3339, c.ExpirationTimestamp); perr == nil {
				expiry = t
			}
		case c.ExpiresIn > 0:
			expiry = time.Now().Add(time.Duration(c.ExpiresIn) * time.Second)
		}
		return token, c.Username, expiry, nil
	}

	bare := string(trimmed)
	if strings.ContainsAny(bare, " \t\r\n") {
		return "", "", time.Time{}, fmt.Errorf("credential command output is not a bare token (contains whitespace); expected a token or a JSON {token,expirationTimestamp} envelope")
	}
	return bare, "", time.Time{}, nil
}
