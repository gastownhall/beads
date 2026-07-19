package dolt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// stubCredRunner isolates the process-level credential cache and swaps credRunner
// for the duration of a test, restoring both on cleanup. Tests using it must NOT
// run in parallel — credCache and credRunner are package globals.
func stubCredRunner(t *testing.T, fn func(ctx context.Context, command string) ([]byte, error)) {
	t.Helper()
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	credRunner = fn
	t.Cleanup(func() {
		credRunner = orig
		credCacheMu.Lock()
		credCache = map[string]cachedCred{}
		credCacheMu.Unlock()
	})
}

func TestCredentialBeforeConnect_SetsUserFromHelper(t *testing.T) {
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(`{"access_token":"tokA","expires_in":300}`), nil
	})

	hook := credentialBeforeConnect("helper-sets-user")
	cfg := mysql.NewConfig()
	if err := hook(context.Background(), cfg); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if cfg.User != "tokA" {
		t.Fatalf("cfg.User = %q, want %q", cfg.User, "tokA")
	}
}

func TestCredentialBeforeConnect_CachesAcrossDials(t *testing.T) {
	var calls int
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		calls++
		return []byte(fmt.Sprintf(`{"access_token":"tok-%d","expires_in":300}`, calls)), nil
	})

	hook := credentialBeforeConnect("helper-caches")
	cfg1 := mysql.NewConfig()
	cfg2 := mysql.NewConfig()
	if err := hook(context.Background(), cfg1); err != nil {
		t.Fatalf("first hook: %v", err)
	}
	if err := hook(context.Background(), cfg2); err != nil {
		t.Fatalf("second hook: %v", err)
	}
	if calls != 1 {
		t.Fatalf("helper ran %d times, want 1 (cache hit on second dial)", calls)
	}
	if cfg1.User != cfg2.User || cfg1.User != "tok-1" {
		t.Fatalf("users = %q / %q, want both %q", cfg1.User, cfg2.User, "tok-1")
	}
}

func TestCredentialBeforeConnect_RefreshesExpiredToken(t *testing.T) {
	var calls int
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		calls++
		return []byte(`{"access_token":"tokB","expires_in":300}`), nil
	})

	const cmd = "helper-refreshes"
	credCacheMu.Lock()
	credCache[cmd] = cachedCred{token: "tokOld", expires: time.Now().Add(-time.Minute)}
	credCacheMu.Unlock()

	hook := credentialBeforeConnect(cmd)
	cfg := mysql.NewConfig()
	if err := hook(context.Background(), cfg); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if cfg.User != "tokB" {
		t.Fatalf("cfg.User = %q, want fresh token %q", cfg.User, "tokB")
	}
	if calls != 1 {
		t.Fatalf("helper ran %d times, want 1 (expired entry forces a refresh)", calls)
	}
}

func TestCredentialBeforeConnect_HelperErrorPropagates(t *testing.T) {
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	})

	hook := credentialBeforeConnect("helper-errors")
	cfg := mysql.NewConfig()
	cfg.User = "unchanged"
	err := hook(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected an error when the helper fails (fail-closed)")
	}
	if !strings.Contains(err.Error(), "resolving dolt credential command") {
		t.Fatalf("error = %q, want it to wrap %q", err.Error(), "resolving dolt credential command")
	}
	if cfg.User != "unchanged" {
		t.Fatalf("cfg.User = %q, want it left unchanged on error", cfg.User)
	}
}

func TestOpenSQLDB_EmptyCommandNeverRunsHelper(t *testing.T) {
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		t.Fatal("credRunner must not be invoked when credCmd is empty")
		return nil, nil
	})

	db, err := openSQLDB("root@tcp(127.0.0.1:3307)/beads", "")
	if err != nil {
		t.Fatalf("openSQLDB(validDSN, \"\"): %v", err)
	}
	if db == nil {
		t.Fatal("openSQLDB returned a nil *sql.DB")
	}
	_ = db.Close()
}

func TestOpenSQLDB_InvalidDSNWithCommand(t *testing.T) {
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		t.Fatal("credRunner must not be invoked when the DSN fails to parse")
		return nil, nil
	})

	_, err := openSQLDB("not-a-dsn", "some-cmd")
	if err == nil {
		t.Fatal("expected a parse error for an invalid DSN")
	}
	if !strings.Contains(err.Error(), "parsing DSN for credential connector") {
		t.Fatalf("error = %q, want it to wrap %q", err.Error(), "parsing DSN for credential connector")
	}
}

func TestOpenSQLDB_ConnectorConfiguredIsLazy(t *testing.T) {
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		t.Fatal("credRunner must not run at construction — BeforeConnect is dial-time only")
		return nil, nil
	})

	db, err := openSQLDB("root@tcp(127.0.0.1:3307)/beads", "lazy-cmd")
	if err != nil {
		t.Fatalf("openSQLDB(validDSN, cmd): %v", err)
	}
	if db == nil {
		t.Fatal("openSQLDB returned a nil *sql.DB")
	}
	_ = db.Close()
}

// TestOpenSQLDB_WiresBeforeConnectOnDial is the FIX #2 teeth test: it fails if the
// BeforeConnect hook is not actually attached to the connector openSQLDB builds.
// The mysql connector resolves BeforeConnect BEFORE it dials, so a dial against an
// unroutable host still invokes the credential helper — observing that invocation
// proves the hook is wired. Deleting the cfg.Apply(BeforeConnect(...)) line in
// openSQLDB makes credRunner never run here, and this test then fails (whereas the
// direct credentialBeforeConnect tests above would still pass — the gap this closes).
func TestOpenSQLDB_WiresBeforeConnectOnDial(t *testing.T) {
	var calls int
	stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
		calls++
		return []byte(`{"access_token":"tokWired","expires_in":300}`), nil
	})

	db, err := openSQLDB("root@tcp(127.0.0.1:1)/x", "wire-probe-cmd")
	if err != nil {
		t.Fatalf("openSQLDB: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	conn, err := db.Conn(context.Background())
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected the dial against the unroutable host to fail")
	}
	if calls == 0 {
		t.Fatal("credential helper never ran on dial — openSQLDB did not wire BeforeConnect; FIX #2's per-dial credential is absent")
	}
}
