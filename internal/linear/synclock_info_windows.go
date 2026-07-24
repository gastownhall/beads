//go:build windows

package linear

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	syncLockInfoFilename = ".linear-sync.info.lock"

	syncLockRecordSize           = 512
	syncLockRecordVersion uint16 = 2

	syncLockRecordMagicOffset          = 0
	syncLockRecordVersionOffset        = 8
	syncLockRecordSizeOffset           = 10
	syncLockRecordPIDOffset            = 12
	syncLockRecordStartedOffset        = 16
	syncLockRecordProcessCreatedOffset = 24
	syncLockRecordGenerationAOffset    = 32
	syncLockRecordGenerationBOffset    = 48
	syncLockRecordChecksumOffset       = 64
	syncLockRecordPaddingOffset        = 96

	syncLockGenerationSize    = 16
	syncLockChecksumSize      = sha256.Size
	syncLockLeaseOffsetBase   = uint64(4096)
	syncLockLeaseOffsetMask   = uint64(1<<62 - 1)
	syncLockGenerationRetries = 8
)

var syncLockRecordMagic = [8]byte{'B', 'D', 'L', 'I', 'N', 'F', 'O', '2'}

type syncLockGeneration [syncLockGenerationSize]byte

type syncLockRecord struct {
	info           SyncLockInfo
	processCreated uint64
	generation     syncLockGeneration
}

type syncLockMetadata struct {
	file        *os.File
	leaseOffset uint64
	leaseHeld   bool
	ops         syncLockWindowsOps
}

type syncLockWindowsOps struct {
	openFile               func(string, int, os.FileMode) (*os.File, error)
	readRandom             func([]byte) (int, error)
	writeAt                func(*os.File, []byte, int64) (int, error)
	tryLockGenerationByte  func(*os.File, uint64, bool) (bool, error)
	unlockGenerationByte   func(*os.File, uint64) error
	closeFile              func(*os.File) error
	currentProcessCreation func() (uint64, error)
}

var defaultSyncLockWindowsOps = syncLockWindowsOps{
	openFile:   os.OpenFile,
	readRandom: rand.Read,
	writeAt: func(f *os.File, data []byte, offset int64) (int, error) {
		return f.WriteAt(data, offset)
	},
	tryLockGenerationByte: tryLockSyncGenerationByte,
	unlockGenerationByte:  unlockSyncGenerationByte,
	closeFile: func(f *os.File) error {
		return f.Close()
	},
	currentProcessCreation: func() (uint64, error) {
		return processCreationFiletime(windows.CurrentProcess())
	},
}

func syncLockMetadataPath(beadsDir, _ string) string {
	return filepath.Join(beadsDir, syncLockInfoFilename)
}

func publishSyncLockInfo(primary *os.File, path string) (syncLockMetadata, error) {
	// Inline metadata cannot be read through Windows' full-range primary lock.
	// Clearing legacy content is advisory and must not affect primary ownership.
	_ = primary.Truncate(0)
	return publishSyncLockInfoWithOps(path, defaultSyncLockWindowsOps), nil
}

func publishSyncLockInfoWithOps(path string, ops syncLockWindowsOps) syncLockMetadata {
	sidecar, err := ops.openFile(path, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- path is constrained to the beads directory.
	if err != nil {
		return syncLockMetadata{}
	}

	// Once the primary is ours, no cooperating owner can still own its old
	// diagnostic generation. Invalidate that record before preparing the next
	// generation so setup gaps always degrade to generic contention.
	var zeros [syncLockRecordSize]byte
	_, _ = ops.writeAt(sidecar, zeros[:], 0)

	processCreated, err := ops.currentProcessCreation()
	if err != nil || processCreated == 0 {
		_ = ops.closeFile(sidecar)
		return syncLockMetadata{}
	}

	started := time.Now().UTC()
	for range syncLockGenerationRetries {
		var generation syncLockGeneration
		n, randomErr := ops.readRandom(generation[:])
		if randomErr != nil || n != len(generation) || generation.isZero() {
			_ = ops.closeFile(sidecar)
			return syncLockMetadata{}
		}

		leaseOffset := generation.leaseOffset()
		acquired, lockErr := ops.tryLockGenerationByte(sidecar, leaseOffset, true)
		if lockErr != nil {
			_ = ops.closeFile(sidecar)
			return syncLockMetadata{}
		}
		if !acquired {
			continue
		}

		record := syncLockRecord{
			info: SyncLockInfo{
				PID:     os.Getpid(),
				Started: started,
			},
			processCreated: processCreated,
			generation:     generation,
		}
		encoded := record.marshal()
		written, writeErr := ops.writeAt(sidecar, encoded[:], 0)
		if writeErr != nil || written != len(encoded) {
			_, _ = ops.writeAt(sidecar, zeros[:], 0)
			_ = ops.unlockGenerationByte(sidecar, leaseOffset)
			_ = ops.closeFile(sidecar)
			return syncLockMetadata{}
		}

		return syncLockMetadata{
			file:        sidecar,
			leaseOffset: leaseOffset,
			leaseHeld:   true,
			ops:         ops,
		}
	}

	_ = ops.closeFile(sidecar)
	return syncLockMetadata{}
}

func clearSyncLockInfo(_ *os.File, _ string, metadata *syncLockMetadata) error {
	if metadata == nil || metadata.file == nil {
		return nil
	}

	// Invalidate the record before releasing its generation lease. Every step is
	// best-effort and completes before the authoritative primary is released.
	var zeros [syncLockRecordSize]byte
	_, _ = metadata.ops.writeAt(metadata.file, zeros[:], 0)
	if metadata.leaseHeld {
		_ = metadata.ops.unlockGenerationByte(metadata.file, metadata.leaseOffset)
		metadata.leaseHeld = false
	}
	_ = metadata.ops.closeFile(metadata.file)
	metadata.file = nil
	return nil
}

func readContendedSyncLockInfo(path string) *SyncLockInfo {
	sidecar, err := os.Open(path) // #nosec G304 -- path is constrained to the beads directory.
	if err != nil {
		return nil
	}
	defer func() { _ = sidecar.Close() }()

	record, first, ok := readSyncLockRecord(sidecar)
	if !ok {
		return nil
	}

	process, ok := openMatchingSyncLockProcess(record)
	if !ok {
		return nil
	}
	defer func() { _ = windows.CloseHandle(process) }()

	active, err := probeSyncGenerationLease(sidecar, record.generation.leaseOffset())
	if err != nil || !active {
		return nil
	}

	secondRecord, second, ok := readSyncLockRecord(sidecar)
	if !ok || first != second || record.generation != secondRecord.generation {
		return nil
	}

	active, err = probeSyncGenerationLease(sidecar, secondRecord.generation.leaseOffset())
	if err != nil || !active || !processHandleIsRunning(process) {
		return nil
	}

	info := record.info
	return &info
}

func (record syncLockRecord) marshal() [syncLockRecordSize]byte {
	var encoded [syncLockRecordSize]byte
	copy(encoded[syncLockRecordMagicOffset:], syncLockRecordMagic[:])
	binary.LittleEndian.PutUint16(encoded[syncLockRecordVersionOffset:], syncLockRecordVersion)
	binary.LittleEndian.PutUint16(encoded[syncLockRecordSizeOffset:], syncLockRecordSize)
	binary.LittleEndian.PutUint32(encoded[syncLockRecordPIDOffset:], uint32(record.info.PID))
	binary.LittleEndian.PutUint64(encoded[syncLockRecordStartedOffset:], uint64(record.info.Started.UnixNano()))
	binary.LittleEndian.PutUint64(encoded[syncLockRecordProcessCreatedOffset:], record.processCreated)
	copy(encoded[syncLockRecordGenerationAOffset:], record.generation[:])
	copy(encoded[syncLockRecordGenerationBOffset:], record.generation[:])

	checksum := sha256.Sum256(encoded[:])
	copy(encoded[syncLockRecordChecksumOffset:], checksum[:])
	return encoded
}

func readSyncLockRecord(f *os.File) (syncLockRecord, [syncLockRecordSize]byte, bool) {
	var encoded [syncLockRecordSize]byte
	n, err := f.ReadAt(encoded[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return syncLockRecord{}, encoded, false
	}
	if n != len(encoded) {
		return syncLockRecord{}, encoded, false
	}

	record, ok := parseSyncLockRecord(encoded)
	return record, encoded, ok
}

func parseSyncLockRecord(encoded [syncLockRecordSize]byte) (syncLockRecord, bool) {
	if [8]byte(encoded[syncLockRecordMagicOffset:syncLockRecordVersionOffset]) != syncLockRecordMagic {
		return syncLockRecord{}, false
	}
	if binary.LittleEndian.Uint16(encoded[syncLockRecordVersionOffset:]) != syncLockRecordVersion ||
		binary.LittleEndian.Uint16(encoded[syncLockRecordSizeOffset:]) != syncLockRecordSize {
		return syncLockRecord{}, false
	}

	pid := binary.LittleEndian.Uint32(encoded[syncLockRecordPIDOffset:])
	startedUnixNano := int64(binary.LittleEndian.Uint64(encoded[syncLockRecordStartedOffset:]))
	processCreated := binary.LittleEndian.Uint64(encoded[syncLockRecordProcessCreatedOffset:])
	if pid == 0 || pid > uint32(1<<31-1) || startedUnixNano <= 0 || processCreated == 0 {
		return syncLockRecord{}, false
	}

	var generationA, generationB syncLockGeneration
	copy(generationA[:], encoded[syncLockRecordGenerationAOffset:syncLockRecordGenerationBOffset])
	copy(generationB[:], encoded[syncLockRecordGenerationBOffset:syncLockRecordChecksumOffset])
	if generationA.isZero() || generationA != generationB {
		return syncLockRecord{}, false
	}

	for _, value := range encoded[syncLockRecordPaddingOffset:] {
		if value != 0 {
			return syncLockRecord{}, false
		}
	}

	var checksum [syncLockChecksumSize]byte
	copy(checksum[:], encoded[syncLockRecordChecksumOffset:syncLockRecordPaddingOffset])
	checksumInput := encoded
	clear(checksumInput[syncLockRecordChecksumOffset:syncLockRecordPaddingOffset])
	expected := sha256.Sum256(checksumInput[:])
	if checksum != expected {
		return syncLockRecord{}, false
	}

	return syncLockRecord{
		info: SyncLockInfo{
			PID:     int(pid),
			Started: time.Unix(0, startedUnixNano).UTC(),
		},
		processCreated: processCreated,
		generation:     generationA,
	}, true
}

func (generation syncLockGeneration) isZero() bool {
	return generation == syncLockGeneration{}
}

func (generation syncLockGeneration) leaseOffset() uint64 {
	digest := sha256.Sum256(generation[:])
	return syncLockLeaseOffsetBase + (binary.LittleEndian.Uint64(digest[:8]) & syncLockLeaseOffsetMask)
}

func tryLockSyncGenerationByte(f *os.File, offset uint64, exclusive bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0,
		1,
		0,
		overlappedAt(offset),
	)
	if err == nil {
		return true, nil
	}
	if isSyncGenerationLockConflict(err) {
		return false, nil
	}
	return false, err
}

func unlockSyncGenerationByte(f *os.File, offset uint64) error {
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		1,
		0,
		overlappedAt(offset),
	)
}

func probeSyncGenerationLease(f *os.File, offset uint64) (bool, error) {
	acquired, err := tryLockSyncGenerationByte(f, offset, false)
	if err != nil {
		return false, err
	}
	if !acquired {
		return true, nil
	}
	if err := unlockSyncGenerationByte(f, offset); err != nil {
		return false, err
	}
	return false, nil
}

func isSyncGenerationLockConflict(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, syscall.EWOULDBLOCK)
}

func overlappedAt(offset uint64) *windows.Overlapped {
	return &windows.Overlapped{
		Offset:     uint32(offset),
		OffsetHigh: uint32(offset >> 32),
	}
}

func processCreationFiletime(process windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

func openMatchingSyncLockProcess(record syncLockRecord) (windows.Handle, bool) {
	process, err := windows.OpenProcess(
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(record.info.PID),
	)
	if err != nil {
		return 0, false
	}

	created, err := processCreationFiletime(process)
	if err != nil || created != record.processCreated || !processHandleIsRunning(process) {
		_ = windows.CloseHandle(process)
		return 0, false
	}
	return process, true
}

func processHandleIsRunning(process windows.Handle) bool {
	status, err := windows.WaitForSingleObject(process, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
