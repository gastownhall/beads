//go:build windows

package util

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/steveyegge/beads/internal/lockfile"
)

// TestRetirableLockRetireWhileHeld covers the retirement contract that makes
// the reverse migration safe: once a lease retires its path, the name stays
// unavailable to openers until the lease unlocks, and is gone for good
// afterwards (#6884). Windows refuses every open of a delete-pending name;
// wine (used for cross-platform evidence) does not model that refusal, so
// the refusal clause runs only where the platform enforces it and skips
// with a reason elsewhere.
func TestRetirableLockRetireWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.lock")

	lock, err := TryRetirableLock(path)
	require.NoError(t, err)
	require.NoError(t, lock.Retire())

	t.Run("name unavailable to openers until unlock", func(t *testing.T) {
		probe, probeErr := os.OpenFile(path, os.O_RDWR, 0o600)
		if probeErr == nil {
			_ = probe.Close()
			t.Skip("platform does not refuse opens of a delete-pending name (wine); native Windows refuses them")
		}
		_, err := openForRetire(path)
		require.Error(t, err, "delete-sharing opener must not reach a retired name while the lease is held")
	})

	lock.Unlock()

	assert.NoFileExists(t, path)
	fresh, err := TryRetirableLock(path)
	require.NoError(t, err, "fresh acquisition must succeed after unlock")
	fresh.Unlock()
}

// openForRetire is the deletion-handle open Retire uses: a competing
// retirement attempt must fail while the name is retired.
func openForRetire(path string) (windows.Handle, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		path16,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

// TestRetirableLockKeepsLockViolationForOthers pins contention
// classification: whoever holds a lifecycle lease, a competing ordinary or
// retirable acquisition fails as lockfile.IsLocked contention, never as an
// open error, and the holder still releases cleanly.
func TestRetirableLockKeepsLockViolationForOthers(t *testing.T) {
	type acquisition func(path string) (*Lock, error)
	cases := []struct {
		name   string
		holder acquisition
		rival  acquisition
	}{
		{name: "ordinary holder, retirable rival", holder: TryLock, rival: TryRetirableLock},
		{name: "retirable holder, ordinary rival", holder: TryRetirableLock, rival: TryLock},
		{name: "retirable holder, retirable rival", holder: TryRetirableLock, rival: TryRetirableLock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "contended.lock")

			held, err := tc.holder(path)
			require.NoError(t, err)

			_, err = tc.rival(path)
			require.Error(t, err, "a held lease must refuse rivals")
			assert.True(t, lockfile.IsLocked(err), "contention must stay classified as a held lock, got %v", err)

			held.Unlock()
			free, err := TryLock(path)
			require.NoError(t, err, "holder must release cleanly after a refused rival")
			free.Unlock()
		})
	}
}

// TestTryRetirableLockRefusesReparsePoint checks acquisition-side refusal:
// a lock pathname whose final component is a reparse point (symlink or
// junction) must not be acquired at all — no lease is taken, no delete
// disposition is applied, and neither the link nor its target changes.
func TestTryRetirableLockRefusesReparsePoint(t *testing.T) {
	refuse := func(t *testing.T, lockPath, probePath string) {
		t.Helper()
		lock, err := TryRetirableLock(lockPath)
		if err == nil {
			lock.Unlock()
		}
		require.Error(t, err, "acquisition must refuse a reparse-point lock path")
		assert.Nil(t, lock, "no lease may be held after refusal")
		assert.False(t, lockfile.IsLocked(err), "refusing a reparse point is not contention")

		content, readErr := os.ReadFile(probePath)
		require.NoError(t, readErr)
		assert.Equal(t, "target", string(content), "the target must be untouched by the refusal")
	}

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.lock")
		require.NoError(t, os.WriteFile(target, []byte("target"), 0o600))
		link := filepath.Join(dir, "link.lock")
		require.NoError(t, os.Symlink(target, link))
		if _, statErr := os.Lstat(link); statErr != nil {
			t.Skipf("platform created a symlink its own file APIs cannot see (%v); reparse coverage needs a platform with observable reparse points", statErr)
		}

		refuse(t, link, target)

		info, statErr := os.Lstat(link)
		require.NoError(t, statErr)
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "the link itself must stay a link")

		// The refused acquisition applied no disposition and locked
		// nothing: the target is lockable and the name is removable.
		free, lockErr := TryLock(target)
		require.NoError(t, lockErr, "the target must remain unlocked after a refused acquisition")
		free.Unlock()
		require.NoError(t, os.Remove(link))
		assert.FileExists(t, target)
	})

	t.Run("junction", func(t *testing.T) {
		dir := t.TempDir()
		targetDir := filepath.Join(dir, "targets")
		require.NoError(t, os.MkdirAll(targetDir, 0o700))
		target := filepath.Join(targetDir, "target.lock")
		require.NoError(t, os.WriteFile(target, []byte("target"), 0o600))
		junction := filepath.Join(dir, "junction")
		if err := makeJunction(junction, targetDir); err != nil {
			t.Skipf("cannot create a junction on this platform: %v", err)
		}

		refuse(t, junction, target)

		// The junction still resolves after the refusal.
		content, readErr := os.ReadFile(filepath.Join(junction, "target.lock"))
		require.NoError(t, readErr)
		assert.Equal(t, "target", string(content))
	})
}

// TestRetireRefusesReplacedPath checks retirement's identity verification:
// when the lease's pathname has been replaced by a different file, Retire
// must refuse rather than delete the replacement.
func TestRetireRefusesReplacedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replaced.lock")

	lease, err := TryRetirableLock(path)
	require.NoError(t, err)

	// Move the leased file aside and put a replacement at its name; the
	// lease's handle still refers to the original file.
	moved := filepath.Join(dir, "moved.lock")
	require.NoError(t, os.Rename(path, moved), "a delete-shared lease's file can be moved aside")
	replacement := filepath.Join(dir, "replacement.lock")
	require.NoError(t, os.WriteFile(replacement, []byte("replacement"), 0o600))
	require.NoError(t, os.Rename(replacement, path))

	require.Error(t, lease.Retire(), "retirement must refuse a replaced path")

	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "replacement", string(content), "the replacement file must survive a refused retirement")

	lease.Unlock()
	content, readErr = os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "replacement", string(content), "the replacement must still be there after the lease is released")
}

// TestTryLockPathRemoveStillFailsWhileHeld guards the retirement machinery
// from loosening ordinary leases: a TryLock file opens without delete
// sharing, so removing its path mid-flight keeps failing with a sharing
// violation — the behavior #6884 routes around for the migration's own
// leases, never removes. The assertion holds on the base and after every
// commit; as a guard it has no red run by design.
func TestTryLockPathRemoveStillFailsWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ordinary.lock")

	held, err := TryLock(path)
	require.NoError(t, err)

	require.Error(t, os.Remove(path), "an ordinary lease's path must stay unremovable while held")
	assert.FileExists(t, path)

	held.Unlock()
	assert.FileExists(t, path, "unlock closes the handle without removing the file")
	require.NoError(t, os.Remove(path), "a released ordinary path removes normally")
}

// makeJunction creates a directory junction at link pointing at target, the
// way mklink /J does: a REPARSE_DATA_BUFFER with the mount-point tag set
// through FSCTL_SET_REPARSE_POINT.
func makeJunction(link, target string) error {
	if err := os.Mkdir(link, 0o700); err != nil {
		return err
	}
	link16, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(
		link16,
		windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("open junction for reparse data: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	print, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	paths := 2 * (len(substitute) + len(print))
	const header = 16 // ReparseTag, ReparseDataLength, Reserved, then the four mount-point offsets
	buf := make([]byte, header+paths)
	binary.LittleEndian.PutUint32(buf[0:], 0xA0000003)             // IO_REPARSE_TAG_MOUNT_POINT
	binary.LittleEndian.PutUint16(buf[4:], uint16(8+paths))        // mount-point data after its own header
	binary.LittleEndian.PutUint16(buf[10:], uint16(len(substitute))) // SubstituteNameLength, in WCHARs
	binary.LittleEndian.PutUint16(buf[12:], uint16(len(substitute))) // PrintNameOffset, after the substitute
	binary.LittleEndian.PutUint16(buf[14:], uint16(len(print)))      // PrintNameLength, in WCHARs
	off := header
	for _, u := range substitute {
		binary.LittleEndian.PutUint16(buf[off:], u)
		off += 2
	}
	for _, u := range print {
		binary.LittleEndian.PutUint16(buf[off:], u)
		off += 2
	}

	var returned uint32
	// nolint:gosec // G103: unsafe.Pointer required for the DeviceIoControl buffer
	if err := windows.DeviceIoControl(
		h,
		0x000900A7, // FSCTL_SET_REPARSE_POINT
		&buf[0],
		uint32(len(buf)),
		nil,
		0,
		&returned,
		nil,
	); err != nil {
		return fmt.Errorf("set reparse point: %w", err)
	}
	return nil
}
