//go:build !windows

package linear

import "os"

type syncLockMetadata struct{}

func syncLockMetadataPath(_ string, lockPath string) string {
	return lockPath
}

func readContendedSyncLockInfo(path string) *SyncLockInfo {
	return readLockInfo(path)
}

func publishSyncLockInfo(f *os.File, _ string) (syncLockMetadata, error) {
	return syncLockMetadata{}, writeLockInfo(f)
}

func clearSyncLockInfo(f *os.File, _ string, _ *syncLockMetadata) error {
	return f.Truncate(0)
}
