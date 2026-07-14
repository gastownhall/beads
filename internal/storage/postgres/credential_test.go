package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
)

const baseDSN = "postgres://bts@127.0.0.1:5432/db"

// TestMain neutralizes every ambient credential source the rungs could read, so tests
// are hermetic: the beads credentials file, plus pgx's own PGPASSWORD/~/.pgpass (which
// pgx.ParseConfig folds into a connection at parse/connect time). Tests that exercise
// the file rung override BEADS_CREDENTIALS_FILE per-test with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beads-pg-cred-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	os.Unsetenv("PGPASSWORD")
	os.Setenv("PGPASSFILE", filepath.Join(dir, "no-pgpass"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// password parses out and returns the password pgx sees, failing the test on a parse error.
func password(t *testing.T, dsn string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(%q): %v", dsn, err)
	}
	return cfg.Password
}

// user parses out and returns the username pgx sees.
func user(t *testing.T, dsn string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(%q): %v", dsn, err)
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
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "printf cmd-pw")
	t.Setenv("BEADS_PG_PASSWORD", "")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "cmd-pw" {
		t.Fatalf("password = %q, want cmd-pw", pw)
	}
}

func TestResolveDSNCredentialEnv(t *testing.T) {
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_PG_PASSWORD", "env-pw")
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
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "printf cmd-wins")
	t.Setenv("BEADS_PG_PASSWORD", "env-loses")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "cmd-wins" {
		t.Fatalf("password = %q, want cmd-wins", pw)
	}
}

// A failing command aborts the open and does NOT fall through to the env password.
func TestResolveDSNCredentialFailsClosed(t *testing.T) {
	// A bare token with whitespace is rejected by parseCredential — a configured error.
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", `printf 'access denied'`)
	t.Setenv("BEADS_PG_PASSWORD", "env-would-be-wrong")
	_, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err == nil {
		t.Fatal("expected an error (fail-closed); a broken command must not fall through to BEADS_PG_PASSWORD")
	}
}

// Nothing configured: the base DSN comes back untouched (pgx's own PGPASSWORD/.pgpass
// fallbacks then apply at connect).
func TestResolveDSNCredentialNothingConfigured(t *testing.T) {
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_PG_PASSWORD", "")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if got != baseDSN {
		t.Fatalf("dsn = %q, want it unchanged (%q)", got, baseDSN)
	}
}

// A DSN that already carries a password wins outright — the ladder is not applied.
func TestResolveDSNCredentialExistingPasswordWins(t *testing.T) {
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "printf should-not-run")
	t.Setenv("BEADS_PG_PASSWORD", "should-not-run")
	withPw := "postgres://bts:already-here@127.0.0.1:5432/db"
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
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_PG_PASSWORD", "")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:5432]\npassword=file-pw\n"))
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
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_PG_PASSWORD", "env-pw")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:5432]\npassword=file-pw\n"))
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if pw := password(t, got); pw != "env-pw" {
		t.Fatalf("password = %q, want env-pw (env out-ranks file)", pw)
	}
}

// A file section for a different endpoint does not match: the DSN is untouched.
func TestResolveDSNCredentialFileWrongSectionMisses(t *testing.T) {
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "")
	t.Setenv("BEADS_PG_PASSWORD", "")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:9999]\npassword=file-pw\n"))
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if got != baseDSN {
		t.Fatalf("dsn = %q, want it unchanged (wrong section must not match)", got)
	}
}

// A broken command fails closed even when the file could supply a valid password.
func TestResolveDSNCredentialBrokenCommandNotRescuedByFile(t *testing.T) {
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "false")
	t.Setenv("BEADS_PG_PASSWORD", "")
	t.Setenv("BEADS_CREDENTIALS_FILE", writeCredsFile(t, "[127.0.0.1:5432]\npassword=file-pw\n"))
	if _, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN); err == nil {
		t.Fatal("expected fail-closed; the credentials file must not rescue a broken command")
	}
}

// A set BEADS_PG_CREDENTIAL_COMMAND is a hard error, pre-empting even a
// password-bearing DSN and any lower rung.
func TestResolveDSNCredentialRejectsReservedIdentityVar(t *testing.T) {
	t.Setenv("BEADS_PG_CREDENTIAL_COMMAND", "get-credential")
	t.Setenv("BEADS_PG_PASSWORD", "would-otherwise-work")
	_, err := resolveDSNCredential(context.Background(), &configfile.Config{}, "postgres://bts:has-pw@127.0.0.1:5432/db")
	if err == nil || !strings.Contains(err.Error(), "BEADS_PG_CREDENTIAL_COMMAND") {
		t.Fatalf("expected a reserved-var rejection naming BEADS_PG_CREDENTIAL_COMMAND, got %v", err)
	}
}

// staticSource is a test Source with a fixed credential, used to exercise placement
// and the identity-refusal guard directly.
type staticSource struct{ cred creds.Credential }

func (s staticSource) Name() string { return "test" }
func (s staticSource) Resolve(context.Context) (creds.Credential, bool, error) {
	return s.cred, true, nil
}

// An identity credential (KindIdentity) has no home on a direct SQL connection and
// must be refused, never landed in the password slot.
func TestResolveDSNCredentialRefusesIdentity(t *testing.T) {
	src := staticSource{cred: creds.Credential{Value: "an-identity-token", Kind: creds.KindIdentity, Source: "test"}}
	_, err := resolveDSNWithSources(context.Background(), baseDSN, src)
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("expected an identity-refusal error, got %v", err)
	}
}

// A credential carrying a username (a Vault dynamic user/password pair) places both.
func TestResolveDSNCredentialPlacesUsername(t *testing.T) {
	src := staticSource{cred: creds.Credential{Value: "pw", Username: "dyn", Kind: creds.KindSecret}}
	got, err := resolveDSNWithSources(context.Background(), baseDSN, src)
	if err != nil {
		t.Fatal(err)
	}
	if u := user(t, got); u != "dyn" {
		t.Fatalf("user = %q, want dyn", u)
	}
	if pw := password(t, got); pw != "pw" {
		t.Fatalf("password = %q, want pw", pw)
	}
}

// An empty username leaves the DSN's user untouched.
func TestResolveDSNCredentialEmptyUsernameKeepsDSNUser(t *testing.T) {
	src := staticSource{cred: creds.Credential{Value: "pw", Username: "", Kind: creds.KindSecret}}
	got, err := resolveDSNWithSources(context.Background(), baseDSN, src)
	if err != nil {
		t.Fatal(err)
	}
	if u := user(t, got); u != "bts" {
		t.Fatalf("user = %q, want bts (DSN user preserved)", u)
	}
}

// End-to-end: a JSON envelope carrying a username places both user and password.
func TestResolveDSNCredentialEnvelopeUsernameEndToEnd(t *testing.T) {
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", `printf '{"token":"pw","username":"dyn"}'`)
	t.Setenv("BEADS_PG_PASSWORD", "")
	got, err := resolveDSNCredential(context.Background(), &configfile.Config{}, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if u, pw := user(t, got), password(t, got); u != "dyn" || pw != "pw" {
		t.Fatalf("(user,password) = (%q,%q), want (dyn,pw)", u, pw)
	}
}
