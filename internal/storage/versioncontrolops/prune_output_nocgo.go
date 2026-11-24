//go:build !cgo

package versioncontrolops

// beginDoltPruneOutputCapture is unavailable when Dolt's embedded CLI output
// is not compiled into the process. SQL-server and proxied routes likewise do
// not return the server's prune summary over SQL.
func beginDoltPruneOutputCapture(string) func() BackupPruneOutcome {
	return unavailableBackupPruneOutcome
}
