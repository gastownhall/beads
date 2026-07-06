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

// expectRemoteMaxReread mocks the extra remote content-hash read
// attemptFastForward's TOCTOU re-check performs (remoteMaxAtRef) immediately
// before the fast-forward write, confirming remoteMax is still exactly
// latest — on top of routeSmartGate's own initial read
// (expectSmartRemoteRead). Only expected in tests where the gate actually
// attempts the write (remoteMax == latest and the write is executable).
func expectRemoteMaxReread(mock sqlmock.Sqlmock, remoteName string, remote map[int]string) {
	if remoteName == "" {
		remoteName = smartGateDefaultRemote
	}
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations AS OF 'remotes/` + remoteName + `/main'`).
		WillReturnRows(hashRows(remote))
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
		if gateErr.FallbackReason != fallbackReasonUnreadableState {
			t.Errorf("FallbackReason = %q, want %q", gateErr.FallbackReason, fallbackReasonUnreadableState)
		}
		if !strings.Contains(gateErr.UserMessage(), "could not read the remote's cached schema state") {
			t.Errorf("UserMessage should explain the unreadable-state fallback:\n%s", gateErr.UserMessage())
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
		if gateErr.FallbackReason != fallbackReasonBelowFloor {
			t.Errorf("FallbackReason = %q, want %q", gateErr.FallbackReason, fallbackReasonBelowFloor)
		}
		if !strings.Contains(gateErr.UserMessage(), "below the convergence floor") {
			t.Errorf("UserMessage should explain the below-floor fallback:\n%s", gateErr.UserMessage())
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
		if gateErr.FallbackReason != fallbackReasonUnreadableState {
			t.Errorf("FallbackReason = %q, want %q", gateErr.FallbackReason, fallbackReasonUnreadableState)
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
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.FallbackReason != fallbackReasonUnreadableState {
			t.Errorf("FallbackReason = %q, want %q", gateErr.FallbackReason, fallbackReasonUnreadableState)
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
		if gateErr.FallbackReason != fallbackReasonOptedOut {
			t.Errorf("FallbackReason = %q, want %q", gateErr.FallbackReason, fallbackReasonOptedOut)
		}
		if !strings.Contains(gateErr.UserMessage(), SmartGateEnv+"=0") {
			t.Errorf("UserMessage should explain the opted-out fallback:\n%s", gateErr.UserMessage())
		}
		// mock would error if the smart reads had been issued (none expected).
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations (smart reads must not run when opted out): %v", err)
		}
	})

	t.Run("unparseable BD_SMART_GATE value: gate stays enabled but the blunt block names the parse failure", func(t *testing.T) {
		if floor < 2 {
			t.Skip("floor too low to construct a below-floor case")
		}
		t.Setenv(SmartGateEnv, "banana")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		current := floor - 1
		expectSmartFiringGate(mock, current)
		hashes := map[int]string{current: "h"}
		// Unparseable defaults to enabled, so smart routing still runs (same
		// reads as the below-floor case above).
		expectSmartRemoteRead(mock, hashes, hashes)

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != "" {
			t.Errorf("unparseable env should still fall back to the blunt block (Decision \"\"), got %q", gateErr.Decision)
		}
		if gateErr.FallbackReason != fallbackReasonUnparseableEnv {
			t.Errorf("FallbackReason = %q, want %q (takes priority over the underlying technical routing reason)", gateErr.FallbackReason, fallbackReasonUnparseableEnv)
		}
		if gateErr.UnrecognizedSmartGateEnv != "banana" {
			t.Errorf("UnrecognizedSmartGateEnv = %q, want %q", gateErr.UnrecognizedSmartGateEnv, "banana")
		}
		msg := gateErr.UserMessage()
		if !strings.Contains(msg, SmartGateEnv+"=banana") || !strings.Contains(msg, "was not recognized") {
			t.Errorf("UserMessage should quote the unparseable value:\n%s", msg)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
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

// fakeAdopter builds a *FastForwardAdopter from canned bool/error results and
// counts how many times each callback is invoked, so tests can assert the
// router short-circuits (never calling WorkingSetClean once ancestry has
// already failed) and never calls a callback with a nil adopt.
type fakeAdopter struct {
	ancestorResult bool
	ancestorErr    error
	ancestorCalls  int

	cleanResult bool
	cleanErr    error
	cleanCalls  int

	// ffErr is returned by FastForward; ffCalls counts invocations. Only
	// wired into the adopter (adopter()) when withFastForward is true —
	// omitting it entirely (the zero value) models an injection site that
	// never wired write execution (piece 3: canAutoFastForward treats a nil
	// FastForward exactly like ReadOnly).
	withFastForward bool
	ffErr           error
	ffCalls         int

	readOnly bool
}

func (f *fakeAdopter) adopter() *FastForwardAdopter {
	a := &FastForwardAdopter{
		IsStrictAncestor: func(_ context.Context, _ DBConn, _ string) (bool, error) {
			f.ancestorCalls++
			return f.ancestorResult, f.ancestorErr
		},
		WorkingSetClean: func(_ context.Context, _ DBConn) (bool, error) {
			f.cleanCalls++
			return f.cleanResult, f.cleanErr
		},
		ReadOnly: f.readOnly,
	}
	if f.withFastForward {
		a.FastForward = func(_ context.Context, _ DBConn, _ string) error {
			f.ffCalls++
			return f.ffErr
		}
	}
	return a
}

// TestSmartGateRoutingFastForward covers the smartAdoptFastForward refinement
// (mybd-ae1i piece 2 detection, piece 3 action, follow-up: gated on landing
// exactly at latest): every input that previously reached smartAdopt still
// does whenever the injected ancestry callbacks are absent, unavailable, or
// fail their precondition. The auto fast-forward itself only EXECUTES once
// ancestry+clean succeed AND remoteMax == latest AND the write is executable
// AND the TOCTOU re-check immediately before the write agrees; every other
// disqualified-but-otherwise-eligible case (read-only, unwired FastForward,
// or remoteMax != latest) still surfaces the accurate loss-free adopt-ff
// directive instead of the generic destructive-adopt text.
func TestSmartGateRoutingFastForward(t *testing.T) {
	floor := LastNonDeterministicMigration
	latest := LatestVersion()

	t.Run("auto fast-forward: remote ahead, no skew, strict ancestor, clean working set, remote at latest", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", latest: "h2"}
		expectSmartRemoteRead(mock, local, remote)
		expectRemoteMaxReread(mock, "", remote) // attemptFastForward's TOCTOU re-check

		fa := &fakeAdopter{ancestorResult: true, cleanResult: true, withFastForward: true}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		if err != nil {
			t.Fatalf("auto fast-forward should succeed silently (nil error), got %v", err)
		}
		// The read-side preconditions are checked twice by design: once to
		// pick the verdict (routeSmartGate), once more as the TOCTOU
		// re-check immediately before the write (attemptFastForward). The
		// write itself (FastForward) must run exactly once.
		if fa.ancestorCalls != 2 {
			t.Errorf("IsStrictAncestor calls = %d, want 2 (initial verdict + TOCTOU re-check)", fa.ancestorCalls)
		}
		if fa.cleanCalls != 2 {
			t.Errorf("WorkingSetClean calls = %d, want 2 (initial verdict + TOCTOU re-check)", fa.cleanCalls)
		}
		if fa.ffCalls != 1 {
			t.Errorf("FastForward calls = %d, want 1", fa.ffCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("adopt-ff directive, no execution: remote ahead of latest by one below the binary's latest", func(t *testing.T) {
		if floor+1 >= latest {
			t.Skip("floor+1 is not below latest in this build; cannot construct a remoteMax < latest fixture")
		}
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		// remoteMax = floor+1, strictly between current (floor) and latest:
		// blocker 1 (fork risk) — landing here would leave MigrateUp to
		// apply floor+2..latest unconditionally in place afterward with no
		// gate re-evaluation, so the fast-forward must never execute even
		// though ancestor+clean hold.
		remote := map[int]string{floor: "h1", floor + 1: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanResult: true, withFastForward: true}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdoptFastForward {
			t.Errorf("Decision = %q, want %q (remoteMax < latest must never auto-execute, but is still a loss-free adopt candidate)", gateErr.Decision, gateDecisionAdoptFastForward)
		}
		if fa.ffCalls != 0 {
			t.Errorf("FastForward calls = %d, want 0 (must never execute when remoteMax != latest)", fa.ffCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("adopt-ff directive, no execution: remote ahead of the binary's own latest (newer-binary migrated)", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		// remoteMax = latest+1: blocker 2 (forward drift) — a newer binary
		// already migrated the remote further than this one understands.
		// Fast-forwarding there would silently land HEAD past what this
		// binary supports; must never auto-execute.
		remote := map[int]string{floor: "h1", latest + 1: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanResult: true, withFastForward: true}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdoptFastForward {
			t.Errorf("Decision = %q, want %q (remoteMax > latest must never auto-execute, but is still a loss-free adopt candidate)", gateErr.Decision, gateDecisionAdoptFastForward)
		}
		if fa.ffCalls != 0 {
			t.Errorf("FastForward calls = %d, want 0 (must never execute when remoteMax != latest)", fa.ffCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("adopt-ff directive (not plain adopt): read-only store must never invoke FastForward", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", latest: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanResult: true, withFastForward: true, readOnly: true}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdoptFastForward {
			t.Errorf("Decision = %q, want %q (read-only is disqualified-but-otherwise-eligible: still loss-free, just cannot execute)", gateErr.Decision, gateDecisionAdoptFastForward)
		}
		if fa.ffCalls != 0 {
			t.Errorf("FastForward calls = %d, want 0 (must never attempt a write on a read-only store)", fa.ffCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("adopt-ff directive (not plain adopt): no FastForward wired (detection-only injection site) must never invoke it", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", latest: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanResult: true} // withFastForward left false
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdoptFastForward {
			t.Errorf("Decision = %q, want %q (no FastForward wired is disqualified-but-otherwise-eligible: still loss-free, just cannot execute)", gateErr.Decision, gateDecisionAdoptFastForward)
		}
		if fa.ffCalls != 0 {
			t.Errorf("FastForward calls = %d, want 0", fa.ffCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("plain adopt: FastForward failure (TOCTOU-raced merge refusal) degrades, never forces", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", latest: "h2"}
		expectSmartRemoteRead(mock, local, remote)
		expectRemoteMaxReread(mock, "", remote) // attemptFastForward's TOCTOU re-check

		fa := &fakeAdopter{
			ancestorResult: true, cleanResult: true,
			withFastForward: true, ffErr: errors.New("dolt_merge: not a fast-forward"),
		}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdopt {
			t.Errorf("Decision = %q, want %q (a failed fast-forward attempt must degrade to the plain destructive adopt — the guarantee no longer holds confidently, unlike the disqualified-before-attempting cases above)", gateErr.Decision, gateDecisionAdopt)
		}
		if fa.ffCalls != 1 {
			t.Errorf("FastForward calls = %d, want 1 (it was attempted, just failed)", fa.ffCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("plain adopt: remote ahead, no skew, dirty working set degrades from adopt-ff", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", floor + 1: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanResult: false}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdopt {
			t.Errorf("Decision = %q, want %q (a dirty working set must fall back to the plain destructive adopt)", gateErr.Decision, gateDecisionAdopt)
		}
		if fa.ancestorCalls != 1 || fa.cleanCalls != 1 {
			t.Errorf("expected both callbacks invoked exactly once, got ancestor=%d clean=%d", fa.ancestorCalls, fa.cleanCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("plain adopt: not a strict ancestor short-circuits before the working-set check", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", floor + 1: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: false, cleanResult: true}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdopt {
			t.Errorf("Decision = %q, want %q (a non-ancestor must fall back to the plain adopt)", gateErr.Decision, gateDecisionAdopt)
		}
		if fa.ancestorCalls != 1 {
			t.Errorf("IsStrictAncestor calls = %d, want 1", fa.ancestorCalls)
		}
		if fa.cleanCalls != 0 {
			t.Errorf("WorkingSetClean calls = %d, want 0 (must not run once ancestry already failed)", fa.cleanCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("plain adopt: ancestry callback error is treated as not fast-forwardable", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", floor + 1: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorErr: errors.New("dolt_log AS OF: branch not found")}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdopt {
			t.Errorf("Decision = %q, want %q (an inconclusive ancestry read must never promote past the plain adopt)", gateErr.Decision, gateDecisionAdopt)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("plain adopt: working-set callback error is treated as not fast-forwardable", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", floor + 1: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanErr: errors.New("query dolt_status: table not found")}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdopt {
			t.Errorf("Decision = %q, want %q (an inconclusive working-set read must never promote past the plain adopt)", gateErr.Decision, gateDecisionAdopt)
		}
		if fa.ancestorCalls != 1 || fa.cleanCalls != 1 {
			t.Errorf("expected both callbacks invoked exactly once, got ancestor=%d clean=%d", fa.ancestorCalls, fa.cleanCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("plain adopt: nil adopt callbacks behave exactly like the pre-piece-2 gate", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		remote := map[int]string{floor: "h1", floor + 1: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		// No adopter wired at all (nil) — the injection site is not updated,
		// or ancestry checks are unavailable (e.g. a read-only open). Fallback
		// safety: identical to CheckRemoteMigrateGate before piece 2 existed.
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, nil)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdopt {
			t.Errorf("Decision = %q, want %q", gateErr.Decision, gateDecisionAdopt)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("fork-skew is unaffected by adopt callbacks: skew short-circuits before the ancestry check", func(t *testing.T) {
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "local-hash"}
		remote := map[int]string{floor: "remote-hash"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanResult: true}
		err := CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected gate error, got %v", err)
		}
		if gateErr.Decision != gateDecisionForkSkew {
			t.Errorf("Decision = %q, want %q (skew must never reach the fast-forward refinement)", gateErr.Decision, gateDecisionForkSkew)
		}
		if fa.ancestorCalls != 0 || fa.cleanCalls != 0 {
			t.Errorf("adopt callbacks must not run once skew is detected, got ancestor=%d clean=%d", fa.ancestorCalls, fa.cleanCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
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
