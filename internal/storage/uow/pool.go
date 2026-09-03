package uow

import "time"

// PoolLimits bounds a provider's underlying database/sql pool.
//
// It exists because openDB sets nothing, and database/sql's default is
// UNLIMITED connections. That is the right default for a one-shot CLI process,
// where the pool outlives a single command by milliseconds; it is the wrong one
// for a long-lived server, where every unit of work pins a connection and an
// unbounded request burst becomes an unbounded connection burst against a
// shared Dolt server's max_connections budget.
//
// The rest of the repo already sets these on the connections it owns
// (internal/storage/embeddeddolt/open.go, internal/storage/dolt/transaction.go);
// the unit-of-work provider is the seam that was missing them.
type PoolLimits struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
}

// PoolTuner is implemented by providers backed by a database/sql pool. It is a
// separate interface rather than a method on UnitOfWorkProvider because pooling
// is an implementation property: a provider that is not pool-backed should not
// have to pretend to honor it, and a caller that cares can ask.
//
// Callers must treat it as OPTIONAL and say so out loud when a provider does
// not implement it — silently running unbounded is the failure this exists to
// prevent.
type PoolTuner interface {
	SetPoolLimits(PoolLimits)
}

var _ PoolTuner = (*doltSQLProvider)(nil)

// SetPoolLimits applies limits to the provider's pool. Zero and negative values
// are passed through to database/sql, which reads them as "no limit" — so a
// zero-valued PoolLimits restores the default rather than throttling to zero.
func (p *doltSQLProvider) SetPoolLimits(l PoolLimits) {
	if p.db == nil {
		return
	}
	p.db.SetMaxOpenConns(l.MaxOpenConns)
	p.db.SetMaxIdleConns(l.MaxIdleConns)
	p.db.SetConnMaxIdleTime(l.ConnMaxIdleTime)
	p.db.SetConnMaxLifetime(l.ConnMaxLifetime)
}
