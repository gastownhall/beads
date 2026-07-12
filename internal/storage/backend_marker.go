package storage

// NonCommitGraphBackend is an affirmative *negative* marker: it is implemented
// only by backends whose Commit/GetCurrentCommit/Push do NOT participate in a
// real version-control commit graph (e.g. Postgres, SQLite). cmd/bd's PostRun
// skips its Dolt-only maintenance tail (auto-commit, auto-export, auto-backup,
// auto-push) when the active store implements this and returns true.
//
// The marker is negative and lives only on non-Dolt backends by design: a store
// that does NOT implement it is assumed to be Dolt, so the default path runs the
// full maintenance tail unchanged and stays byte-identical. New SQL backends opt
// OUT of Dolt maintenance by implementing this; they never have to touch the
// Dolt constructors.
type NonCommitGraphBackend interface {
	CommitGraphUnsupported() bool
}
