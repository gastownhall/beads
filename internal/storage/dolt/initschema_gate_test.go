package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestInitSchemaOnDBWithRetryAndGate_GateErrorClassification verifies the gate
// runs INSIDE the retry loop (bd-6dnrw.30): transient probe failures (a freshly
// auto-started server still warming its catalog) are retried like the migration
// itself, while a gate refusal and unexpected probe errors are permanent. No
// Dolt server is needed — the gate fails before the database is ever touched.
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
		gate := func(context.Context, *sql.DB) error {
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
			gate := func(context.Context, *sql.DB) error {
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
		calls := 0
		gate := func(context.Context, *sql.DB) error {
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

	t.Run("non-retryable probe error is permanent", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, *sql.DB) error {
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
		gate := func(context.Context, *sql.DB) error {
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
