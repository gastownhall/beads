package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBackupLockSerializesBackups pins the lock itself: a second holder is
// refused while the first holds it, a bounded wait gives up with errBackupBusy,
// and releasing makes it available again.
//
// Cannot be parallel: prepareBackupStatusTest mutates package globals.
func TestBackupLockSerializesBackups(t *testing.T) {
	prepareBackupStatusTest(t)

	release, err := acquireBackupLock(0)
	if err != nil {
		t.Fatalf("first acquisition: %v", err)
	}
	if _, err := acquireBackupLock(0); !errors.Is(err, errBackupBusy) {
		t.Fatalf("non-blocking acquisition while held = %v, want errBackupBusy", err)
	}
	start := time.Now()
	if _, err := acquireBackupLock(150 * time.Millisecond); !errors.Is(err, errBackupBusy) {
		t.Fatalf("bounded wait while held = %v, want errBackupBusy", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Fatalf("bounded wait returned after %s; it must poll for the wait it was given", waited)
	}
	release()

	again, err := acquireBackupLock(0)
	if err != nil {
		t.Fatalf("acquisition after release: %v", err)
	}
	again()
}

// TestAutoBackupSkipsWhileAnotherBackupRuns: auto-backup never waits on the
// lock. With it held the backup does not run and the throttle state is left
// alone, so the next command retries; once released, the same call backs up.
func TestAutoBackupSkipsWhileAnotherBackupRuns(t *testing.T) {
	prepareBackupStatusTest(t)
	t.Setenv("BD_BACKUP_ENABLED", "true")
	t.Setenv("BD_BACKUP_INTERVAL", "1ms")
	t.Setenv("BD_GIT_HOOK", "")
	initConfigForTest(t)

	oldStore := store
	fake := &failingBackupStore{commit: "c1"}
	store = fake
	t.Cleanup(func() { store = oldStore })

	release, err := acquireBackupLock(0)
	if err != nil {
		t.Fatalf("hold the backup lock: %v", err)
	}
	maybeAutoBackup(context.Background())
	if fake.backupCalls != 0 {
		t.Fatalf("auto-backup ran %d backup(s) while another backup held the lock", fake.backupCalls)
	}
	dir, err := backupDir()
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := loadBackupState(dir); !st.Timestamp.IsZero() {
		t.Fatalf("a skipped auto-backup advanced the throttle to %v", st.Timestamp)
	}

	release()
	maybeAutoBackup(context.Background())
	if fake.backupCalls != 1 {
		t.Fatalf("auto-backup ran %d backup(s) after the lock was released, want 1", fake.backupCalls)
	}
}

// TestBackupSyncFailsWhenAnotherBackupHoldsTheLock: an explicit sync waits
// backupLockWait for another backup and then fails loudly, before touching the
// store — it never reports a sync it did not do.
func TestBackupSyncFailsWhenAnotherBackupHoldsTheLock(t *testing.T) {
	prepareBackupStatusTest(t)
	oldWait := backupLockWait
	backupLockWait = 100 * time.Millisecond
	t.Cleanup(func() { backupLockWait = oldWait })

	oldStore := store
	fake := &failingBackupStore{commit: "c1"}
	store = fake
	t.Cleanup(func() { store = oldStore })

	release, err := acquireBackupLock(0)
	if err != nil {
		t.Fatalf("hold the backup lock: %v", err)
	}
	defer release()

	var runErr error
	stderr := captureStderrForBackupLockTest(t, func() {
		runErr = backupSyncCmd.RunE(backupSyncCmd, nil)
	})
	if runErr == nil {
		t.Fatal("bd backup sync succeeded while another backup held the lock")
	}
	if !strings.Contains(stderr, "another backup is running") {
		t.Fatalf("sync refusal does not say why; stderr:\n%s", stderr)
	}
	if fake.backupCalls != 0 {
		t.Fatalf("refused sync still reached the store (%d backup calls)", fake.backupCalls)
	}
}

func captureStderrForBackupLockTest(t *testing.T, fn func()) string {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	_ = w.Close()
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	_ = r.Close()
	return string(buf[:n])
}
