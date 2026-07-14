package configfile

import "testing"

// GetMySQLDSN returns the BASE DSN and never merges a password: BEADS_MYSQL_PASSWORD
// is resolved and placed at open time in the mysql backend (which owns the
// go-sql-driver parser), so the persisted, password-free metadata DSN comes back
// unchanged here.
func TestGetMySQLDSNReturnsBaseUnmerged(t *testing.T) {
	t.Setenv("BEADS_MYSQL_PASSWORD", "secret")
	c := &Config{MySQLDSN: "bts@tcp(127.0.0.1:3306)/"}
	if got, want := c.GetMySQLDSN(), "bts@tcp(127.0.0.1:3306)/"; got != want {
		t.Fatalf("GetMySQLDSN() = %q, want %q (must not merge the password)", got, want)
	}
}

// BEADS_MYSQL_URL (full override) takes precedence over the metadata DSN.
func TestGetMySQLDSNEnvURLPrecedence(t *testing.T) {
	t.Setenv("BEADS_MYSQL_URL", "root:pw@tcp(host:3306)/db")
	c := &Config{MySQLDSN: "other@tcp(127.0.0.1:3306)/"}
	if got, want := c.GetMySQLDSN(), "root:pw@tcp(host:3306)/db"; got != want {
		t.Fatalf("GetMySQLDSN() = %q, want %q", got, want)
	}
}

// GetMySQLPasswordCommand reads BEADS_MYSQL_PASSWORD_COMMAND from the environment
// only (metadata is deliberately excluded until a workspace-trust gate exists).
func TestGetMySQLPasswordCommand(t *testing.T) {
	c := &Config{}
	if got := c.GetMySQLPasswordCommand(); got != "" {
		t.Fatalf("GetMySQLPasswordCommand() = %q, want empty when unset", got)
	}
	t.Setenv("BEADS_MYSQL_PASSWORD_COMMAND", "get-credential")
	if got, want := c.GetMySQLPasswordCommand(), "get-credential"; got != want {
		t.Fatalf("GetMySQLPasswordCommand() = %q, want %q", got, want)
	}
}
