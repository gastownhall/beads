package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusingConnector is a pool that never connects; these tests only need a
// live *sql.DB for Close to release.
type refusingConnector struct{}

func (refusingConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("refusing connector")
}
func (refusingConnector) Driver() driver.Driver { return nil }

// A library host (internal/storage/uowstore) can reach the provider after it
// closed: every pool user must refuse with ErrProviderClosed, never panic on
// the released pool.
func TestDoltSQLProviderRefusesAfterClose(t *testing.T) {
	p := &doltSQLProvider{defaultBranch: defaultBranch, db: sql.OpenDB(refusingConnector{})}
	require.NoError(t, p.Close(context.Background()))
	require.NoError(t, p.Close(context.Background()), "a second Close is a no-op")

	assert.Nil(t, p.UnderlyingDB())
	require.NotPanics(t, func() {
		_, err := p.BeginTx(context.Background())
		assert.ErrorIs(t, err, ErrProviderClosed)
		_, err = p.NewUOW(context.Background())
		assert.ErrorIs(t, err, ErrProviderClosed)
		err = p.RunNonTx(context.Background(), func(context.Context, *sql.Conn) error { return nil })
		assert.ErrorIs(t, err, ErrProviderClosed)
		err = withDeleteFence(context.Background(), p, "bd-1", func() error { return nil })
		assert.ErrorIs(t, err, ErrProviderClosed)
		// Activating the journal probes the pool; after Close it must skip
		// the probe rather than query a nil pool.
		p.SetEventsJournalEnabled(true)
		p.SetEventsJournalEnabled(false)
	})
}

// Close racing pool readers must not race on the pool field (run under -race).
func TestDoltSQLProviderCloseRacesReaders(t *testing.T) {
	p := &doltSQLProvider{defaultBranch: defaultBranch, db: sql.OpenDB(refusingConnector{})}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = p.UnderlyingDB()
				if j == 0 {
					p.SetEventsJournalEnabled(true)
				}
				if tx, err := p.BeginTx(context.Background()); err == nil {
					tx.RollbackUnlessCommitted(context.Background())
				}
			}
		}()
	}
	require.NoError(t, p.Close(context.Background()))
	wg.Wait()
}
