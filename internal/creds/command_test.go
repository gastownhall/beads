package creds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil/credentialcmd"
)

func TestParseCredential(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantTok  string
		wantUser string
		wantExp  bool // expect a non-zero expiry
		wantErr  bool
	}{
		{"bare token (JWT-shaped)", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig\n", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig", "", false, false},
		{"execcredential token+exp", `{"token":"abc","expirationTimestamp":"2099-01-02T15:04:05Z"}`, "abc", "", true, false},
		{"OAuth access_token+expires_in", `{"access_token":"xyz","expires_in":90,"token_type":"Bearer"}`, "xyz", "", true, false},
		{"vault dynamic user+pass pair", `{"username":"v-app-beads-xyz","password":"s3cr3t","expires_in":3600}`, "", "", true, true}, // token/access_token absent -> error
		{"pair via access_token+username", `{"access_token":"pw","username":"dynuser","expires_in":900}`, "pw", "dynuser", true, false},
		{"json without token", `{"foo":"bar"}`, "", "", false, true},
		{"unparseable json", `{not json`, "", "", false, true},
		{"empty output", "   \n", "", "", false, true},
		{"bare with whitespace (error message)", "access denied: nope", "", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, user, exp, err := parseCredential([]byte(c.in))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got token=%q", tok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tok != c.wantTok {
				t.Fatalf("token = %q, want %q", tok, c.wantTok)
			}
			if user != c.wantUser {
				t.Fatalf("username = %q, want %q", user, c.wantUser)
			}
			if c.wantExp && exp.IsZero() {
				t.Fatal("expected a non-zero expiry")
			}
			if !c.wantExp && !exp.IsZero() {
				t.Fatalf("expected zero expiry, got %v", exp)
			}
		})
	}
}

// resetCache isolates the process-level command cache and runner for a test.
func resetCache(t *testing.T) {
	t.Helper()
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })
}

// resolveCredentialToken caches by command until near expiry, then re-runs the helper.
func TestResolveCredentialTokenCachesUntilExpiry(t *testing.T) {
	resetCache(t)

	var calls int
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		calls++
		// Long-lived expiry so the cache holds across the second call.
		return []byte(fmt.Sprintf(`{"token":"tok-%d","expirationTimestamp":%q}`, calls,
			time.Now().Add(time.Hour).Format(time.RFC3339))), nil
	}

	tok1, _, _, err := resolveCredentialToken(context.Background(), "helper --x")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	tok2, _, _, err := resolveCredentialToken(context.Background(), "helper --x")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if tok1 != tok2 || calls != 1 {
		t.Fatalf("expected one cached helper call, got calls=%d tok1=%q tok2=%q", calls, tok1, tok2)
	}

	// A different command is a different cache key -> a fresh run.
	if _, _, _, err := resolveCredentialToken(context.Background(), "helper --y"); err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a fresh run for a new command, got calls=%d", calls)
	}
}

func TestResolveCredentialTokenPropagatesHelperError(t *testing.T) {
	resetCache(t)
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}
	if _, _, _, err := resolveCredentialToken(context.Background(), "broken-helper"); err == nil {
		t.Fatal("expected an error when the helper fails")
	}
}

// A real shell command flows end-to-end through CommandSource (no stub), proving the
// platform shell runner and the bare-token path work together.
func TestCommandSourceRealShell(t *testing.T) {
	resetCache(t)

	// Capture the helper's stdout as the shell actually produced it. The whole
	// reason Emit exists rather than an echo-style emitter is byte-exact stdout
	// across sh and the Windows .cmd trampoline — but parseCredential runs
	// bytes.TrimSpace before every comparison (command.go), so every assertion
	// on cred.Value is blind to a trailing "\r\n". Without the capture below,
	// a trampoline changed to `echo %OUT%` would keep this test green while
	// silently breaking the property the fixture was built for. Wrapping the
	// existing package seam keeps the real shell in the loop instead of
	// replacing it, so this stays an end-to-end assertion.
	var rawStdout []byte
	origRunner := credRunner
	t.Cleanup(func() { credRunner = origRunner })
	credRunner = func(ctx context.Context, command string) ([]byte, error) {
		out, err := origRunner(ctx, command)
		rawStdout = append([]byte(nil), out...)
		return out, err
	}

	src := CommandSource{Command: credentialcmd.Emit(t, "s3cr3t"), Kind: KindSecret, Label: "TEST_CMD"}
	cred, ok, err := src.Resolve(context.Background())
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if string(rawStdout) != "s3cr3t" {
		t.Fatalf("helper raw stdout = %q, want exactly %q: the fixture must emit the payload with no "+
			"trailing newline on either shell, and parseCredential's TrimSpace hides any that appears", rawStdout, "s3cr3t")
	}
	if cred.Value != "s3cr3t" {
		t.Fatalf("value = %q, want s3cr3t", cred.Value)
	}
	if cred.Kind != KindSecret {
		t.Fatalf("kind = %v, want KindSecret", cred.Kind)
	}
	if cred.Source != "TEST_CMD" {
		t.Fatalf("source = %q, want TEST_CMD", cred.Source)
	}
}

func TestCredentialCommandFixtureProtocol(t *testing.T) {
	resetCache(t)
	marker := filepath.Join(t.TempDir(), "marker path with spaces")
	src := CommandSource{Command: credentialcmd.Marker(t, marker), Kind: KindSecret, Label: "TEST_MARKER"}
	_, ok, err := src.Resolve(context.Background())
	if !ok || err == nil || !strings.Contains(err.Error(), "credential command produced no output") {
		t.Fatalf("marker resolve: ok=%v err=%v, want configured empty-output error", ok, err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read credential command marker: %v", err)
	}
	if string(got) != "invoked" {
		t.Fatalf("credential command marker = %q, want invoked", got)
	}
}

// TestAllowedProjectEnvKeyRefusesExecutableAndBearerSettings pins what a
// project .beads/.env must never supply beyond the exact command list: a
// setting whose name marks it as executable (a *_COMMAND, *_BIN or *_BINARY
// key, matched by suffix so a command setting added later is refused without
// anyone extending CommandEnvKeys); the http backend's bearer token, which
// outranks every credential source the caller configured; the plaintext grant
// that lets that bearer cross the network in the clear; and the token file
// that decides which bearers bd serve accepts. The documented per-project Dolt
// passwords (GH#2520) and ordinary BEADS_*/BD_* settings, including ones that
// merely contain COMMAND or TOKEN, stay admitted.
func TestAllowedProjectEnvKeyRefusesExecutableAndBearerSettings(t *testing.T) {
	const (
		commandReason    = "command settings must come from your shell environment or user config"
		credentialReason = "credential settings must come from your shell environment or user config"
		targetReason     = "credential target settings must come from your shell environment or user config"
	)
	for _, tc := range []struct {
		key    string
		ok     bool
		reason string
	}{
		{key: "BEADS_DOLT_PASSWORD_COMMAND", reason: commandReason},
		{key: "BEADS_EXAMPLE_HELPER_COMMAND", reason: commandReason},
		{key: "BD_EXAMPLE_COMMAND", reason: commandReason},
		{key: "beads_example_command", reason: commandReason},
		{key: " Bd_Example_Command ", reason: commandReason},
		{key: "BEADS_EXAMPLE_BIN", reason: commandReason},
		{key: "BD_EXAMPLE_BINARY", reason: commandReason},
		{key: "BEADS_HTTP_TOKEN", reason: credentialReason},
		{key: "beads_http_token", reason: credentialReason},
		{key: "BEADS_HTTP_ALLOW_INSECURE", reason: targetReason},
		{key: "BEADS_SERVE_TOKEN_FILE", reason: targetReason},
		{key: "BEADS_DOLT_PASSWORD", ok: true},
		{key: "DOLT_REMOTE_PASSWORD", ok: true},
		{key: "GT_ROOT", ok: true},
		{key: "BEADS_ACTOR", ok: true},
		{key: "BEADS_HTTP_RESPONSE_HEADER_TIMEOUT", ok: true},
		{key: "BEADS_COMMAND_TIMEOUT", ok: true},
		{key: "BD_TOKEN_BUDGET", ok: true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			ok, reason := AllowedProjectEnvKey(tc.key)
			if ok != tc.ok || reason != tc.reason {
				t.Errorf("AllowedProjectEnvKey(%q) = (%v, %q), want (%v, %q)", tc.key, ok, reason, tc.ok, tc.reason)
			}
		})
	}
}

// TestAllowedProjectEnvKeyRefusesOutputFileSettings pins that a project
// .beads/.env cannot name a file bd creates, truncates or appends to: the heap
// profile and MemStats destinations bd writes on exit, and the log the proxied
// Dolt server appends to. A committed value would choose a file on the
// caller's machine for bd to overwrite. The profiling switch that names no
// path, and the directories bd keeps state of its own naming in, stay
// admitted.
func TestAllowedProjectEnvKeyRefusesOutputFileSettings(t *testing.T) {
	const outputReason = "output file settings must come from your shell environment"
	for _, tc := range []struct {
		key    string
		ok     bool
		reason string
	}{
		{key: "BEADS_MEM_PROFILE", reason: outputReason},
		{key: "beads_mem_profile", reason: outputReason},
		{key: " Beads_Mem_Stats ", reason: outputReason},
		{key: "BEADS_MEM_STATS", reason: outputReason},
		{key: "BEADS_PROXIED_SERVER_LOG", reason: outputReason},
		{key: "BEADS_MEM_PROFILE_NOGC", ok: true},
		{key: "BEADS_DOLT_DATA_DIR", ok: true},
		{key: "BEADS_SHARED_SERVER_DIR", ok: true},
		{key: "BEADS_PROXIED_SERVER_ROOT_PATH", ok: true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			ok, reason := AllowedProjectEnvKey(tc.key)
			if ok != tc.ok || reason != tc.reason {
				t.Errorf("AllowedProjectEnvKey(%q) = (%v, %q), want (%v, %q)", tc.key, ok, reason, tc.ok, tc.reason)
			}
		})
	}
}
