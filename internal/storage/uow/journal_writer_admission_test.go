package uow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestJournalWriterLock_UOWSaturatedPoolIsBusyAtTheDeadline pins the proxied
// provider's pool wait against the unit of work's admission deadline
// (gastownhall/beads#6117): when every pooled session is busy, BeginTx is
// refused busy at the deadline the outer operation fixed, before START
// TRANSACTION, with the caller's context still live.
func TestJournalWriterLock_UOWSaturatedPoolIsBusyAtTheDeadline(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	p := &doltSQLProvider{db: db, database: "beads"}
	p.SetEventsJournalEnabled(true)

	start := time.Now()
	tx, err := p.BeginTx(withJournalWriter(context.Background(), 100*time.Millisecond))
	if tx != nil {
		t.Fatal("BeginTx began a unit of work without a pooled session")
	}
	if !errors.Is(err, issueops.ErrJournalWriterBusy) {
		t.Fatalf("BeginTx on a saturated pool = %v, want ErrJournalWriterBusy", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("BeginTx waited %s for the pool, want it bounded by its 100ms deadline", waited)
	}
}
