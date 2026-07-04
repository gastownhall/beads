package schema

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectSmartFiringGate mocks the blunt-gate probe sequence (CurrentVersion,
// PendingVersions, dolt_remotes) for a behind, remote-backed database at the
// given current version — the state in which the smart router runs.
func expectSmartFiringGate(mock sqlmock.Sqlmock, current int) {
	expectGateCurrentVersion(mock, current) // CurrentVersion
	expectGateCurrentVersion(mock, current) // PendingVersions -> pending exists
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
}

func hashRows(hashes map[int]string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"version", "content_hash"})
	for v, h := range hashes {
		rows.AddRow(v, h)
	}
	return rows
}

// expectSmartRemoteRead mocks the three reads the smart router issues: local
// content hashes (HEAD), active_branch(), and remote content hashes (AS OF).
func expectSmartRemoteReadForRemote(mock sqlmock.Sqlmock, remoteName string, local, remote map[int]string) {
	if remoteName == "" {
		remoteName = smartGateDefaultRemote
	}
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(hashRows(local))
	mock.ExpectQuery(`SELECT active_branch\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"active_branch()"}).AddRow("main"))
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations AS OF 'remotes/` + remoteName + `/main'`).
		WillReturnRows(hashRows(remote))
}

// expectSmartRemoteRead mocks the three reads the smart router issues using the
// default remote: local content hashes (HEAD), active_branch(), and remote
// content hashes (AS OF remotes/origin/main).
func expectSmartRemoteRead(mock sqlmock.Sqlmock, local, remote map[int]string) {
	expectSmartRemoteReadForRemote(mock, "", local, remote)
}

func TestSmartGateRouting(t *testing.T) {
	latest := LatestVersion()
	floor := LastNonDeterministicMigration

	t.Run("auto-migrate: remote == local, at/above floor, no skew → allowed", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		hashes := map[int]string{floor - 1: "h1", floor: "h2"}
		expectSmartRemoteRead(mock, hashes, hashes)

		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("safe first-mover should be allowed to migrate, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("adopt: remote ahead, no skew → adopt decision", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor - 1: "h1", floor: "h2"}
		remote := map[int]string{floor - 1: "h1", floor: "h2", floor + 1: "h3"}
		expectSmartRemoteRead(mock, local, remote)

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdopt {
			t.Errorf("Decision = %q, want %q", gateErr.Decision, gateDecisionAdopt)
		}
		// Adopt must not offer the migrate escape command anywhere.
		for _, o := range gateErr.Options() {
			for _, c := range o.Commands {
				if strings.Contains(c, AllowRemoteMigrateEnv) {
					t.Errorf("adopt decision must not surface the migrate escape command, got %q", c)
				}
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("remote behind local: not a first-mover → blunt block", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		current := floor + 1
		expectSmartFiringGate(mock, current)
		local := map[int]string{floor: "h1", current: "h2"}
		remote := map[int]string{floor: "h1"}
		expectSmartRemoteRead(mock, local, remote)

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != "" {
			t.Errorf("remote-behind should fall back to the blunt block (Decision \"\"), got %q", gateErr.Decision)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("uses configured remote for smart read", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		hashes := map[int]string{floor: "h"}
		expectSmartRemoteReadForRemote(mock, "upstream", hashes, hashes)

		if err := CheckRemoteMigrateGateForRemoteWithRemoteCheck(context.Background(), db, "upstream", nil); err != nil {
			t.Fatalf("safe first-mover on configured remote should be allowed, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("fork-skew: divergent content for a shared version → fork-skew decision", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "local-hash"}
		remote := map[int]string{floor: "remote-hash"}
		expectSmartRemoteRead(mock, local, remote)

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionForkSkew {
			t.Errorf("Decision = %q, want %q", gateErr.Decision, gateDecisionForkSkew)
		}
		if len(gateErr.SkewVersions) != 1 || gateErr.SkewVersions[0] != floor {
			t.Errorf("SkewVersions = %v, want [%d]", gateErr.SkewVersions, floor)
		}
		if !strings.Contains(gateErr.UserMessage(), "forked") {
			t.Errorf("fork-skew UserMessage should explain the fork:\n%s", gateErr.UserMessage())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("below floor: non-deterministic migration still pending → blunt block", func(t *testing.T) {
		if floor < 2 {
			t.Skip("floor too low to construct a below-floor case")
		}
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		current := floor - 1
		expectSmartFiringGate(mock, current)
		hashes := map[int]string{current: "h"}
		expectSmartRemoteRead(mock, hashes, hashes)

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != "" {
			t.Errorf("below-floor should fall back to the blunt block (Decision \"\"), got %q", gateErr.Decision)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("undetermined: no cached remote ref → blunt block", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
			WillReturnRows(hashRows(map[int]string{floor: "h"}))
		mock.ExpectQuery(`SELECT active_branch\(\)`).
			WillReturnRows(sqlmock.NewRows([]string{"active_branch()"}).AddRow("main"))
		mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations AS OF 'remotes/origin/main'`).
			WillReturnError(errors.New("branch not found: remotes/origin/main"))

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != "" {
			t.Errorf("uncached remote ref should fall back to the blunt block, got Decision %q", gateErr.Decision)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("undetermined: no local hashes → blunt block (remote not read)", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
			WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}))

		err := CheckRemoteMigrateGate(context.Background(), db)
		if !IsRemoteMigrateGateError(err) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("escape hatch still wins over smart routing", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "1")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		// Escape hatch returns before any smart read, so only the blunt probes
		// are expected.
		expectSmartFiringGate(mock, floor)

		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("escape hatch should allow migration without smart reads, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("smart opted out (BD_SMART_GATE=0): no extra reads, blunt block", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "0")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != "" {
			t.Errorf("smart disabled must produce the blunt block, got Decision %q", gateErr.Decision)
		}
		// mock would error if the smart reads had been issued (none expected).
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations (smart reads must not run when opted out): %v", err)
		}
	})

	t.Run("default (env unset): smart routing runs and resolves the safe first-mover", func(t *testing.T) {
		// t.Setenv registers restoration of any inherited value; then clear it
		// so this subtest sees the true unset default.
		t.Setenv(SmartGateEnv, "")
		os.Unsetenv(SmartGateEnv)
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		hashes := map[int]string{floor: "aaaa"}
		expectSmartRemoteRead(mock, hashes, hashes)

		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("smart gate must be on by default and allow the safe first-mover, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations (smart reads must run by default): %v", err)
		}
	})

	_ = latest
}

// TestSmartGateEnabled pins the default-on contract: unset and unparseable
// values keep the smart gate active; only an explicit boolean false opts out.
func TestSmartGateEnabled(t *testing.T) {
	cases := []struct {
		value string
		unset bool
		want  bool
	}{
		{unset: true, want: true},
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "FALSE", want: false},
		{value: "banana", want: true}, // unparseable keeps the default, never silently disables
	}
	for _, c := range cases {
		name := c.value
		if c.unset {
			name = "(unset)"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(SmartGateEnv, c.value)
			if c.unset {
				os.Unsetenv(SmartGateEnv)
			}
			if got := SmartGateEnabled(); got != c.want {
				t.Errorf("SmartGateEnabled() with %s = %v, want %v", name, got, c.want)
			}
		})
	}
}

// TestConvergenceFloorMatchesAllowlist cross-checks LastNonDeterministicMigration
// against the nondeterminism allowlist so the constant cannot silently drift if a
// new genuinely-non-deterministic migration is ever grandfathered in. A new
// allowlist entry above the floor forces a conscious update here.
func TestConvergenceFloorMatchesAllowlist(t *testing.T) {
	// Allowlist entries whose nondeterminism is genuinely clone-safe (evaluated
	// at query time, or on never-replicated tables), so they do NOT raise the
	// convergence floor. Keep justifications in sync with the allowlist file.
	knownSafe := map[int]bool{
		17: true, // 0017: NOW() inside a VIEW body — query-time, identical per clone
	}

	f, err := os.Open("migrations/nondeterminism-allowlist.txt")
	if err != nil {
		t.Fatalf("open allowlist: %v", err)
	}
	defer f.Close()

	maxUnsafe := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry := strings.Fields(line)[0]
		if strings.HasPrefix(entry, "ignored/") {
			continue // dolt-ignored local-only tables are never replicated
		}
		// entry looks like "0043_drop_dependencies_generated_column.up.sql"
		numStr := entry
		if i := strings.IndexByte(entry, '_'); i > 0 {
			numStr = entry[:i]
		}
		v, err := strconv.Atoi(numStr)
		if err != nil {
			t.Fatalf("could not parse version from allowlist entry %q: %v", entry, err)
		}
		if knownSafe[v] {
			continue
		}
		if v > maxUnsafe {
			maxUnsafe = v
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan allowlist: %v", err)
	}

	if maxUnsafe == 0 {
		t.Fatal("found no genuinely-non-deterministic allowlist entries; allowlist parsing likely broke")
	}
	if LastNonDeterministicMigration != maxUnsafe {
		t.Errorf("LastNonDeterministicMigration = %d, but the highest non-safe allowlist entry is %d.\n"+
			"If a new non-deterministic migration was grandfathered in, update the convergence floor "+
			"(and confirm its divergence is healed before auto-migrate trusts it), or add it to knownSafe with a justification.",
			LastNonDeterministicMigration, maxUnsafe)
	}
}
