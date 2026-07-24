//go:build windows

package linear

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
	"golang.org/x/sys/windows"
)

const (
	syncLockHelperModeEnv = "BD_TEST_LINEAR_SYNC_LOCK_HELPER_MODE"
	syncLockHelperDirEnv  = "BD_TEST_LINEAR_SYNC_LOCK_HELPER_DIR"
)

func TestSyncLockWindowsRecordRoundTripAndStrictValidation(t *testing.T) {
	record := currentSyncLockRecord(t, generationForTest(0x11))
	encoded := record.marshal()

	parsed, ok := parseSyncLockRecord(encoded)
	if !ok {
		t.Fatal("valid record did not parse")
	}
	if parsed.info.PID != record.info.PID ||
		parsed.info.Started.UnixNano() != record.info.Started.UnixNano() ||
		parsed.processCreated != record.processCreated ||
		parsed.generation != record.generation {
		t.Fatalf("record changed across round trip: got %+v, want %+v", parsed, record)
	}

	mutations := map[string]func(*[syncLockRecordSize]byte){
		"magic":       func(data *[syncLockRecordSize]byte) { data[syncLockRecordMagicOffset] ^= 0xff },
		"version":     func(data *[syncLockRecordSize]byte) { data[syncLockRecordVersionOffset]++ },
		"record size": func(data *[syncLockRecordSize]byte) { data[syncLockRecordSizeOffset]-- },
		"zero PID":    func(data *[syncLockRecordSize]byte) { clear(data[syncLockRecordPIDOffset:syncLockRecordStartedOffset]) },
		"zero lock start": func(data *[syncLockRecordSize]byte) {
			clear(data[syncLockRecordStartedOffset:syncLockRecordProcessCreatedOffset])
		},
		"zero process creation": func(data *[syncLockRecordSize]byte) {
			clear(data[syncLockRecordProcessCreatedOffset:syncLockRecordGenerationAOffset])
		},
		"zero generation": func(data *[syncLockRecordSize]byte) {
			clear(data[syncLockRecordGenerationAOffset:syncLockRecordChecksumOffset])
		},
		"duplicate generation mismatch": func(data *[syncLockRecordSize]byte) { data[syncLockRecordGenerationBOffset] ^= 0xff },
		"nonzero padding":               func(data *[syncLockRecordSize]byte) { data[syncLockRecordPaddingOffset] = 1 },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			corrupt := encoded
			mutate(&corrupt)
			resignSyncLockRecord(&corrupt)
			if _, ok := parseSyncLockRecord(corrupt); ok {
				t.Fatal("invalid record parsed successfully")
			}
		})
	}

	t.Run("checksum", func(t *testing.T) {
		corrupt := encoded
		corrupt[syncLockRecordChecksumOffset] ^= 0xff
		if _, ok := parseSyncLockRecord(corrupt); ok {
			t.Fatal("checksum mismatch parsed successfully")
		}
	})

	t.Run("weak textual sidecar", func(t *testing.T) {
		var weak [syncLockRecordSize]byte
		copy(weak[:], "pid=1234\nstarted=2026-01-01T00:00:00Z\n")
		if _, ok := parseSyncLockRecord(weak); ok {
			t.Fatal("weak textual sidecar parsed as a v2 record")
		}
	})

	t.Run("partial record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), syncLockInfoFilename)
		if err := os.WriteFile(path, encoded[:syncLockRecordPaddingOffset], 0600); err != nil {
			t.Fatalf("write partial record: %v", err)
		}
		f, err := os.Open(path) // #nosec G304 -- test path is under t.TempDir.
		if err != nil {
			t.Fatalf("open partial record: %v", err)
		}
		defer f.Close()
		if _, _, ok := readSyncLockRecord(f); ok {
			t.Fatal("partial record parsed successfully")
		}
	})
}

func TestSyncLockWindowsLeaseOffsetBounds(t *testing.T) {
	maxOffset := syncLockLeaseOffsetBase + syncLockLeaseOffsetMask
	for _, seed := range []byte{0x01, 0x7f, 0xff} {
		offset := generationForTest(seed).leaseOffset()
		if offset < syncLockLeaseOffsetBase || offset > maxOffset {
			t.Fatalf("generation %#x lease offset = %d, want [%d, %d]",
				seed, offset, syncLockLeaseOffsetBase, maxOffset)
		}
		if offset < syncLockRecordSize {
			t.Fatalf("generation %#x lease offset %d overlaps the %d-byte metadata record",
				seed, offset, syncLockRecordSize)
		}
	}
}

func TestSyncLockWindowsOverlappedOffsetSplit(t *testing.T) {
	const offset = uint64(0x1122334455667788)
	overlapped := overlappedAt(offset)
	if overlapped.Offset != 0x55667788 {
		t.Fatalf("low offset = %#x, want %#x", overlapped.Offset, uint32(0x55667788))
	}
	if overlapped.OffsetHigh != 0x11223344 {
		t.Fatalf("high offset = %#x, want %#x", overlapped.OffsetHigh, uint32(0x11223344))
	}
}

func TestSyncLockWindowsValidOwnerLifecycle(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSyncLock(dir, false)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if lock.metadata.file == nil || !lock.metadata.leaseHeld {
		t.Fatal("Windows owner did not retain its generation lease")
	}

	record, _, ok := readSyncLockRecord(lock.metadata.file)
	if !ok {
		t.Fatal("published record did not validate")
	}
	if record.info.PID != os.Getpid() {
		t.Fatalf("record PID = %d, want %d", record.info.PID, os.Getpid())
	}
	created, err := processCreationFiletime(windows.CurrentProcess())
	if err != nil {
		t.Fatalf("query current process creation time: %v", err)
	}
	if record.processCreated != created {
		t.Fatalf("record process creation = %d, want %d", record.processCreated, created)
	}

	held := contendForSyncLock(t, dir)
	if held.Info == nil || held.Info.PID != os.Getpid() ||
		held.Info.Started.UnixNano() != record.info.Started.UnixNano() {
		t.Fatalf("valid owner diagnostics = %+v, want %+v", held.Info, record.info)
	}

	infoPath := filepath.Join(dir, syncLockInfoFilename)
	leaseOffset := record.generation.leaseOffset()
	if err := lock.Release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}

	sidecar, err := os.Open(infoPath) // #nosec G304 -- test path is under t.TempDir.
	if err != nil {
		t.Fatalf("open released sidecar: %v", err)
	}
	defer sidecar.Close()
	stat, err := sidecar.Stat()
	if err != nil {
		t.Fatalf("stat released sidecar: %v", err)
	}
	if stat.Size() != syncLockRecordSize {
		t.Fatalf("sidecar size = %d, want fixed metadata size %d", stat.Size(), syncLockRecordSize)
	}
	var cleared [syncLockRecordSize]byte
	if n, err := sidecar.ReadAt(cleared[:], 0); err != nil || n != len(cleared) {
		t.Fatalf("read cleared sidecar: n=%d err=%v", n, err)
	}
	if cleared != [syncLockRecordSize]byte{} {
		t.Fatal("release did not invalidate the fixed metadata region")
	}
	active, err := probeSyncGenerationLease(sidecar, leaseOffset)
	if err != nil {
		t.Fatalf("probe released generation: %v", err)
	}
	if active {
		t.Fatal("generation lease remained active after release")
	}
}

func TestSyncLockMetadataReaderDoesNotBlockLifecycle(t *testing.T) {
	dir := t.TempDir()
	seed, err := AcquireSyncLock(dir, false)
	if err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	if err := seed.Release(); err != nil {
		t.Fatalf("release seed lock: %v", err)
	}

	infoPath := filepath.Join(dir, syncLockInfoFilename)
	reader, err := os.Open(infoPath) // #nosec G304 -- test path is under t.TempDir.
	if err != nil {
		t.Fatalf("open persistent reader: %v", err)
	}
	defer reader.Close()

	lock, err := AcquireSyncLock(dir, false)
	if err != nil {
		t.Fatalf("open reader blocked publication: %v", err)
	}
	if _, _, ok := readSyncLockRecord(reader); !ok {
		t.Fatal("persistent reader did not observe the published record")
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("open reader blocked invalidation or release: %v", err)
	}

	var cleared [syncLockRecordSize]byte
	if n, err := reader.ReadAt(cleared[:], 0); err != nil || n != len(cleared) {
		t.Fatalf("read invalidated record through persistent reader: n=%d err=%v", n, err)
	}
	if cleared != [syncLockRecordSize]byte{} {
		t.Fatal("persistent reader did not observe record invalidation")
	}
}

func TestAcquireSyncLockWindowsRejectsUncorrelatedMetadata(t *testing.T) {
	t.Run("missing sidecar", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		assertGenericSyncLockContention(t, dir)
	})

	t.Run("weak sidecar record", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		path := filepath.Join(dir, syncLockInfoFilename)
		if err := os.WriteFile(path, []byte("pid=1234\nstarted=2026-01-01T00:00:00Z\n"), 0600); err != nil {
			t.Fatalf("write weak record: %v", err)
		}
		assertGenericSyncLockContention(t, dir)
	})

	t.Run("partial record", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		encoded := currentSyncLockRecord(t, generationForTest(0x21)).marshal()
		path := filepath.Join(dir, syncLockInfoFilename)
		if err := os.WriteFile(path, encoded[:syncLockRecordPaddingOffset], 0600); err != nil {
			t.Fatalf("write partial record: %v", err)
		}
		assertGenericSyncLockContention(t, dir)
	})

	t.Run("corrupt record", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		encoded := currentSyncLockRecord(t, generationForTest(0x22)).marshal()
		encoded[syncLockRecordChecksumOffset] ^= 0xff
		writeSyncLockRecordForTest(t, dir, encoded)
		assertGenericSyncLockContention(t, dir)
	})

	t.Run("old primary owner with live stale strong sidecar", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		encoded := currentSyncLockRecord(t, generationForTest(0x23)).marshal()
		writeSyncLockRecordForTest(t, dir, encoded)
		assertGenericSyncLockContention(t, dir)
	})

	t.Run("matching lease with process creation mismatch", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		record := currentSyncLockRecord(t, generationForTest(0x24))
		record.processCreated++
		writeSyncLockRecordForTest(t, dir, record.marshal())
		holdGenerationLeaseForTest(t, dir, record.generation)
		assertGenericSyncLockContention(t, dir)
	})

	t.Run("different generation lease", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		record := currentSyncLockRecord(t, generationForTest(0x25))
		writeSyncLockRecordForTest(t, dir, record.marshal())
		holdGenerationLeaseForTest(t, dir, generationForTest(0x26))
		assertGenericSyncLockContention(t, dir)
	})

	t.Run("dead PID with matching lease", func(t *testing.T) {
		dir := t.TempDir()
		holdPrimarySyncLock(t, dir)
		record := currentSyncLockRecord(t, generationForTest(0x27))
		record.info.PID = 2147483647
		writeSyncLockRecordForTest(t, dir, record.marshal())
		holdGenerationLeaseForTest(t, dir, record.generation)
		assertGenericSyncLockContention(t, dir)
	})
}

func TestSyncLockWindowsSharedProbesCannotAuthenticateEachOther(t *testing.T) {
	path := filepath.Join(t.TempDir(), syncLockInfoFilename)
	if err := os.WriteFile(path, make([]byte, syncLockRecordSize), 0600); err != nil {
		t.Fatalf("create sidecar: %v", err)
	}
	first, err := os.Open(path) // #nosec G304 -- test path is under t.TempDir.
	if err != nil {
		t.Fatalf("open first probe: %v", err)
	}
	defer first.Close()
	second, err := os.Open(path) // #nosec G304 -- test path is under t.TempDir.
	if err != nil {
		t.Fatalf("open second probe: %v", err)
	}
	defer second.Close()

	offset := generationForTest(0x31).leaseOffset()
	firstAcquired, err := tryLockSyncGenerationByte(first, offset, false)
	if err != nil || !firstAcquired {
		t.Fatalf("first shared probe: acquired=%v err=%v", firstAcquired, err)
	}
	defer unlockSyncGenerationByte(first, offset)

	secondAcquired, err := tryLockSyncGenerationByte(second, offset, false)
	if err != nil || !secondAcquired {
		t.Fatalf("second shared probe did not coexist: acquired=%v err=%v", secondAcquired, err)
	}
	if err := unlockSyncGenerationByte(second, offset); err != nil {
		t.Fatalf("unlock second shared probe: %v", err)
	}
}

func TestSyncLockWindowsConcurrentContendersRejectFreeStaleGeneration(t *testing.T) {
	dir := t.TempDir()
	holdPrimarySyncLock(t, dir)
	record := currentSyncLockRecord(t, generationForTest(0x32))
	writeSyncLockRecordForTest(t, dir, record.marshal())

	const contenders = 32
	start := make(chan struct{})
	results := make(chan error, contenders)
	var ready sync.WaitGroup
	ready.Add(contenders)
	for range contenders {
		go func() {
			ready.Done()
			<-start
			_, err := AcquireSyncLock(dir, false)
			results <- err
		}()
	}
	ready.Wait()
	close(start)

	for range contenders {
		err := <-results
		held, ok := err.(*SyncLockHeldError)
		if !ok {
			t.Fatalf("contender error = %T %v, want SyncLockHeldError", err, err)
		}
		if held.Info != nil {
			t.Fatalf("concurrent shared probe authenticated stale metadata: %+v", held.Info)
		}
	}
}

func TestSyncLockWindowsDiagnosticFailuresDoNotAffectPrimary(t *testing.T) {
	t.Run("sidecar open failure", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, syncLockInfoFilename), 0700); err != nil {
			t.Fatalf("create directory at sidecar path: %v", err)
		}
		lock, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("sidecar open failure prevented primary acquisition: %v", err)
		}
		assertGenericSyncLockContention(t, dir)
		if err := lock.Release(); err != nil {
			t.Fatalf("sidecar open failure prevented primary release: %v", err)
		}
	})

	t.Run("all generation bytes busy", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("create lock directory: %v", err)
		}
		path := filepath.Join(dir, syncLockInfoFilename)
		blocker, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- test path is under t.TempDir.
		if err != nil {
			t.Fatalf("open sidecar blocker: %v", err)
		}
		if err := lockfile.FlockExclusiveNonBlocking(blocker); err != nil {
			_ = blocker.Close()
			t.Fatalf("lock full sidecar range: %v", err)
		}
		defer func() {
			_ = lockfile.FlockUnlock(blocker)
			_ = blocker.Close()
		}()

		lock, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("lease collision prevented primary acquisition: %v", err)
		}
		if lock.metadata.file != nil {
			t.Fatal("diagnostic metadata survived exhaustive lease collision")
		}
		assertGenericSyncLockContention(t, dir)
		if err := lock.Release(); err != nil {
			t.Fatalf("lease collision prevented primary release: %v", err)
		}
	})

	t.Run("short metadata write", func(t *testing.T) {
		dir := t.TempDir()
		primary := holdPrimarySyncLock(t, dir)
		path := filepath.Join(dir, syncLockInfoFilename)
		generation := generationForTest(0x41)
		ops := defaultSyncLockWindowsOps
		ops.readRandom = deterministicGenerationReader(generation)
		ops.writeAt = func(f *os.File, data []byte, offset int64) (int, error) {
			if len(data) == syncLockRecordSize && [8]byte(data[:8]) == syncLockRecordMagic {
				n, _ := f.WriteAt(data[:syncLockRecordPaddingOffset], offset)
				return n, io.ErrShortWrite
			}
			return f.WriteAt(data, offset)
		}

		metadata := publishSyncLockInfoWithOps(path, ops)
		if metadata.file != nil {
			t.Fatal("short record write retained a diagnostic lease")
		}
		assertGenericSyncLockContention(t, dir)
		sidecar, err := os.Open(path) // #nosec G304 -- test path is under t.TempDir.
		if err != nil {
			t.Fatalf("open failed-publication sidecar: %v", err)
		}
		defer sidecar.Close()
		active, err := probeSyncGenerationLease(sidecar, generation.leaseOffset())
		if err != nil || active {
			t.Fatalf("failed publication retained lease: active=%v err=%v", active, err)
		}
		runtime.KeepAlive(primary)
	})

	t.Run("clear unlock and close errors", func(t *testing.T) {
		dir := t.TempDir()
		primary := holdPrimarySyncLock(t, dir)
		path := filepath.Join(dir, syncLockInfoFilename)
		generation := generationForTest(0x42)
		releasing := false
		ops := defaultSyncLockWindowsOps
		ops.readRandom = deterministicGenerationReader(generation)
		ops.writeAt = func(f *os.File, data []byte, offset int64) (int, error) {
			if releasing {
				return 0, errors.New("injected clear failure")
			}
			return f.WriteAt(data, offset)
		}
		ops.unlockGenerationByte = func(f *os.File, offset uint64) error {
			err := unlockSyncGenerationByte(f, offset)
			if err != nil {
				return err
			}
			return errors.New("injected unlock report")
		}
		ops.closeFile = func(f *os.File) error {
			err := f.Close()
			if err != nil {
				return err
			}
			return errors.New("injected close report")
		}

		metadata := publishSyncLockInfoWithOps(path, ops)
		if metadata.file == nil {
			t.Fatal("failed to publish fixture metadata")
		}
		releasing = true
		if err := clearSyncLockInfo(primary, path, &metadata); err != nil {
			t.Fatalf("diagnostic release errors escaped helper: %v", err)
		}
		if metadata.file != nil {
			t.Fatal("diagnostic metadata retained a closed handle")
		}
		assertGenericSyncLockContention(t, dir)
	})
}

func TestSyncLockWindowsReleaseInvalidatesBeforePrimaryUnlock(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSyncLock(dir, false)
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}
	if err := clearSyncLockInfo(lock.file, lock.infoPath, &lock.metadata); err != nil {
		t.Fatalf("invalidate diagnostics: %v", err)
	}
	assertGenericSyncLockContention(t, dir)
	if err := lock.Release(); err != nil {
		t.Fatalf("release primary after diagnostic phase: %v", err)
	}
}

func TestSyncLockWindowsSubprocessPhases(t *testing.T) {
	if os.Getenv(syncLockHelperModeEnv) != "" {
		t.Skip("parent-only test")
	}

	tests := []struct {
		mode       string
		expectInfo bool
	}{
		{mode: "primary"},
		{mode: "lease"},
		{mode: "partial"},
		{mode: "complete", expectInfo: true},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			dir := t.TempDir()
			helper := startSyncLockHelper(t, dir, tt.mode)
			defer helper.stop()

			held := contendForSyncLock(t, dir)
			if tt.expectInfo {
				if held.Info == nil || held.Info.PID != helper.cmd.Process.Pid {
					t.Fatalf("complete helper diagnostics = %+v, want PID %d", held.Info, helper.cmd.Process.Pid)
				}
			} else if held.Info != nil {
				t.Fatalf("%s phase exposed uncorrelated metadata: %+v", tt.mode, held.Info)
			}

			helper.stop()
			lock := acquireSyncLockEventually(t, dir, 5*time.Second)
			if err := lock.Release(); err != nil {
				t.Fatalf("release successor after %s crash: %v", tt.mode, err)
			}
		})
	}
}

func TestSyncLockWindowsSubprocessHelper(t *testing.T) {
	mode := os.Getenv(syncLockHelperModeEnv)
	if mode == "" {
		return
	}
	dir := os.Getenv(syncLockHelperDirEnv)
	if dir == "" {
		t.Fatal("helper directory is empty")
	}

	var primary, sidecar *os.File
	var owner *SyncLock
	var leaseOffset uint64
	switch mode {
	case "complete":
		var err error
		owner, err = AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("helper acquire complete owner: %v", err)
		}
		defer owner.Release()
	case "primary", "lease", "partial":
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("helper create directory: %v", err)
		}
		var err error
		primary, err = os.OpenFile(filepath.Join(dir, syncLockFilename), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatalf("helper open primary: %v", err)
		}
		if err := lockfile.FlockExclusiveNonBlocking(primary); err != nil {
			t.Fatalf("helper lock primary: %v", err)
		}
		defer func() {
			_ = lockfile.FlockUnlock(primary)
			_ = primary.Close()
		}()

		if mode != "primary" {
			sidecar, err = os.OpenFile(filepath.Join(dir, syncLockInfoFilename), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatalf("helper open sidecar: %v", err)
			}
			generation := generationForTest(0x51)
			leaseOffset = generation.leaseOffset()
			acquired, err := tryLockSyncGenerationByte(sidecar, leaseOffset, true)
			if err != nil || !acquired {
				t.Fatalf("helper acquire generation: acquired=%v err=%v", acquired, err)
			}
			defer func() {
				_ = unlockSyncGenerationByte(sidecar, leaseOffset)
				_ = sidecar.Close()
			}()

			if mode == "partial" {
				record := currentSyncLockRecord(t, generation)
				encoded := record.marshal()
				if n, err := sidecar.WriteAt(encoded[:syncLockRecordPaddingOffset], 0); err != nil || n != syncLockRecordPaddingOffset {
					t.Fatalf("helper write partial record: n=%d err=%v", n, err)
				}
			}
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}

	fmt.Println("READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
	runtime.KeepAlive(owner)
	runtime.KeepAlive(primary)
	runtime.KeepAlive(sidecar)
	runtime.KeepAlive(leaseOffset)
}

func TestSyncLockWindowsRepeatedHandoffs(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		lock, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("iteration %d acquire: %v", i, err)
		}
		held := contendForSyncLock(t, dir)
		if held.Info == nil || held.Info.PID != os.Getpid() {
			t.Fatalf("iteration %d diagnostics = %+v", i, held.Info)
		}
		if err := lock.Release(); err != nil {
			t.Fatalf("iteration %d release: %v", i, err)
		}
	}
}

func currentSyncLockRecord(t *testing.T, generation syncLockGeneration) syncLockRecord {
	t.Helper()
	created, err := processCreationFiletime(windows.CurrentProcess())
	if err != nil {
		t.Fatalf("query current process creation: %v", err)
	}
	return syncLockRecord{
		info: SyncLockInfo{
			PID:     os.Getpid(),
			Started: time.Now().UTC(),
		},
		processCreated: created,
		generation:     generation,
	}
}

func generationForTest(value byte) syncLockGeneration {
	var generation syncLockGeneration
	for i := range generation {
		generation[i] = value
	}
	return generation
}

func resignSyncLockRecord(data *[syncLockRecordSize]byte) {
	clear(data[syncLockRecordChecksumOffset:syncLockRecordPaddingOffset])
	checksum := sha256.Sum256(data[:])
	copy(data[syncLockRecordChecksumOffset:], checksum[:])
}

func writeSyncLockRecordForTest(t *testing.T, dir string, encoded [syncLockRecordSize]byte) {
	t.Helper()
	path := filepath.Join(dir, syncLockInfoFilename)
	if err := os.WriteFile(path, encoded[:], 0600); err != nil {
		t.Fatalf("write sidecar record: %v", err)
	}
}

func holdPrimarySyncLock(t *testing.T, dir string) *os.File {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create lock directory: %v", err)
	}
	path := filepath.Join(dir, syncLockFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- test path is under t.TempDir.
	if err != nil {
		t.Fatalf("open primary lock: %v", err)
	}
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		t.Fatalf("acquire primary lock: %v", err)
	}
	t.Cleanup(func() {
		_ = lockfile.FlockUnlock(f)
		_ = f.Close()
	})
	return f
}

func holdGenerationLeaseForTest(t *testing.T, dir string, generation syncLockGeneration) *os.File {
	t.Helper()
	path := filepath.Join(dir, syncLockInfoFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- test path is under t.TempDir.
	if err != nil {
		t.Fatalf("open sidecar lease: %v", err)
	}
	offset := generation.leaseOffset()
	acquired, err := tryLockSyncGenerationByte(f, offset, true)
	if err != nil || !acquired {
		_ = f.Close()
		t.Fatalf("acquire sidecar lease: acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(func() {
		_ = unlockSyncGenerationByte(f, offset)
		_ = f.Close()
	})
	return f
}

func contendForSyncLock(t *testing.T, dir string) *SyncLockHeldError {
	t.Helper()
	_, err := AcquireSyncLock(dir, false)
	if err == nil {
		t.Fatal("acquire should fail while primary lock is held")
	}
	held, ok := err.(*SyncLockHeldError)
	if !ok {
		t.Fatalf("contention error = %T %v, want SyncLockHeldError", err, err)
	}
	return held
}

func assertGenericSyncLockContention(t *testing.T, dir string) {
	t.Helper()
	held := contendForSyncLock(t, dir)
	if held.Info != nil {
		t.Fatalf("uncorrelated metadata produced owner diagnostics: %+v", held.Info)
	}
}

func deterministicGenerationReader(generation syncLockGeneration) func([]byte) (int, error) {
	return func(destination []byte) (int, error) {
		return copy(destination, generation[:]), nil
	}
}

type syncLockHelperProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *bytes.Buffer
}

func startSyncLockHelper(t *testing.T, dir, mode string) *syncLockHelperProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSyncLockWindowsSubprocessHelper$")
	cmd.Env = append(os.Environ(),
		syncLockHelperModeEnv+"="+mode,
		syncLockHelperDirEnv+"="+dir,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("create helper stdout: %v", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	select {
	case line := <-ready:
		if line != "READY" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("helper readiness = %q, stderr=%s", line, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper readiness timed out, stderr=%s", stderr.String())
	}

	return &syncLockHelperProcess{cmd: cmd, stdin: stdin, stderr: stderr}
}

func (helper *syncLockHelperProcess) stop() {
	if helper == nil || helper.cmd == nil {
		return
	}
	_ = helper.cmd.Process.Kill()
	_ = helper.stdin.Close()
	_ = helper.cmd.Wait()
	helper.cmd = nil
}

func acquireSyncLockEventually(t *testing.T, dir string, timeout time.Duration) *SyncLock {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		lock, err := AcquireSyncLock(dir, false)
		if err == nil {
			return lock
		}
		if _, ok := err.(*SyncLockHeldError); !ok {
			t.Fatalf("successor acquire failed: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("primary lock remained busy after helper exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
