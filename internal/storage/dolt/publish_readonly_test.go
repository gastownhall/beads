package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// failOnUseConnector is a database/sql connector that records every attempt
// to open a connection and refuses it. A store built on it proves a method
// returned before any database work: sql.OpenDB is lazy, so the counter stays
// at zero unless the method actually reached for a connection.
type failOnUseConnector struct{ connects atomic.Int64 }

func (c *failOnUseConnector) Connect(context.Context) (driver.Conn, error) {
	c.connects.Add(1)
	return nil, errors.New("failOnUseConnector: unexpected database use")
}

func (c *failOnUseConnector) Driver() driver.Driver { return failOnUseDriver{c} }

type failOnUseDriver struct{ c *failOnUseConnector }

func (d failOnUseDriver) Open(string) (driver.Conn, error) { return d.c.Connect(context.Background()) }

// newPublishGuardStore builds a server store whose database, one-shot
// connections and CLI directory are all unusable, so the only way a publish
// method can return a read-only error with zero connection attempts is the
// guard at its entry.
func newPublishGuardStore(t *testing.T, readOnly, classifiedRead bool) (*DoltStore, *failOnUseConnector) {
	t.Helper()
	conn := &failOnUseConnector{}
	db := sql.OpenDB(conn)
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{
		db:             db,
		connStr:        "not-a-dsn",
		dbPath:         t.TempDir(),
		database:       "beads",
		branch:         "main",
		remote:         "origin",
		serverMode:     true,
		readOnly:       readOnly,
		classifiedRead: classifiedRead,
	}, conn
}

// publishCalls are the five server-store publish entry points AC-4 covers,
// with the forced and named variants.
func publishCalls() []struct {
	name string
	call func(context.Context, *DoltStore) error
} {
	return []struct {
		name string
		call func(context.Context, *DoltStore) error
	}{
		{name: "Push", call: func(ctx context.Context, s *DoltStore) error { return s.Push(ctx) }},
		{name: "ForcePush", call: func(ctx context.Context, s *DoltStore) error { return s.ForcePush(ctx) }},
		{name: "PushRemote", call: func(ctx context.Context, s *DoltStore) error { return s.PushRemote(ctx, "backup", false) }},
		{name: "PushRemote/force", call: func(ctx context.Context, s *DoltStore) error { return s.PushRemote(ctx, "backup", true) }},
		{name: "PushTo", call: func(ctx context.Context, s *DoltStore) error { return s.PushTo(ctx, "peer") }},
		{name: "pushRefToPeer", call: func(ctx context.Context, s *DoltStore) error {
			return s.pushRefToPeer(ctx, "peer", "staging:main")
		}},
		{name: "Sync", call: func(ctx context.Context, s *DoltStore) error {
			result, err := s.Sync(ctx, "peer", "")
			if result == nil {
				return errors.New("Sync returned a nil result")
			}
			if result.Fetched || result.Merged || result.Pushed || result.ConflictsResolved {
				return errors.New("Sync reported progress on a refused run")
			}
			if result.Error == nil || !errors.Is(err, result.Error) {
				return errors.New("Sync must carry its refusal in result.Error and return it")
			}
			return err
		}},
	}
}

// TestPublishRejectsStrictReadOnly pins AC-4 (GH#7876): a strict-read-only
// server store refuses every publish entry point with a read-only error
// before touching the database, a subprocess or the remote, and Sync refuses
// before its pre-merge commit, fetch and staging work.
func TestPublishRejectsStrictReadOnly(t *testing.T) {
	ctx := context.Background()
	for _, tc := range publishCalls() {
		t.Run(tc.name, func(t *testing.T) {
			store, conn := newPublishGuardStore(t, true, false)
			err := tc.call(ctx, store)
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("err = %v, want a read-only refusal", err)
			}
			if n := conn.connects.Load(); n != 0 {
				t.Fatalf("database connection attempts = %d, want 0 (the guard must run first)", n)
			}
		})
	}
}

// TestPublishAllowsWritableAndClassifiedRead is the positive control for
// AC-4: a writable store and a classified-read store (GH#804: readOnly by
// command classification, writable underneath) both get past the guard and
// reach their normal routing, which the fail-on-use database then refuses.
func TestPublishAllowsWritableAndClassifiedRead(t *testing.T) {
	ctx := context.Background()
	for _, open := range []struct {
		name           string
		readOnly       bool
		classifiedRead bool
	}{
		{name: "writable"},
		{name: "classified-read", readOnly: true, classifiedRead: true},
	} {
		t.Run(open.name, func(t *testing.T) {
			for _, tc := range publishCalls() {
				if tc.name == "Sync" {
					// Sync's own progress checks assume a refusal; the
					// routing proof below covers it through PushTo.
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					store, conn := newPublishGuardStore(t, open.readOnly, open.classifiedRead)
					err := tc.call(ctx, store)
					if err != nil && strings.Contains(err.Error(), "read-only") {
						t.Fatalf("err = %v, the guard must not refuse a %s store", err, open.name)
					}
					if n := conn.connects.Load(); n == 0 {
						t.Fatalf("no database connection attempt: %s store never reached routing (err = %v)", open.name, err)
					}
				})
			}
		})
	}
}
