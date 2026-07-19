package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "driver bad connection",
			err:      errors.New("driver: bad connection"),
			expected: true,
		},
		{
			name:     "Driver Bad Connection (case insensitive)",
			err:      errors.New("Driver: Bad Connection"),
			expected: true,
		},
		{
			name:     "invalid connection",
			err:      errors.New("invalid connection"),
			expected: true,
		},
		{
			name:     "broken pipe",
			err:      errors.New("write: broken pipe"),
			expected: true,
		},
		{
			name:     "connection reset",
			err:      errors.New("read: connection reset by peer"),
			expected: true,
		},
		{
			name:     "connection refused - retryable (server restart)",
			err:      errors.New("dial tcp: connection refused"),
			expected: true,
		},
		{
			name:     "database is read only - retryable",
			err:      errors.New("cannot update manifest: database is read only"),
			expected: true,
		},
		{
			name:     "Database Is Read Only (case insensitive)",
			err:      errors.New("Database Is Read Only"),
			expected: true,
		},
		{
			name:     "lost connection - retryable (MySQL error 2013)",
			err:      errors.New("Error 2013: Lost connection to MySQL server during query"),
			expected: true,
		},
		{
			name:     "server gone away - retryable (MySQL error 2006)",
			err:      errors.New("Error 2006: MySQL server has gone away"),
			expected: true,
		},
		{
			name:     "i/o timeout - retryable",
			err:      errors.New("read tcp 127.0.0.1:3307: i/o timeout"),
			expected: true,
		},
		{
			name:     "unknown database - retryable (catalog race GH-1851)",
			err:      errors.New("Error 1049 (42000): Unknown database 'beads_test'"),
			expected: true,
		},
		{
			name:     "Unknown Database (case insensitive)",
			err:      errors.New("Unknown Database 'beads_test'"),
			expected: true,
		},
		{
			name:     "no root value found in session",
			err:      errors.New("Error 1105 (HY000): no root value found in session"),
			expected: true,
		},
		{
			name:     "syntax error - not retryable",
			err:      errors.New("Error 1064: You have an error in your SQL syntax"),
			expected: false,
		},
		{
			name:     "table not found - not retryable",
			err:      errors.New("Error 1146: Table 'beads.foo' doesn't exist"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestWithRetry_Success(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call on success, got %d", callCount)
	}
}

func TestWithRetry_RetryOnBadConnection(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("driver: bad connection")
		}
		return nil // Success on 3rd attempt
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + success), got %d", callCount)
	}
}

func TestWithRetry_RetryOnUnknownDatabase(t *testing.T) {
	// Simulates the GH-1851 race: "Unknown database" is transient after CREATE DATABASE
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("Error 1049 (42000): Unknown database 'beads_test'")
		}
		return nil // Catalog caught up on 3rd attempt
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + success), got %d", callCount)
	}
}

func TestWithRetry_NonRetryableError(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		return errors.New("syntax error in SQL")
	})

	if err == nil {
		t.Error("expected error, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
}

// TestWithRetryTx_AuthErrorInvalidatesAndRetries is the write-path analogue of
// withRetry's credential recovery: a MySQL 1045 from a write transaction's dial
// (a rotating token revoked before its cached expiry) must drop the cached token
// and retry, not fail permanently. Before withRetryTx learned this, it
// classified 1045 as non-retryable and surfaced it, so hosted-credential writes
// could fail for the whole life of a stale-but-unexpired cache entry.
func TestWithRetryTx_AuthErrorInvalidatesAndRetries(t *testing.T) {
	const cmd = "rotating-helper"
	credCacheMu.Lock()
	credCache = map[string]cachedCred{cmd: {token: "revoked", expires: time.Now().Add(time.Hour)}}
	credCacheMu.Unlock()
	t.Cleanup(func() {
		credCacheMu.Lock()
		credCache = map[string]cachedCred{}
		credCacheMu.Unlock()
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// First dial rejects the revoked token; the retry's dial succeeds and commits.
	mock.ExpectBegin().WillReturnError(&mysql.MySQLError{Number: 1045, Message: "Access denied for user"})
	mock.ExpectBegin()
	mock.ExpectCommit()

	store := &DoltStore{db: db, credCommand: cmd, serverMode: true}

	bodyRuns := 0
	if err := store.withRetryTx(context.Background(), func(tx *sql.Tx) error {
		bodyRuns++
		return nil
	}); err != nil {
		t.Fatalf("withRetryTx must retry past the auth rejection, got: %v", err)
	}
	if bodyRuns != 1 {
		t.Fatalf("tx body should run once (only after the successful retry), ran %d times", bodyRuns)
	}

	credCacheMu.Lock()
	_, stillCached := credCache[cmd]
	credCacheMu.Unlock()
	if stillCached {
		t.Fatal("auth rejection must invalidate the cached credential so the retry re-mints")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// TestWithRetryTx_CommitPhaseAuthErrorNotRetried guards the double-apply
// invariant: an auth rejection observed during tx.Commit is ambiguous (the
// commit may have landed), so — exactly like a commit-phase connection loss — it
// must surface permanently rather than replay the write, even though the cached
// token is still dropped so future dials re-mint.
func TestWithRetryTx_CommitPhaseAuthErrorNotRetried(t *testing.T) {
	const cmd = "rotating-helper"
	credCacheMu.Lock()
	credCache = map[string]cachedCred{cmd: {token: "revoked", expires: time.Now().Add(time.Hour)}}
	credCacheMu.Unlock()
	t.Cleanup(func() {
		credCacheMu.Lock()
		credCache = map[string]cachedCred{}
		credCacheMu.Unlock()
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectCommit().WillReturnError(&mysql.MySQLError{Number: 1045, Message: "Access denied for user"})

	store := &DoltStore{db: db, credCommand: cmd, serverMode: true}

	bodyRuns := 0
	err = store.withRetryTx(context.Background(), func(tx *sql.Tx) error {
		bodyRuns++
		return nil
	})
	if err == nil {
		t.Fatal("a commit-phase auth rejection must surface, not be silently retried")
	}
	if !errors.Is(err, errCommitPhase) {
		t.Fatalf("commit-phase failure must stay tagged errCommitPhase, got: %v", err)
	}
	if bodyRuns != 1 {
		t.Fatalf("commit-phase failure must not replay the write; body ran %d times", bodyRuns)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// TestExecWithLongTimeout_RevokedTokenRecovers proves the long-timeout side pool —
// which dials outside withRetry/withRetryTx — recovers from a rotating credential
// revoked before its cached expiry. The first dial's MySQL 1045 must invalidate the
// cached token so the re-dial re-mints, and the wrapped pull operation must then run
// exactly once: the auth retry recovers the dial without replaying committed work.
// The migration and pull side pools (openMigrationDB, openLongTimeoutConn) share the
// same primeCredentialedConn path.
func TestExecWithLongTimeout_RevokedTokenRecovers(t *testing.T) {
	const cmd = "rotating-helper"
	credCacheMu.Lock()
	credCache = map[string]cachedCred{cmd: {token: "revoked", expires: time.Now().Add(time.Hour)}}
	credCacheMu.Unlock()
	t.Cleanup(func() {
		credCacheMu.Lock()
		credCache = map[string]cachedCred{}
		credCacheMu.Unlock()
	})

	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	origOpener := sideConnOpener
	sideConnOpener = func(dsn, credCmd string) (*sql.DB, error) { return db, nil }
	t.Cleanup(func() { sideConnOpener = origOpener })

	// Prime dials first: the revoked token is rejected (1045), then the re-minted
	// dial succeeds. Only afterward does the single pull operation run — once.
	mock.ExpectPing().WillReturnError(&mysql.MySQLError{Number: 1045, Message: "Access denied for user"})
	mock.ExpectPing()
	mock.ExpectBegin()
	mock.ExpectExec("CALL DOLT_PULL").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	store := &DoltStore{connStr: "user:pass@tcp(127.0.0.1:3306)/beads", credCommand: cmd}
	if err := store.execWithLongTimeout(context.Background(), "CALL DOLT_PULL(?, ?)", "origin", "main"); err != nil {
		t.Fatalf("execWithLongTimeout must recover past the revoked-token dial, got: %v", err)
	}

	credCacheMu.Lock()
	_, stillCached := credCache[cmd]
	credCacheMu.Unlock()
	if stillCached {
		t.Fatal("revoked-token dial must invalidate the cached credential so the retry re-mints")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// TestPrimeCredentialedConn_StaticPathIsNoOp verifies the static-user/local path
// (empty credCommand) is left byte-for-byte unchanged: priming adds no dial.
func TestPrimeCredentialedConn_StaticPathIsNoOp(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// No ping is expected; a stray dial would surface here and fail the check.
	store := &DoltStore{} // credCommand == ""
	if err := store.primeCredentialedConn(context.Background(), db); err != nil {
		t.Fatalf("static path must be a no-op, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("static path must not dial: %v", err)
	}
}

// TestPrimeCredentialedConn_NonAuthErrorNotRetried verifies a non-auth dial error
// surfaces immediately without dropping the cached credential or re-dialing — only a
// MySQL 1045 triggers the invalidate-and-retry recovery.
func TestPrimeCredentialedConn_NonAuthErrorNotRetried(t *testing.T) {
	const cmd = "rotating-helper"
	credCacheMu.Lock()
	credCache = map[string]cachedCred{cmd: {token: "live", expires: time.Now().Add(time.Hour)}}
	credCacheMu.Unlock()
	t.Cleanup(func() {
		credCacheMu.Lock()
		credCache = map[string]cachedCred{}
		credCacheMu.Unlock()
	})

	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectPing().WillReturnError(errors.New("driver: bad connection"))

	store := &DoltStore{credCommand: cmd}
	if err := store.primeCredentialedConn(context.Background(), db); err == nil {
		t.Fatal("a non-auth dial error must surface")
	}
	credCacheMu.Lock()
	_, stillCached := credCache[cmd]
	credCacheMu.Unlock()
	if !stillCached {
		t.Fatal("a non-auth error must not invalidate the cached credential")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("must not re-dial after a non-auth error: %v", err)
	}
}

// TestRetryStaleCredentialOpen covers the store-open credential recovery primitive
// used by openServerConnection's first dials (SHOW DATABASES, CREATE DATABASE, and the
// test-connection ping), which run before the DoltStore's own withRetry/withRetryTx/
// primeCredentialedConn handlers exist. On the credential-command path a MySQL 1045
// must drop the cached token and retry the op exactly once; the static path and
// non-auth errors must run the op once and leave the cache untouched.
func TestRetryStaleCredentialOpen(t *testing.T) {
	authErr := &mysql.MySQLError{Number: 1045, Message: "Access denied for user"}

	t.Run("static path runs once and never invalidates", func(t *testing.T) {
		runs := 0
		err := retryStaleCredentialOpen("", func() error {
			runs++
			return authErr
		})
		if !errors.Is(err, authErr) {
			t.Fatalf("static path must surface the error, got: %v", err)
		}
		if runs != 1 {
			t.Fatalf("static path must run op exactly once, ran %d", runs)
		}
	})

	t.Run("auth error invalidates cached token and retries once", func(t *testing.T) {
		const cmd = "rotating-helper"
		credCacheMu.Lock()
		credCache = map[string]cachedCred{cmd: {token: "revoked", expires: time.Now().Add(time.Hour)}}
		credCacheMu.Unlock()
		t.Cleanup(func() {
			credCacheMu.Lock()
			credCache = map[string]cachedCred{}
			credCacheMu.Unlock()
		})

		runs := 0
		err := retryStaleCredentialOpen(cmd, func() error {
			runs++
			if runs == 1 {
				return authErr
			}
			return nil
		})
		if err != nil {
			t.Fatalf("must recover on the retry, got: %v", err)
		}
		if runs != 2 {
			t.Fatalf("auth error must retry the op exactly once (2 runs), ran %d", runs)
		}
		credCacheMu.Lock()
		_, stillCached := credCache[cmd]
		credCacheMu.Unlock()
		if stillCached {
			t.Fatal("auth rejection must invalidate the cached credential so the retry re-mints")
		}
	})

	t.Run("non-auth error runs once and keeps the cache", func(t *testing.T) {
		const cmd = "rotating-helper"
		credCacheMu.Lock()
		credCache = map[string]cachedCred{cmd: {token: "live", expires: time.Now().Add(time.Hour)}}
		credCacheMu.Unlock()
		t.Cleanup(func() {
			credCacheMu.Lock()
			credCache = map[string]cachedCred{}
			credCacheMu.Unlock()
		})

		runs := 0
		wantErr := errors.New("driver: bad connection")
		err := retryStaleCredentialOpen(cmd, func() error {
			runs++
			return wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("non-auth error must surface, got: %v", err)
		}
		if runs != 1 {
			t.Fatalf("non-auth error must not retry; ran %d", runs)
		}
		credCacheMu.Lock()
		_, stillCached := credCache[cmd]
		credCacheMu.Unlock()
		if !stillCached {
			t.Fatal("a non-auth error must not invalidate the cached credential")
		}
	})
}

// TestOpenServerConnection_StaleCredentialRecovers proves store construction itself
// recovers from a rotating credential revoked before its cached expiry — the exact gap
// the DoltStore's retry handlers cannot cover because they do not exist yet during open.
// openServerConnection dials two independent pools that share the process credential
// cache: initDB (no database in the DSN) runs SHOW DATABASES, and db (database in the
// DSN) runs the open-time catalog ping. With a stale cached token, each pool's first
// dial is rejected with MySQL 1045; store-open must drop the token and re-dial rather
// than fail permanently with "failed to check if database" or a ping error.
func TestOpenServerConnection_StaleCredentialRecovers(t *testing.T) {
	const cmd = "rotating-helper"
	credCacheMu.Lock()
	credCache = map[string]cachedCred{cmd: {token: "revoked", expires: time.Now().Add(time.Hour)}}
	credCacheMu.Unlock()
	t.Cleanup(func() {
		credCacheMu.Lock()
		credCache = map[string]cachedCred{}
		credCacheMu.Unlock()
	})

	mainDB, mainMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New (main): %v", err)
	}
	t.Cleanup(func() { _ = mainDB.Close() })
	initDB, initMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New (init): %v", err)
	}
	t.Cleanup(func() { _ = initDB.Close() })

	// Route the main vs init pool by the DSN's database name, exactly as
	// openServerConnection builds them (buildServerDSN(cfg, cfg.Database) vs "").
	origOpener := serverConnOpener
	serverConnOpener = func(dsn, credCmd string) (*sql.DB, error) {
		parsed, perr := mysql.ParseDSN(dsn)
		if perr != nil {
			t.Fatalf("unexpected DSN %q: %v", dsn, perr)
		}
		if parsed.DBName == "" {
			return initDB, nil
		}
		return mainDB, nil
	}
	t.Cleanup(func() { serverConnOpener = origOpener })

	authErr := &mysql.MySQLError{Number: 1045, Message: "Access denied for user"}
	// Each pool's first dial presents the revoked token and is rejected; the
	// invalidate-and-re-dial retry then succeeds.
	initMock.ExpectQuery("SHOW DATABASES").WillReturnError(authErr)
	initMock.ExpectQuery("SHOW DATABASES").WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow("beads"))
	mainMock.ExpectPing().WillReturnError(authErr)
	mainMock.ExpectPing()

	cfg := &Config{
		Database:          "beads",
		ServerHost:        "127.0.0.1",
		ServerPort:        3999,
		ServerUser:        "placeholder",
		CredentialCommand: cmd,
		CreateIfMissing:   true,
	}
	db, _, err := openServerConnection(context.Background(), cfg)
	if err != nil {
		t.Fatalf("store-open must recover past the revoked-token dials, got: %v", err)
	}
	if db == nil {
		t.Fatal("store-open must return a live connection on recovery")
	}

	credCacheMu.Lock()
	_, stillCached := credCache[cmd]
	credCacheMu.Unlock()
	if stillCached {
		t.Fatal("store-open auth rejection must invalidate the cached credential so the re-dial re-mints")
	}
	if err := initMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet init-pool expectations: %v", err)
	}
	if err := mainMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet main-pool expectations: %v", err)
	}
}
