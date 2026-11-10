package dolt

import (
	"context"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// newFailingPublishStore builds a DoltStore whose dependencies fail on use:
// the sqlmock pool answers no query, and connStr is a DSN the long-timeout
// push path cannot parse, so a publish that slips past a missing read-only
// guard dies on its own transport plumbing without touching the network.
func newFailingPublishStore(t *testing.T, readOnly, classifiedRead bool) *DoltStore {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Every unexpected sqlmock call is an error; nothing is ever expected.
	_ = mock
	return &DoltStore{
		db:             db,
		connStr:        "invalid-dsn-no-slash",
		readOnly:       readOnly,
		classifiedRead: classifiedRead,
		remote:         "origin",
		branch:         "main",
	}
}

// TestPublishRejectsStrictReadOnly pins GH#7876 item 3's storage half: every
// server-store publish entry point must refuse on a strict read-only store
// before database, subprocess, remote or staging work. The store's pool is a
// fail-on-use mock, so a publish that runs anyway fails with a dependency
// error instead of the read-only refusal — and the test fails.
func TestPublishRejectsStrictReadOnly(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		run  func(*DoltStore) error
	}{
		{name: "push", run: func(s *DoltStore) error { return s.Push(ctx) }},
		{name: "force_push", run: func(s *DoltStore) error { return s.ForcePush(ctx) }},
		{name: "push_remote_named", run: func(s *DoltStore) error { return s.PushRemote(ctx, "town-beta", false) }},
		{name: "push_remote_named_forced", run: func(s *DoltStore) error { return s.PushRemote(ctx, "town-beta", true) }},
		{name: "push_to_peer", run: func(s *DoltStore) error { return s.PushTo(ctx, "town-beta") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFailingPublishStore(t, true, false)
			err := tc.run(s)
			if err == nil {
				t.Fatalf("publish on a strict read-only store succeeded; want read-only refusal")
			}
			if !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("publish on a strict read-only store returned %v, want a read-only refusal", err)
			}
		})
	}

	t.Run("sync", func(t *testing.T) {
		s := newFailingPublishStore(t, true, false)
		result, err := s.Sync(ctx, "town-beta", "")
		if err == nil {
			t.Fatal("Sync on a strict read-only store returned nil error; want read-only refusal")
		}
		if !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("Sync on a strict read-only store returned %v, want a read-only refusal", err)
		}
		if result == nil {
			t.Fatal("Sync must still return its result alongside the refusal")
		}
		if result.Error == nil {
			t.Error("SyncResult.Error is nil; want the read-only refusal recorded on the result")
		}
		if result.Fetched || result.Merged || result.Pushed {
			t.Errorf("refused Sync still reported work: fetched=%v merged=%v pushed=%v",
				result.Fetched, result.Merged, result.Pushed)
		}
	})
}

// TestStrictReadOnlyPublishPredicate pins the exemption: a read-classified
// open (GH#804) is "otherwise a normal writable store" and must NOT trip the
// publish guard, only strict --readonly/preview/foreign-project opens may.
func TestStrictReadOnlyPublishPredicate(t *testing.T) {
	cases := []struct {
		name              string
		readOnly          bool
		classifiedRead    bool
		wantStrictReadonly bool
	}{
		{name: "writable", readOnly: false, classifiedRead: false, wantStrictReadonly: false},
		{name: "classified_read", readOnly: true, classifiedRead: true, wantStrictReadonly: false},
		{name: "strict_readonly", readOnly: true, classifiedRead: false, wantStrictReadonly: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFailingPublishStore(t, tc.readOnly, tc.classifiedRead)
			if got := s.isStrictReadOnlyStore(); got != tc.wantStrictReadonly {
				t.Fatalf("isStrictReadOnlyStore() = %v, want %v", got, tc.wantStrictReadonly)
			}
		})
	}
}

// TestPublishWritableAndClassifiedReadControlsRun is the positive control
// for the predicate above: a writable and a read-classified store must get
// PAST the guard. Both then die on the store's deliberately unusable DSN —
// the long-timeout connection parse error is the proof they reached the
// transport boundary instead of being refused as read-only.
func TestPublishWritableAndClassifiedReadControlsRun(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name           string
		readOnly       bool
		classifiedRead bool
	}{
		{name: "writable", readOnly: false, classifiedRead: false},
		{name: "classified_read", readOnly: true, classifiedRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFailingPublishStore(t, tc.readOnly, tc.classifiedRead)
			err := s.Push(ctx)
			if err == nil {
				t.Fatal("Push on the fail-on-use store succeeded; the control lost its teeth")
			}
			if strings.Contains(err.Error(), "read-only") {
				t.Fatalf("Push was refused as read-only on a store the guard must exempt: %v", err)
			}
			if !strings.Contains(err.Error(), "DSN") {
				t.Fatalf("Push did not reach the transport boundary (want the DSN parse failure): %v", err)
			}
		})
	}
}
