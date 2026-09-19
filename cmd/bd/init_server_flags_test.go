package main

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newServerFlagsCmd() *cobra.Command {
	c := &cobra.Command{}
	c.Flags().String("server-host", "", "")
	c.Flags().Int("server-port", 0, "")
	c.Flags().String("server-socket", "", "")
	c.Flags().String("server-user", "", "")
	c.Flags().Bool("server-tls", false, "")
	return c
}

func mustSetFlag(t *testing.T, c *cobra.Command, name, value string) {
	t.Helper()
	if err := c.Flags().Set(name, value); err != nil {
		t.Fatal(err)
	}
}

// mustPromote applies the promotion and registers its restore for the end of
// the subtest, so the cases below assert promoted state without leaking it.
func mustPromote(t *testing.T, c *cobra.Command) {
	t.Helper()
	restore := mustPromoteWithRestore(t, c)
	t.Cleanup(restore)
}

func mustPromoteWithRestore(t *testing.T, c *cobra.Command) func() {
	t.Helper()
	restore, err := promoteExplicitServerConnFlags(c)
	if err != nil {
		t.Fatalf("promoteExplicitServerConnFlags: %v", err)
	}
	return restore
}

func TestPromoteExplicitServerConnFlags(t *testing.T) {
	t.Run("explicit flags override environment", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "profile-host")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")
		t.Setenv("BEADS_DOLT_SERVER_USER", "profile-user")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-host", "db.example.com")
		mustSetFlag(t, c, "server-port", "3306")
		mustSetFlag(t, c, "server-user", "app_rw")

		mustPromote(t, c)

		if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "db.example.com" {
			t.Errorf("BEADS_DOLT_SERVER_HOST = %q, want db.example.com", got)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_PORT"); got != "3306" {
			t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want 3306", got)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_USER"); got != "app_rw" {
			t.Errorf("BEADS_DOLT_SERVER_USER = %q, want app_rw", got)
		}
	})

	t.Run("unset flags leave environment untouched", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "profile-host")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/profile.sock")

		mustPromote(t, newServerFlagsCmd())

		if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "profile-host" {
			t.Errorf("BEADS_DOLT_SERVER_HOST = %q, want profile-host", got)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_PORT"); got != "3307" {
			t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want 3307", got)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_SOCKET"); got != "/tmp/profile.sock" {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want /tmp/profile.sock (untouched)", got)
		}
	})

	t.Run("server-tls flag promotes both directions", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_TLS", "1")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-tls", "false")
		mustPromote(t, c)
		if got := os.Getenv("BEADS_DOLT_SERVER_TLS"); got != "0" {
			t.Errorf("BEADS_DOLT_SERVER_TLS = %q, want 0 (explicit --server-tls=false)", got)
		}

		c = newServerFlagsCmd()
		mustSetFlag(t, c, "server-tls", "true")
		mustPromote(t, c)
		if got := os.Getenv("BEADS_DOLT_SERVER_TLS"); got != "1" {
			t.Errorf("BEADS_DOLT_SERVER_TLS = %q, want 1 (explicit --server-tls)", got)
		}
	})

	t.Run("unset server-tls leaves environment untouched", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_TLS", "true")
		mustPromote(t, newServerFlagsCmd())
		if got := os.Getenv("BEADS_DOLT_SERVER_TLS"); got != "true" {
			t.Errorf("BEADS_DOLT_SERVER_TLS = %q, want true (untouched)", got)
		}
	})

	t.Run("socket flag overrides environment", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/stale.sock")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-socket", "/tmp/fresh.sock")
		mustPromote(t, c)

		if got := os.Getenv("BEADS_DOLT_SERVER_SOCKET"); got != "/tmp/fresh.sock" {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want /tmp/fresh.sock", got)
		}
	})

	t.Run("explicit empty socket selects TCP over ambient socket", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/stale.sock")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-socket", "")
		mustPromote(t, c)

		if got, ok := os.LookupEnv("BEADS_DOLT_SERVER_SOCKET"); ok {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want unset (empty means TCP)", got)
		}
	})

	t.Run("explicit TCP flags clear ambient socket", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/stale.sock")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-host", "db.example.com")
		mustPromote(t, c)

		if got, ok := os.LookupEnv("BEADS_DOLT_SERVER_SOCKET"); ok {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want unset (explicit TCP selected)", got)
		}

		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/stale.sock")
		c = newServerFlagsCmd()
		mustSetFlag(t, c, "server-port", "3306")
		mustPromote(t, c)

		if got, ok := os.LookupEnv("BEADS_DOLT_SERVER_SOCKET"); ok {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want unset (explicit TCP selected)", got)
		}
	})

	t.Run("explicit socket wins over explicit TCP flags", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/stale.sock")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-host", "db.example.com")
		mustSetFlag(t, c, "server-socket", "/tmp/fresh.sock")
		mustPromote(t, c)

		if got := os.Getenv("BEADS_DOLT_SERVER_SOCKET"); got != "/tmp/fresh.sock" {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want /tmp/fresh.sock (socket flag documented to override host/port)", got)
		}
	})

	t.Run("non-TCP flags do not clear ambient socket", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/profile.sock")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-user", "app_rw")
		mustPromote(t, c)

		if got := os.Getenv("BEADS_DOLT_SERVER_SOCKET"); got != "/tmp/profile.sock" {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want /tmp/profile.sock (untouched)", got)
		}
	})

	t.Run("changed empty and out-of-range values fail explicitly", func(t *testing.T) {
		cases := []struct {
			name    string
			flag    string
			value   string
			wantSub string
		}{
			{"empty host", "server-host", "", "--server-host cannot be empty"},
			{"zero port", "server-port", "0", "--server-port must be between 1 and 65535"},
			{"negative port", "server-port", "-1", "--server-port must be between 1 and 65535"},
			{"oversized port", "server-port", "65536", "--server-port must be between 1 and 65535"},
			{"empty user", "server-user", "", "--server-user cannot be empty"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("BEADS_DOLT_SERVER_HOST", "profile-host")
				t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")
				t.Setenv("BEADS_DOLT_SERVER_USER", "profile-user")

				c := newServerFlagsCmd()
				mustSetFlag(t, c, tc.flag, tc.value)

				restore, err := promoteExplicitServerConnFlags(c)
				if err == nil {
					t.Fatalf("promoteExplicitServerConnFlags = nil, want error containing %q", tc.wantSub)
				}
				if restore == nil {
					t.Fatal("restore func is nil on error; callers defer it unconditionally")
				}
				restore()
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error = %q, want substring %q", err, tc.wantSub)
				}
				if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "profile-host" {
					t.Errorf("BEADS_DOLT_SERVER_HOST = %q, want profile-host (untouched on error)", got)
				}
				if got := os.Getenv("BEADS_DOLT_SERVER_PORT"); got != "3307" {
					t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want 3307 (untouched on error)", got)
				}
				if got := os.Getenv("BEADS_DOLT_SERVER_USER"); got != "profile-user" {
					t.Errorf("BEADS_DOLT_SERVER_USER = %q, want profile-user (untouched on error)", got)
				}
			})
		}
	})
}

// The promotion mutates process-global state. One CLI process runs one init,
// but the test binary (and any embedding host) runs many in the same process,
// so every override must be reversible. These cases pin the restore contract
// that keeps main's suites passing unmodified.
func TestPromoteExplicitServerConnFlagsRestoresEnv(t *testing.T) {
	t.Run("restores previous values after a successful promotion", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "profile-host")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")
		t.Setenv("BEADS_DOLT_SERVER_USER", "profile-user")
		t.Setenv("BEADS_DOLT_SERVER_TLS", "1")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-host", "db.example.com")
		mustSetFlag(t, c, "server-port", "3306")
		mustSetFlag(t, c, "server-user", "app_rw")
		mustSetFlag(t, c, "server-tls", "false")

		restore := mustPromoteWithRestore(t, c)

		if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "db.example.com" {
			t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want db.example.com while promoted", got)
		}

		restore()

		for key, want := range map[string]string{
			"BEADS_DOLT_SERVER_HOST": "profile-host",
			"BEADS_DOLT_SERVER_PORT": "3307",
			"BEADS_DOLT_SERVER_USER": "profile-user",
			"BEADS_DOLT_SERVER_TLS":  "1",
		} {
			if got := os.Getenv(key); got != want {
				t.Errorf("after restore %s = %q, want %q", key, got, want)
			}
		}
	})

	t.Run("restores absent variables to absent", func(t *testing.T) {
		for _, key := range []string{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_SOCKET"} {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-host", "db.example.com")
		mustSetFlag(t, c, "server-port", "3306")

		restore := mustPromoteWithRestore(t, c)
		if got := os.Getenv("BEADS_DOLT_SERVER_PORT"); got != "3306" {
			t.Fatalf("BEADS_DOLT_SERVER_PORT = %q, want 3306 while promoted", got)
		}

		restore()

		for _, key := range []string{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT"} {
			if got, ok := os.LookupEnv(key); ok {
				t.Errorf("after restore %s = %q, want absent", key, got)
			}
		}
	})

	t.Run("restores a socket cleared by explicit TCP selection", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/profile.sock")

		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-host", "db.example.com")

		restore := mustPromoteWithRestore(t, c)
		if got, ok := os.LookupEnv("BEADS_DOLT_SERVER_SOCKET"); ok {
			t.Fatalf("BEADS_DOLT_SERVER_SOCKET = %q, want absent while promoted", got)
		}

		restore()

		if got := os.Getenv("BEADS_DOLT_SERVER_SOCKET"); got != "/tmp/profile.sock" {
			t.Errorf("after restore BEADS_DOLT_SERVER_SOCKET = %q, want /tmp/profile.sock", got)
		}
	})

	t.Run("a rejected flag leaves earlier valid flags unapplied", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "profile-host")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")

		// Host is valid and resolves first; the port is not. Validation runs
		// to completion before any variable is written, so the host must not
		// survive the rejection.
		c := newServerFlagsCmd()
		mustSetFlag(t, c, "server-host", "db.example.com")
		mustSetFlag(t, c, "server-port", "0")

		restore, err := promoteExplicitServerConnFlags(c)
		if err == nil {
			t.Fatal("promoteExplicitServerConnFlags = nil, want an out-of-range port error")
		}
		restore()

		if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "profile-host" {
			t.Errorf("BEADS_DOLT_SERVER_HOST = %q, want profile-host (unapplied)", got)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_PORT"); got != "3307" {
			t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want 3307 (unapplied)", got)
		}
	})

	t.Run("sequential in-process promotions do not accumulate", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "profile-host")
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/tmp/profile.sock")

		// First invocation promotes, as a server-mode init would.
		first := newServerFlagsCmd()
		mustSetFlag(t, first, "server-host", "db.example.com")
		mustPromoteWithRestore(t, first)()

		// Second invocation passes no connection flags, as an embedded-mode
		// init would. It must see the ambient environment, not the first
		// invocation's overrides.
		mustPromoteWithRestore(t, newServerFlagsCmd())()

		if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "profile-host" {
			t.Errorf("BEADS_DOLT_SERVER_HOST = %q, want profile-host (first promotion leaked)", got)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_SOCKET"); got != "/tmp/profile.sock" {
			t.Errorf("BEADS_DOLT_SERVER_SOCKET = %q, want /tmp/profile.sock (first promotion leaked)", got)
		}
	})
}
