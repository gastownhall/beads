package uow

import (
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// NewDoltSQLProviderOverDBForTest is a doltSQLProvider over db with nothing
// else set, for the external tests that drive a provider through packages
// that import this one (internal/versionedhistory), which an in-package test
// cannot import.
func NewDoltSQLProviderOverDBForTest(db *sql.DB) UnitOfWorkProvider {
	return &doltSQLProvider{defaultBranch: defaultBranch, db: db}
}

// VersionedHistoryScopedForTest reports whether uw's transaction is scoped for
// minting, the answer BeginTx bound to its pinned session.
func VersionedHistoryScopedForTest(uw UnitOfWork) bool {
	return issueops.VersionedHistoryEnabledFor(uw.(*baseUOW).tx.(*doltServerTx).conn)
}
