package versioncontrolops

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// The two spellings the engine's stomped-tables diagnostic is known to take,
// both captured live: the single-table shape inline (the #6368 field report),
// and the multiline shape the pinned engine emits (the embedded and
// server-suite repros), wrapped the way each pull route frames it.
const (
	stompedInlineFixture = "merge origin/main: Error 1105: error: local changes would be stomped by merge: events\n Please commit your changes before you merge."
	stompedMultilineCLI  = "dolt pull failed: error: local changes would be stomped by merge:\n\tevents\n Please commit your changes before you merge.: exit status 1"
)

// TestParseStompedTableNames pins the bounded parse at the heart of the
// #6368 classification: every diagnostic it can parse exactly, and — more
// important — every near-miss it must decline rather than invent a table
// name from surrounding error text.
func TestParseStompedTableNames(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
		ok   bool
	}{
		{"nil error", nil, nil, false},
		{
			"an unrelated error",
			errors.New("failed to pull from origin/main: Error 1105: the remote end hung up unexpectedly"),
			nil, false,
		},
		{
			"the field report's inline single-table shape",
			errors.New(stompedInlineFixture),
			[]string{"events"}, true,
		},
		{
			"the engine's multiline shape inside a CLI transfer error",
			errors.New(stompedMultilineCLI),
			[]string{"events"}, true,
		},
		{
			"names are deduped and sorted",
			errors.New("error: local changes would be stomped by merge:\n\tleases\n\tevents\n\tleases\n Please commit your changes before you merge."),
			[]string{"events", "leases"}, true,
		},
		{
			"carriage returns separate names too",
			errors.New("error: local changes would be stomped by merge:\r\n\tevents\r\n Please commit your changes before you merge."),
			[]string{"events"}, true,
		},
		{
			"no marker declines",
			errors.New("Error 1105: local changes in events would conflict\n Please commit your changes before you merge."),
			nil, false,
		},
		{
			"no terminator declines",
			errors.New("error: local changes would be stomped by merge:\n\tevents\n and the pull aborted"),
			nil, false,
		},
		{
			"an empty segment declines",
			errors.New("error: local changes would be stomped by merge:\n Please commit your changes before you merge."),
			nil, false,
		},
		{
			"a token carrying internal whitespace declines the whole diagnostic",
			errors.New("error: local changes would be stomped by merge:\n\tevents\n\tand other tables\n Please commit your changes before you merge."),
			nil, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseStompedTableNames(tt.err)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (names %v)", ok, tt.ok, got)
			}
			if !ok {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("names = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("names = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// newIgnoredTablesMock returns a DBConn backed by sqlmock with exact-match
// queries, so every classification read asserts its own statement too.
func newIgnoredTablesMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// expectIgnoredRows arms the dolt_status_ignored read with one row per
// table name, the engine's half of the classification.
func expectIgnoredRows(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table_name"})
	for _, table := range tables {
		rows = rows.AddRow(table)
	}
	mock.ExpectQuery("SELECT table_name FROM dolt_status_ignored WHERE ignored").
		WillReturnRows(rows)
}

// TestMergeNotOpen pins the strict no-open-merge read: only a clean,
// successful false observation counts, and a failed read is never proof.
func TestMergeNotOpen(t *testing.T) {
	t.Run("a clean false is proof no merge is open", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		open, err := MergeNotOpen(context.Background(), db)
		if err != nil || !open {
			t.Fatalf("MergeNotOpen = (%v, %v), want (true, nil)", open, err)
		}
	})
	t.Run("an open merge is not proof of no merge", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(true))
		open, err := MergeNotOpen(context.Background(), db)
		if err != nil || open {
			t.Fatalf("MergeNotOpen = (%v, %v), want (false, nil)", open, err)
		}
	})
	t.Run("a failed read is not proof either way", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnError(errors.New("table dolt_merge_status does not exist"))
		open, err := MergeNotOpen(context.Background(), db)
		if err == nil || open {
			t.Fatalf("MergeNotOpen = (%v, %v), want (false, error)", open, err)
		}
	})
}

// TestClassifyIgnoredTablesRefusal pins the classification: the typed error
// only for a diagnostic whose every name the session positively confirms as
// an ignored working table, and the original error for everything else.
func TestClassifyIgnoredTablesRefusal(t *testing.T) {
	mergeErr := errors.New(stompedInlineFixture)

	t.Run("a confirmed refusal is typed with the names and the original", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "events", "leases")
		typed, confirmed := ClassifyIgnoredTablesRefusal(context.Background(), db, mergeErr)
		if !confirmed {
			t.Fatalf("confirmed = false for a refusal the session confirms")
		}
		if typed == nil {
			t.Fatal("confirmed classification returned no typed error")
		}
		if len(typed.Tables) != 1 || typed.Tables[0] != "events" {
			t.Fatalf("typed.Tables = %v, want [events]", typed.Tables)
		}
		if !errors.Is(typed, mergeErr) {
			t.Fatalf("typed error lost the original: %v", typed.Unwrap())
		}
	})
	t.Run("a name the session does not confirm is unconfirmed", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "leases")
		typed, confirmed := ClassifyIgnoredTablesRefusal(context.Background(), db, mergeErr)
		if confirmed || typed != nil {
			t.Fatalf("classification = (%v, %v) for an unconfirmed name, want (nil, false)", typed, confirmed)
		}
	})
	t.Run("unreadable ignore evidence is unconfirmed", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		mock.ExpectQuery("SELECT table_name FROM dolt_status_ignored WHERE ignored").
			WillReturnError(errors.New("no such table"))
		typed, confirmed := ClassifyIgnoredTablesRefusal(context.Background(), db, mergeErr)
		if confirmed || typed != nil {
			t.Fatalf("classification = (%v, %v) for unreadable evidence, want (nil, false)", typed, confirmed)
		}
	})
	t.Run("an unrelated error is unclassified without touching the session", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		typed, confirmed := ClassifyIgnoredTablesRefusal(context.Background(), db,
			errors.New("failed to pull from origin/main: Error 1105: fetch rejected"))
		if confirmed || typed != nil {
			t.Fatalf("classification = (%v, %v) for an unrelated error, want (nil, false)", typed, confirmed)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("an unrelated error read the session: %v", err)
		}
	})
}

// TestResolveStompedIgnoredTables pins the pull wrappers' decision table
// (GH#6368): the no-op only with complete proof, the typed error for a
// confirmed refusal without it, and the untouched original for anything the
// classification does not positively confirm.
func TestResolveStompedIgnoredTables(t *testing.T) {
	const ref = "origin/main"
	mergeErr := errors.New(stompedInlineFixture)

	t.Run("not the diagnostic keeps the original", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		unrelated := errors.New("merge origin/main: Error 1105: conflicts found")
		outcome, handled := resolveStompedIgnoredTables(context.Background(), db, ref, unrelated)
		if handled || !errors.Is(outcome, unrelated) {
			t.Fatalf("outcome = (%v, handled %v), want the original unchanged", outcome, handled)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("an unparseable error read the session: %v", err)
		}
	})
	t.Run("a name the session does not confirm keeps the original", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "leases")
		outcome, handled := resolveStompedIgnoredTables(context.Background(), db, ref, mergeErr)
		if handled || !errors.Is(outcome, mergeErr) {
			t.Fatalf("outcome = (%v, handled %v), want the original unchanged", outcome, handled)
		}
	})
	t.Run("unreadable confirmation keeps the original", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		mock.ExpectQuery("SELECT table_name FROM dolt_status_ignored WHERE ignored").
			WillReturnError(errors.New("no such table"))
		outcome, handled := resolveStompedIgnoredTables(context.Background(), db, ref, mergeErr)
		if handled || !errors.Is(outcome, mergeErr) {
			t.Fatalf("outcome = (%v, handled %v), want the original unchanged", outcome, handled)
		}
	})
	t.Run("proven containment is the no-op", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "events")
		mock.ExpectQuery("SELECT DOLT_MERGE_BASE('HEAD', ?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"base"}).AddRow("hash1"))
		mock.ExpectQuery("SELECT DOLT_HASHOF(?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("hash1"))
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		outcome, handled := resolveStompedIgnoredTables(context.Background(), db, ref, mergeErr)
		if !handled || outcome != nil {
			t.Fatalf("outcome = (%v, handled %v), want the proven no-op (nil, true)", outcome, handled)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("the no-op proof skipped a read: %v", err)
		}
	})
	t.Run("a diverged ref is the typed refusal", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "events")
		mock.ExpectQuery("SELECT DOLT_MERGE_BASE('HEAD', ?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"base"}).AddRow("common-ancestor"))
		mock.ExpectQuery("SELECT DOLT_HASHOF(?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("peer-tip"))
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		assertTypedRefusal(t, context.Background(), db, ref, mergeErr)
	})
	t.Run("an unresolvable ref is the typed refusal", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "events")
		mock.ExpectQuery("SELECT DOLT_MERGE_BASE('HEAD', ?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"base"}).AddRow(nil))
		mock.ExpectQuery("SELECT DOLT_HASHOF(?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(nil))
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		assertTypedRefusal(t, context.Background(), db, ref, mergeErr)
	})
	t.Run("unreadable containment evidence is the typed refusal", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "events")
		mock.ExpectQuery("SELECT DOLT_MERGE_BASE('HEAD', ?)").
			WithArgs(ref).
			WillReturnError(errors.New("session closed"))
		mock.ExpectQuery("SELECT DOLT_HASHOF(?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("hash1"))
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		assertTypedRefusal(t, context.Background(), db, ref, mergeErr)
	})
	t.Run("unknown merge state is the typed refusal", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "events")
		mock.ExpectQuery("SELECT DOLT_MERGE_BASE('HEAD', ?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"base"}).AddRow("hash1"))
		mock.ExpectQuery("SELECT DOLT_HASHOF(?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("hash1"))
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnError(errors.New("no such table"))
		assertTypedRefusal(t, context.Background(), db, ref, mergeErr)
	})
	t.Run("an open merge is the typed refusal", func(t *testing.T) {
		db, mock := newIgnoredTablesMock(t)
		expectIgnoredRows(mock, "events")
		mock.ExpectQuery("SELECT DOLT_MERGE_BASE('HEAD', ?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"base"}).AddRow("hash1"))
		mock.ExpectQuery("SELECT DOLT_HASHOF(?)").
			WithArgs(ref).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("hash1"))
		mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(true))
		assertTypedRefusal(t, context.Background(), db, ref, mergeErr)
	})
}

// assertTypedRefusal asserts the outcome the confirmed-but-unproven refusals
// share: handled, the typed error carrying the confirmed names and the
// original, and every classification read actually made.
func assertTypedRefusal(t *testing.T, ctx context.Context, db DBConn, ref string, mergeErr error) {
	t.Helper()
	outcome, handled := resolveStompedIgnoredTables(ctx, db, ref, mergeErr)
	if !handled {
		t.Fatalf("outcome = (%v, handled false), want the typed refusal", outcome)
	}
	var typed *IgnoredTablesMergeError
	if !errors.As(outcome, &typed) {
		t.Fatalf("outcome = %v, want the typed IgnoredTablesMergeError", outcome)
	}
	if len(typed.Tables) != 1 || typed.Tables[0] != "events" {
		t.Fatalf("typed.Tables = %v, want [events]", typed.Tables)
	}
	if !errors.Is(typed, mergeErr) {
		t.Fatalf("typed refusal lost the original error: %v", typed.Unwrap())
	}
}
