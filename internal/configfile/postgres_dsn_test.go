package configfile

import "testing"

// GetPostgresDSN returns the BASE DSN and never merges a password: BEADS_PG_PASSWORD
// is resolved and placed at open time in the postgres backend (which owns the pgx
// parser), so the persisted, password-free metadata DSN comes back unchanged here.
func TestGetPostgresDSNReturnsBaseUnmerged(t *testing.T) {
	t.Setenv("BEADS_PG_PASSWORD", "secret")
	c := &Config{PostgresDSN: "postgres://bts@127.0.0.1:5432/db"}
	if got, want := c.GetPostgresDSN(), "postgres://bts@127.0.0.1:5432/db"; got != want {
		t.Fatalf("GetPostgresDSN() = %q, want %q (must not merge the password)", got, want)
	}
}

// BEADS_POSTGRES_URL (full override) takes precedence over the metadata DSN.
func TestGetPostgresDSNEnvURLPrecedence(t *testing.T) {
	t.Setenv("BEADS_POSTGRES_URL", "postgres://a:b@host:5432/db")
	c := &Config{PostgresDSN: "postgres://other@127.0.0.1:5432/db"}
	if got, want := c.GetPostgresDSN(), "postgres://a:b@host:5432/db"; got != want {
		t.Fatalf("GetPostgresDSN() = %q, want %q", got, want)
	}
}

// GetPostgresPasswordCommand reads BEADS_PG_PASSWORD_COMMAND from the environment
// only (metadata is deliberately excluded until a workspace-trust gate exists).
func TestGetPostgresPasswordCommand(t *testing.T) {
	c := &Config{}
	if got := c.GetPostgresPasswordCommand(); got != "" {
		t.Fatalf("GetPostgresPasswordCommand() = %q, want empty when unset", got)
	}
	t.Setenv("BEADS_PG_PASSWORD_COMMAND", "get-credential")
	if got, want := c.GetPostgresPasswordCommand(), "get-credential"; got != want {
		t.Fatalf("GetPostgresPasswordCommand() = %q, want %q", got, want)
	}
}
