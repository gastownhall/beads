package schema

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// bd-7bpkd / ga-28co77: failure paths of ResetHardPreservingCloneLocalFKs,
// driven through sqlmock's strict statement order so a path the helper skips
// or adds shows up as an unmet or unexpected statement.

var errInjected = errors.New("injected failure")

// expectFKProbe mocks one readCloneLocalFKState pass. tables names the
// clone-local tables that exist; present names the FKs ("table.constraint")
// on them.
func expectFKProbe(mock sqlmock.Sqlmock, tables map[string]bool, present map[string]bool) {
	for _, fk := range CloneLocalFKs {
		exists := 0
		if tables[fk.Table] {
			exists = 1
		}
		mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?")).
			WithArgs(fk.Table).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(exists))
		if exists == 0 {
			continue
		}
		n := 0
		if present[fk.String()] {
			n = 1
		}
		mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLE_CONSTRAINTS")).
			WithArgs(fk.Table, fk.Constraint).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
	}
}

// relinkSQL matches RelinkCloneLocalFK's DELETE and ALTER for fk, with or
// without identifier quoting.
func relinkSQL(fk CloneLocalFK) (deleteRE, alterRE string) {
	q := func(id string) string { return "`?" + regexp.QuoteMeta(id) + "`?" }
	deleteRE = "DELETE FROM " + q(fk.Table) + " WHERE " + q(fk.Column) + " IS NOT NULL"
	alterRE = "ALTER TABLE " + q(fk.Table) + " ADD CONSTRAINT " + q(fk.Constraint) + " FOREIGN KEY"
	return deleteRE, alterRE
}

func specFK(t *testing.T, name string) CloneLocalFK {
	t.Helper()
	for _, fk := range CloneLocalFKs {
		if fk.String() == name {
			return fk
		}
	}
	t.Fatalf("no spec FK %s", name)
	return CloneLocalFK{}
}

func newFKMockDB(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, mock
}

// A failed pre-reset probe must not reset: nothing after the first probe
// query may run.
func TestResetHardPreservingCloneLocalFKs_ProbeFailureDoesNotReset(t *testing.T) {
	db, mock := newFKMockDB(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLES")).
		WithArgs("events").
		WillReturnError(errInjected)

	_, err := ResetHardPreservingCloneLocalFKs(context.Background(), db, "")
	if err == nil || !strings.Contains(err.Error(), "reset not run") || !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want a reset-not-run error wrapping the probe failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// One FK's failed re-link must not stop the others: the error names only the
// failed FK and carries the injected cause; the next FK is still re-linked.
func TestResetHardPreservingCloneLocalFKs_ContinuesPastAFailedRelink(t *testing.T) {
	db, mock := newFKMockDB(t)
	events, labels := specFK(t, "events.fk_events_issue"), specFK(t, "wisp_labels.fk_wisp_labels_issue")
	tables := map[string]bool{"events": true, "wisp_labels": true}

	expectFKProbe(mock, tables, map[string]bool{events.String(): true, labels.String(): true})
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectFKProbe(mock, tables, nil)
	expectRefTableExists(mock, "issues", true)
	del, alter := relinkSQL(events)
	mock.ExpectExec(del).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(alter).WillReturnError(errInjected)
	expectRefTableExists(mock, "wisps", true)
	del, alter = relinkSQL(labels)
	mock.ExpectExec(del).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(alter).WillReturnResult(sqlmock.NewResult(0, 0))

	result, err := ResetHardPreservingCloneLocalFKs(context.Background(), db, "")
	if err == nil {
		t.Fatal("err = nil, want the events relink failure")
	}
	msg := err.Error()
	for _, want := range []string{"hard reset succeeded", "events.fk_events_issue", errInjected.Error()} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "wisp_labels") {
		t.Errorf("error %q names wisp_labels, which re-linked", msg)
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error does not wrap the injected ALTER failure")
	}
	if len(result.Relinked) != 1 || result.Relinked[0].String() != labels.String() {
		t.Errorf("Relinked = %v, want only %s", result.Relinked, labels)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A failed post-reset probe leaves the FK state UNKNOWN: the error must say
// so, not claim the FKs were dropped and enforcement is off.
func TestResetHardPreservingCloneLocalFKs_UnverifiedAfterReset(t *testing.T) {
	db, mock := newFKMockDB(t)
	tables := map[string]bool{"events": true}
	expectFKProbe(mock, tables, map[string]bool{"events.fk_events_issue": true})
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLES")).
		WithArgs("events").
		WillReturnError(errInjected)

	_, err := ResetHardPreservingCloneLocalFKs(context.Background(), db, "")
	if err == nil {
		t.Fatal("err = nil, want an unverified-state error")
	}
	msg := err.Error()
	for _, want := range []string{"hard reset succeeded", "could not verify", "events.fk_events_issue", errInjected.Error()} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	for _, overclaim := range []string{"could not be re-linked", "enforcement is off"} {
		if strings.Contains(msg, overclaim) {
			t.Errorf("error %q claims %q, but the post-reset state is unknown", msg, overclaim)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Identifiers are backtick-quoted in the DELETE and the ALTER.
func TestRelinkCloneLocalFK_QuotesIdentifiers(t *testing.T) {
	db, mock := newFKMockDB(t)
	mock.ExpectExec(regexp.QuoteMeta(
		"DELETE FROM `wisp_labels` WHERE `issue_id` IS NOT NULL AND NOT EXISTS (SELECT 1 FROM `wisps` r WHERE r.`id` = `wisp_labels`.`issue_id`)")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(
		"ALTER TABLE `wisp_labels` ADD CONSTRAINT `fk_wisp_labels_issue` FOREIGN KEY (`issue_id`) REFERENCES `wisps` (`id`) ON DELETE CASCADE ON UPDATE CASCADE")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if _, err := RelinkCloneLocalFK(context.Background(), db, specFK(t, "wisp_labels.fk_wisp_labels_issue")); err != nil {
		t.Fatalf("RelinkCloneLocalFK: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Astra r2 SF (clone_local_fks.go:335): when the DELETE removed rows and the
// ALTER then fails, the completed deletions must be disclosed — in the error
// and in the returned count — so nobody believes the table was left untouched.
func TestRelinkCloneLocalFK_ReportsDeletedRowsWhenAlterFails(t *testing.T) {
	db, mock := newFKMockDB(t)
	fk := specFK(t, "events.fk_events_issue")
	del, alter := relinkSQL(fk)
	mock.ExpectExec(del).WillReturnResult(sqlmock.NewResult(0, 10))
	mock.ExpectExec(alter).WillReturnError(errInjected)

	removed, err := RelinkCloneLocalFK(context.Background(), db, fk)
	if err == nil || !errors.Is(err, errInjected) {
		t.Fatalf("RelinkCloneLocalFK() error = %v, want the injected ALTER failure", err)
	}
	if removed != 10 {
		t.Errorf("RelinkCloneLocalFK() removed = %d, want 10", removed)
	}
	if !strings.Contains(err.Error(), "10 orphaned row(s)") {
		t.Errorf("RelinkCloneLocalFK() error %q does not disclose the 10 rows already deleted", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The same disclosure reaches the helper's reset-succeeded error.
func TestResetHardPreservingCloneLocalFKs_ReportsDeletedRowsWhenRelinkFails(t *testing.T) {
	db, mock := newFKMockDB(t)
	events := specFK(t, "events.fk_events_issue")
	tables := map[string]bool{"events": true}
	expectFKProbe(mock, tables, map[string]bool{events.String(): true})
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectFKProbe(mock, tables, nil)
	expectRefTableExists(mock, "issues", true)
	del, alter := relinkSQL(events)
	mock.ExpectExec(del).WillReturnResult(sqlmock.NewResult(0, 10))
	mock.ExpectExec(alter).WillReturnError(errInjected)

	_, err := ResetHardPreservingCloneLocalFKs(context.Background(), db, "")
	if err == nil {
		t.Fatal("err = nil, want the relink failure")
	}
	for _, want := range []string{"hard reset succeeded", "events.fk_events_issue", "10 orphaned row(s)", errInjected.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// expectRefTableExists mocks the helper's check, made only for an FK the
// reset dropped from a table that survived, that the FK's referenced table
// survived too.
func expectRefTableExists(mock sqlmock.Sqlmock, table string, exists bool) {
	n := 0
	if exists {
		n = 1
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?")).
		WithArgs(table).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
}

// Upstream review 1, MAJOR 1 (helper level): the reset keeps the FK's own
// table but removes the table it REFERENCES. The FK went with that table:
// there is nothing to re-link, so no DELETE, no ALTER, no error, and the
// result reports it as removed with its table.
func TestResetHardPreservingCloneLocalFKs_RefTableRemovedByReset(t *testing.T) {
	db, mock := newFKMockDB(t)
	tables := map[string]bool{"events": true}
	expectFKProbe(mock, tables, map[string]bool{"events.fk_events_issue": true})
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectFKProbe(mock, tables, nil) // events survives, its FK does not
	expectRefTableExists(mock, "issues", false)

	result, err := ResetHardPreservingCloneLocalFKs(context.Background(), db, "")
	if err != nil {
		t.Fatalf("ResetHardPreservingCloneLocalFKs() error = %v, want nil (the FK went with its referenced table)", err)
	}
	if len(result.Relinked) != 0 || len(result.AlreadySevered) != 0 {
		t.Errorf("result = %+v, want nothing relinked and nothing already severed", result)
	}
	// Checked through %+v so this test also compiles on the pre-fix head.
	if got := fmt.Sprintf("%+v", result); !strings.Contains(got, "RemovedWithTable:[events.fk_events_issue]") {
		t.Errorf("result = %s, want RemovedWithTable:[events.fk_events_issue]", got)
	}
	if w := result.Warning(); w != "" {
		t.Errorf("Warning() = %q, want none: there is nothing for bd doctor --fix to do", w)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
