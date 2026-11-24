//go:build !cgo

package versioncontrolops

// CaptureDoltPruneOutput cannot observe a summary when Dolt's embedded CLI
// output is not compiled into the process. SQL-server and proxy routes likewise
// do not return the server's summary over SQL.
func CaptureDoltPruneOutput(_ string, syncBackup func() error) (BackupPruneOutcome, error) {
	if err := syncBackup(); err != nil {
		return unavailableBackupPruneOutcome(), err
	}
	return unavailableBackupPruneOutcome(), nil
}
