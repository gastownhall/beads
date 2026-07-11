package mysql

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
)

const baseDSN = "bts@tcp(127.0.0.1:55441)/"

// TestMain points the credentials-file rung at a nonexistent path in a private temp
// dir so tests never read the developer's real ~/.config/beads/credentials.
// go-sql-driver reads no ambient env/option files, so there is nothing else to
// neutralize. Tests that exercise the file rung override it per-test with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beads-mysql-cred-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// password parses out and returns the password go-sql-driver sees.
func password(t *testing.T, dsn string) string {
	t.Helper()
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("gomysql.ParseDSN(%q): %v", dsn, err)
	}
	return cfg.Passwd
}

// user parses out and returns the username go-sql-driver sees.
func user(t *testing.T, dsn string) string {
	t.Helper()
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("gomysql.ParseDSN(%q): %v", dsn, err)
	}
	return cfg.User
}

// writeCredsFile writes an INI credentials file and returns its path.
func writeCredsFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveDSNCredentialCommand(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "printf cmd-pw")
	t.Setenv("BEADS_MYSQL_PASSWORD", "")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "cmd-pw" {
		t.Fatalf("password = %q, want cmd-pw", pw)
	}
}

func TestResolveDSNCredentialEnv(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_MYSQL_PASSWORD", "env-pw")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "env-pw" {
		t.Fatalf("password = %q, want env-pw", pw)
	}
}

// The command out-ranks the static env password.
func TestResolveDSNCredentialCommandBeatsEnv(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "printf cmd-wins")
	t.Setenv("BEADS_MYSQL_PASSWORD", "env-loses")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "cmd-wins" {
		t.Fatalf("password = %q, want cmd-wins", pw)
	}
}

// A configured-but-erroring command aborts the open and does NOT fall through to the
// env password. Here a bare token with whitespace is rejected by parseCredential — a
// configured error, not a command exit failure.
func TestResolveDSNCredentialFailsClosed(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", `printf 'access denied'`)
	t.Setenv("BEADS_MYSQL_PASSWORD", "env-would-be-wrong")
	_, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err == nil {
		t.Fatal("expected an error (fail-closed); a broken command must not fall through to BEADS_MYSQL_PASSWORD")
	}
}

// A command that exits non-zero also aborts (the other configured-error shape).
func TestResolveDSNCredentialFailsClosedOnExit(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "false")
	t.Setenv("BEADS_MYSQL_PASSWORD", "env-would-be-wrong")
	_, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err == nil {
		t.Fatal("expected an error when the command exits non-zero")
	}
}

// A getToken/ExecCredential JSON envelope resolves the password end-to-end.
func TestResolveDSNCredentialJSONEnvelope(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", `printf '{"access_token":"tok-pw","expires_in":90}'`)
	t.Setenv("BEADS_MYSQL_PASSWORD", "")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "tok-pw" {
		t.Fatalf("password = %q, want tok-pw", pw)
	}
}

// End-to-end: resolving a password into a userless base DSN must fail loudly (the
// grammar would silently drop it), not connect passwordless.
func TestResolveDSNCredentialUserlessDSNFailsLoudly(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_MYSQL_PASSWORD", "some-pw")
	_, err := resolveDSNCredential(context.Background(), &configfile.Config{}, "tcp(127.0.0.1:55441)/")
	if err == nil {
		t.Fatal("expected an error placing a password into a userless DSN")
	}
}

// withDatabase refuses a userless password-bearing DSN (the path that skips the
// ladder via an inline ":secret@tcp(host)/" URL) rather than silently dropping it.
func TestWithDatabaseRefusesUserlessPassword(t *testing.T) {
	if _, err := withDatabase(":secret@tcp(127.0.0.1:55441)/", "db"); err == nil {
		t.Fatal("expected withDatabase to refuse a userless password-bearing DSN")
	}
	// A normal user:pass DSN still works.
	if _, err := withDatabase("bts:bts@tcp(127.0.0.1:55441)/", "db"); err != nil {
		t.Fatalf("withDatabase rejected a valid DSN: %v", err)
	}
}

func TestResolveDSNCredentialNothingConfigured(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_MYSQL_PASSWORD", "")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if got != baseDSN {
		t.Fatalf("dsn = %q, want it unchanged (%q)", got, baseDSN)
	}
}

func TestResolveDSNCredentialExistingPasswordWins(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "printf should-not-run")
	t.Setenv("BEADS_MYSQL_PASSWORD", "should-not-run")
	withPw := "bts:already-here@tcp(127.0.0.1:55441)/"
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, withPw)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "already-here" {
		t.Fatalf("password = %q, want already-here (existing password must win)", pw)
	}
}

// The credentials file resolves the password when no command or env is set.
func TestResolveDSNCredentialFile(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_MYSQL_PASSWORD", "")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:55441]\npassword=file-pw\n"))
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "file-pw" {
		t.Fatalf("password = %q, want file-pw", pw)
	}
}

// The env password out-ranks the credentials file.
func TestResolveDSNCredentialEnvBeatsFile(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_MYSQL_PASSWORD", "env-pw")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:55441]\npassword=file-pw\n"))
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "env-pw" {
		t.Fatalf("password = %q, want env-pw (env out-ranks file)", pw)
	}
}

// A broken command fails closed even when the file could supply a valid password.
func TestResolveDSNCredentialBrokenCommandNotRescuedByFile(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "false")
	t.Setenv("BEADS_MYSQL_PASSWORD", "")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:55441]\npassword=file-pw\n"))
	if _, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN); err == nil {
		t.Fatal("expected fail-closed; the credentials file must not rescue a broken command")
	}
}

// A unix-socket DSN has no [host:port] endpoint, so the file rung degrades to
// not-configured (no crash), leaving the DSN untouched.
func TestResolveDSNCredentialFileUnixSocketDegrades(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_MYSQL_PASSWORD", "")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:55441]\npassword=file-pw\n"))
	unixDSN := "bts@unix(/tmp/mysql.sock)/db"
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, unixDSN)
	if err != nil {
		t.Fatal(err)
	}
	if got != unixDSN {
		t.Fatalf("dsn = %q, want it unchanged (unix socket has no file-rung endpoint)", got)
	}
}

// A set BEADS_MYSQL_CREDENTIAL_COMMAND is a hard error, pre-empting even a
// password-bearing DSN and any lower rung.
func TestResolveDSNCredentialRejectsReservedIdentityVar(t *testing.T) {
	t.Setenv("BEADS_MYSQL_CREDENTIAL_COMMAND", "get-credential")
	t.Setenv("BEADS_MYSQL_PASSWORD", "would-otherwise-work")
	_, err := resolveDSNCredential(context.Background(), &configfile.Config{}, "bts:has-pw@tcp(127.0.0.1:55441)/")
	if err == nil || !strings.Contains(err.Error(), "BEADS_MYSQL_CREDENTIAL_COMMAND") {
		t.Fatalf("expected a reserved-var rejection naming BEADS_MYSQL_CREDENTIAL_COMMAND, got %v", err)
	}
}

// staticSource is a test Source with a fixed credential, used to exercise placement
// and the identity-refusal guard directly.
type staticSource struct{ cred creds.Credential }

func (s staticSource) Name() string { return "test" }
func (s staticSource) Resolve(context.Context) (creds.Credential, bool, error) {
	return s.cred, true, nil
}

func TestResolveDSNCredentialRefusesIdentity(t *testing.T) {
	src := staticSource{cred: creds.Credential{Value: "an-identity-token", Kind: creds.KindIdentity, Source: "test"}}
	_, err := resolveDSNWithSources(context.Background(), baseDSN, src)
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("expected an identity-refusal error, got %v", err)
	}
}

// A credential carrying a username (a Vault dynamic user/password pair) places both,
// overriding the base DSN's user.
func TestResolveDSNCredentialPlacesUsername(t *testing.T) {
	src := staticSource{cred: creds.Credential{Value: "pw", Username: "dyn", Kind: creds.KindSecret}}
	got, err := resolveDSNWithSources(context.Background(), baseDSN, src)
	if err != nil {
		t.Fatal(err)
	}
	if u, pw := user(t, got), password(t, got); u != "dyn" || pw != "pw" {
		t.Fatalf("(user,password) = (%q,%q), want (dyn,pw)", u, pw)
	}
}

// A dynamic username satisfies the userless-DSN requirement: a bare tcp(host)/ base
// plus a user/password pair is a valid configuration.
func TestResolveDSNWithSourcesUsernameSatisfiesUserlessBase(t *testing.T) {
	src := staticSource{cred: creds.Credential{Value: "pw", Username: "dyn", Kind: creds.KindSecret}}
	got, err := resolveDSNWithSources(context.Background(), "tcp(127.0.0.1:55441)/", src)
	if err != nil {
		t.Fatal(err)
	}
	if u, pw := user(t, got), password(t, got); u != "dyn" || pw != "pw" {
		t.Fatalf("(user,password) = (%q,%q), want (dyn,pw)", u, pw)
	}
}
