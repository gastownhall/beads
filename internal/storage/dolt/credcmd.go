package dolt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Credential-command hook: the vendor-neutral way bd obtains a short-lived server-mode
// credential. It follows the established external "credential process" idiom (kubectl
// client.authentication.k8s.io ExecCredential, AWS credential_process, git/Docker credential
// helpers): bd runs a CONFIGURED command, reads a token + optional expiry from its stdout,
// caches the token until it expires, and re-runs the command when the cache is stale. bd knows
// nothing of the issuer (no STS / EIA / OIDC / DPoP here) — a hosted deployment simply sets the
// command (e.g. `gasworks getToken beads --org <org>`). The token is presented as the MySQL
// username over TLS, exactly where the gateway reads the credential.

const (
	credCommandTimeout = 30 * time.Second // a helper that hangs must not wedge an open
	credDefaultTTL     = 60 * time.Second // cache window when the helper reports no expiry
	credExpirySkew     = 10 * time.Second // refresh this long before the reported expiry
)

// execCredential is the union of envelopes bd accepts on the helper's stdout: the kubectl
// ExecCredential subset {token, expirationTimestamp} and the gasworks getToken --json shape
// {access_token, expires_in}. A helper may instead print a BARE token (no JSON) — see parseCredential.
type execCredential struct {
	Token               string `json:"token"`
	AccessToken         string `json:"access_token"`
	ExpirationTimestamp string `json:"expirationTimestamp"`
	ExpiresIn           int64  `json:"expires_in"`
}

type cachedCred struct {
	token   string
	expires time.Time
}

var (
	credCacheMu sync.Mutex
	credCache   = map[string]cachedCred{}

	// credRunner runs the helper; a package var so tests can stub it without spawning a shell.
	credRunner = func(ctx context.Context, command string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
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

// resolveCredentialToken returns the bearer token for the given helper command, using a
// process-level cache keyed by the command so repeated opens don't re-spawn the helper until
// the token is near expiry. It is concurrency-safe.
func resolveCredentialToken(ctx context.Context, command string) (string, error) {
	now := time.Now()

	credCacheMu.Lock()
	if c, ok := credCache[command]; ok && now.Before(c.expires.Add(-credExpirySkew)) {
		tok := c.token
		credCacheMu.Unlock()
		return tok, nil
	}
	credCacheMu.Unlock()

	// Derive the mint deadline from the caller's context (the dial context on the
	// connector path), capped at credCommandTimeout. An expired dial deadline now
	// aborts a slow or hung helper instead of blocking the caller for the full
	// timeout; a Background caller (the open-time eager resolve) keeps the cap.
	ctx, cancel := context.WithTimeout(ctx, credCommandTimeout)
	defer cancel()
	raw, err := credRunner(ctx, command)
	if err != nil {
		return "", fmt.Errorf("credential command failed: %w", err)
	}
	token, expiry, err := parseCredential(raw)
	if err != nil {
		return "", err
	}
	if expiry.IsZero() {
		expiry = now.Add(credDefaultTTL)
	}

	credCacheMu.Lock()
	credCache[command] = cachedCred{token: token, expires: expiry}
	credCacheMu.Unlock()
	return token, nil
}

// invalidateCredentialToken drops any cached token for command so the next resolve
// re-runs the helper. Called when the server rejects a presented token (auth
// failure) before its recorded expiry — e.g. a rotating credential revoked
// mid-life — so a new dial re-mints instead of re-presenting the dead token until
// its stale expiry. No-op if nothing is cached.
func invalidateCredentialToken(command string) {
	credCacheMu.Lock()
	delete(credCache, command)
	credCacheMu.Unlock()
}

// parseCredential extracts the token (and any expiry) from a helper's stdout. A JSON object
// is read as the ExecCredential/getToken envelope; otherwise the trimmed output is taken as a
// bare token. A bare value containing whitespace is rejected — that is almost always an error
// message, and using it as a username would only fail confusingly downstream.
func parseCredential(raw []byte) (token string, expiry time.Time, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", time.Time{}, fmt.Errorf("credential command produced no output")
	}

	if trimmed[0] == '{' {
		var c execCredential
		if jerr := json.Unmarshal(trimmed, &c); jerr != nil {
			return "", time.Time{}, fmt.Errorf("credential command returned unparseable JSON: %w", jerr)
		}
		token = c.Token
		if token == "" {
			token = c.AccessToken
		}
		if token == "" {
			return "", time.Time{}, fmt.Errorf("credential command JSON has no token/access_token field")
		}
		switch {
		case c.ExpirationTimestamp != "":
			if t, perr := time.Parse(time.RFC3339, c.ExpirationTimestamp); perr == nil {
				expiry = t
			}
		case c.ExpiresIn > 0:
			expiry = time.Now().Add(time.Duration(c.ExpiresIn) * time.Second)
		}
		return token, expiry, nil
	}

	bare := string(trimmed)
	if strings.ContainsAny(bare, " \t\r\n") {
		return "", time.Time{}, fmt.Errorf("credential command output is not a bare token (contains whitespace); expected a token or a JSON {token,expirationTimestamp} envelope")
	}
	return bare, time.Time{}, nil
}
