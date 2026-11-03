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

// CommandEnvKeys names environment settings whose values bd executes.
var CommandEnvKeys = []string{
	"BEADS_DOLT_CREDENTIAL_COMMAND",
	"BEADS_HTTP_TOKEN_COMMAND",
	"BEADS_MAIL_DELEGATE",
	"BD_MAIL_DELEGATE",
	"EDITOR",
	"VISUAL",
	"BD_PAGER",
	"PAGER",
	"BEADS_DOLT_BIN",
}

// CredentialRedirectEnvKeys names environment settings that redirect where a
// user's credential command or file is sent or read from, or that redirect the
// trust/config bd loads a server and its credentials from.
var CredentialRedirectEnvKeys = []string{
	"BEADS_DOLT_SERVER_HOST",
	"BEADS_CREDENTIALS_FILE",
	// BEADS_CENTRAL_CONFIG names a JSON file whose server fields (host, mode,
	// user, TLS) bd merges as connection defaults; a .beads/.env that pointed
	// it at a committed file could silently send the user's Dolt credentials
	// to an attacker-chosen host.
	"BEADS_CENTRAL_CONFIG",
	// BEADS_HTTP_CA_FILE names the CA bundle the http backend trusts; a
	// .beads/.env that pointed it at a committed cert would let a
	// network-positioned attacker present a MITM certificate bd accepts.
	"BEADS_HTTP_CA_FILE",
}

// AllowedDoltEnvKeys names the only bare DOLT_* variables a project
// .beads/.env may import: the Dolt remote credential pair, the documented
// per-project credential use case (GH#2520). The DOLT_* namespace as a whole
// is owned by the dolt binary bd shells out to, and carries command-valued and
// path-redirect members bd does not control (DOLT_SSH_COMMAND runs a command in
// dolt's ssh transport; DOLT_ROOT_PATH redirects where dolt reads its global
// config, which can name an executable credential helper; DOLT_PAGER /
// DOLT_EDITOR / DOLT_BIN_PATH are the same class). A prefix allow of DOLT_*
// therefore re-opens the very command-execution hole this filter exists to
// close, so the prefix is NOT admitted and only these two keys are.
var AllowedDoltEnvKeys = []string{
	"DOLT_REMOTE_USER",
	"DOLT_REMOTE_PASSWORD",
}

// AllowedProjectEnvKey reports whether key may be imported from a project
// .beads/.env file. Comparison is case-insensitive so mixed-case keys on
// Windows cannot bypass the filter. Only BEADS_* and BD_* keys outside the
// command and credential-redirection sets, plus the explicit Dolt remote
// credential keys in AllowedDoltEnvKeys, are admitted: the broader DOLT_*
// namespace is deliberately excluded (see AllowedDoltEnvKeys) and non-prefixed
// keys are refused so child git/dolt/ssh processes cannot inherit GIT_*, SSH_*,
// LD_*, or DYLD_* hooks.
func AllowedProjectEnvKey(key string) (bool, string) {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return false, "empty environment variable name"
	}
	for _, k := range CommandEnvKeys {
		if upper == k {
			return false, "command settings must come from your shell environment or user config"
		}
	}
	for _, k := range CredentialRedirectEnvKeys {
		if upper == k {
			return false, "credential target settings must come from your shell environment or user config"
		}
	}
	for _, k := range AllowedDoltEnvKeys {
		if upper == k {
			return true, ""
		}
	}
	if !strings.HasPrefix(upper, "BEADS_") && !strings.HasPrefix(upper, "BD_") {
		return false, "only BEADS_*, BD_*, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"
	}
	return true, ""
}

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
