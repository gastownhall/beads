package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/cenkalti/backoff/v4"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestInitSchemaOnDBWithRetryAndGate_GateErrorClassification verifies the gate
// runs INSIDE the retry loop (bd-6dnrw.30): transient probe failures (a freshly
// auto-started server still warming its catalog) are retried like the migration
// itself, while a gate refusal and unexpected probe errors are permanent. No
// Dolt server is needed: the gate fails before the database is ever touched,
// and a refusal's re-read under the migration lock (lockedGateRecheck) cannot
// reach a session here, so the first refusal stands. The locked re-read itself
// is pinned against real Dolt by
// TestDoltNew_SharedStoreMigrateGate_RefusalWaitsOutInFlightPass.
func TestInitSchemaOnDBWithRetryAndGate_GateErrorClassification(t *testing.T) {
	ctx := context.Background()
	// Never dialed: every gate below errors, so initSchemaOnDB never runs.
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:1)/never_dialed")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	t.Run("retryable probe error is retried until the gate answers", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, schema.DBConn) error {
			calls++
			if calls < 3 {
				return fmt.Errorf("remote-migrate gate: read current version: %w",
					errors.New("driver: bad connection"))
			}
			return &schema.RemoteMigrateGateError{CurrentVersion: 1, LatestVersion: 2, Pending: 1}
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if !schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError after retries", err, err)
		}
		if calls != 3 {
			t.Fatalf("gate calls = %d, want 3 (two transient failures retried, then refusal)", calls)
		}
	})

	for _, tc := range []struct {
		name string
		err  *mysql.MySQLError
	}{
		{
			name: "typed unknown database",
			err:  &mysql.MySQLError{Number: 1049, Message: "Unknown database 'beads_test'"},
		},
		{
			name: "typed missing session root",
			err:  &mysql.MySQLError{Number: 1105, Message: "no root value found in session"},
		},
	} {
		t.Run(tc.name+" is retried during setup", func(t *testing.T) {
			calls := 0
			gate := func(context.Context, schema.DBConn) error {
				calls++
				if calls < 3 {
					return fmt.Errorf("remote-migrate gate: probe schema: %w", tc.err)
				}
				return &schema.RemoteMigrateGateError{CurrentVersion: 1, LatestVersion: 2, Pending: 1}
			}
			_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
			if !schema.IsRemoteMigrateGateError(err) {
				t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError after retries", err, err)
			}
			if calls != 3 {
				t.Fatalf("gate calls = %d, want 3", calls)
			}
		})
	}

	t.Run("gate refusal is permanent, not retried", func(t *testing.T) {
		// The refusal is re-read under the migration lock before it is final,
		// which takes a session. With none to be had, nothing contradicts the
		// first read, and the backoff loop never asks the gate again.
		calls := 0
		gate := func(context.Context, schema.DBConn) error {
			calls++
			return &schema.RemoteMigrateGateError{CurrentVersion: 1, LatestVersion: 2, Pending: 1}
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if !schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1 (refusal must not be retried)", calls)
		}
	})

	t.Run("shared-store refusal is permanent, not retried", func(t *testing.T) {
		// The classification keys on the error TYPE, so the new #5920 decision
		// inherits it — pinned explicitly because retrying this particular
		// refusal would mean retrying it into the migration it just refused.
		calls := 0
		gate := func(context.Context, schema.DBConn) error {
			calls++
			return &schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 66, Pending: 1,
				Decision: "shared-no-remote",
			}
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if !schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1 (a shared-store refusal must not be retried)", calls)
		}
	})

	t.Run("non-retryable probe error is permanent", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, schema.DBConn) error {
			calls++
			return errors.New("remote-migrate gate: read remotes: syntax error")
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if err == nil || schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %v, want plain permanent error", err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1 (non-retryable must not be retried)", calls)
		}
	})

	t.Run("unrelated typed MySQL error is permanent", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, schema.DBConn) error {
			calls++
			return fmt.Errorf("remote-migrate gate: probe schema: %w",
				&mysql.MySQLError{Number: 1105, Message: "connection lost while validating commit"})
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if err == nil || schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %v, want plain permanent error", err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1", calls)
		}
	})
}

// TestLockedGateRecheckOutcome pins how the retry loop reads an attempt whose
// gate refused before the migration lock (GH#7122): only a refusal read again
// UNDER the lock is final; a busy lock means a migration pass is still running
// and is waited out; a pass that finished in between lets the open go on; and
// when the lock was never reached, the first refusal stands. No Dolt server is
// needed: these are the attempt's possible results, fed in directly.
func TestLockedGateRecheckOutcome(t *testing.T) {
	refusal := &schema.RemoteMigrateGateError{CurrentVersion: 22, LatestVersion: 74, Pending: 52, Decision: "shared-no-remote", Shared: true}
	lockedRefusal := &schema.RemoteMigrateGateError{CurrentVersion: 30, LatestVersion: 74, Pending: 44, Decision: "shared-no-remote", Shared: true}
	lockBusy := fmt.Errorf("schema migration: schema: acquire migration lock: %w: timeout", schema.ErrMigrationLockUnavailable)
	transientProbe := fmt.Errorf("remote-migrate gate: read current version: %w", errors.New("driver: bad connection"))
	brokenProbe := errors.New("remote-migrate gate: read remotes: syntax error")
	noSession := errors.New("schema: pin connection: dial tcp 127.0.0.1:1: connect: connection refused")

	for _, tc := range []struct {
		name          string
		lockedErr     error // what the gate returned under the lock; nil if it never ran
		schemaErr     error // the attempt's error
		want          error
		wantPermanent bool
	}{
		{name: "converged before the lock: the pass finished, go on", want: nil},
		{name: "refused again under the lock: final", lockedErr: lockedRefusal, schemaErr: lockedRefusal, want: lockedRefusal, wantPermanent: true},
		{name: "locked read hit a transient probe error: retry", lockedErr: transientProbe, schemaErr: transientProbe, want: transientProbe},
		{name: "locked read failed for good: final", lockedErr: brokenProbe, schemaErr: brokenProbe, want: brokenProbe, wantPermanent: true},
		{name: "lock still held by a migrating session: wait and retry", schemaErr: lockBusy, want: lockBusy},
		{name: "lock never reached: the first refusal stands", schemaErr: noSession, want: refusal, wantPermanent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &lockedGateRecheck{refusal: refusal, lockedErr: tc.lockedErr}
			got := r.outcome(tc.schemaErr)
			var permanent *backoff.PermanentError
			isPermanent := errors.As(got, &permanent)
			if isPermanent != tc.wantPermanent {
				t.Fatalf("outcome(%v) = %v, permanent %v; want permanent %v", tc.schemaErr, got, isPermanent, tc.wantPermanent)
			}
			if isPermanent {
				got = permanent.Err
			}
			if got != tc.want {
				t.Fatalf("outcome(%v) = %v; want %v", tc.schemaErr, got, tc.want)
			}
		})
	}

	t.Run("run latches an allow and records anything else", func(t *testing.T) {
		satisfied := false
		answer := error(lockedRefusal)
		r := &lockedGateRecheck{
			gate:      func(context.Context, schema.DBConn) error { return answer },
			refusal:   refusal,
			satisfied: &satisfied,
		}
		if err := r.run(context.Background(), nil); err != lockedRefusal || r.lockedErr != lockedRefusal || satisfied {
			t.Fatalf("refusing run = %v (lockedErr %v, satisfied %v); want the refusal recorded and no latch", err, r.lockedErr, satisfied)
		}
		answer = nil
		r.lockedErr = nil
		if err := r.run(context.Background(), nil); err != nil || r.lockedErr != nil || !satisfied {
			t.Fatalf("allowing run = %v (lockedErr %v, satisfied %v); want nil and the latch set", err, r.lockedErr, satisfied)
		}
	})
}
