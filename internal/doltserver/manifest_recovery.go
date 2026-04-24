package doltserver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// corruptManifestSignature is the error emitted by dolt sql-server when its
	// manifest references a root hash that was never flushed to disk (typically
	// after an unclean shutdown). See GH#3290.
	corruptManifestSignature = "root hash doesn't exist"

	// corruptJournalSignature is emitted by dolt sql-server when the journal
	// contains damaged blocks. Unlike the empty-manifest case, Dolt may still
	// have user data to recover, so bd must not run destructive repair
	// automatically. See GH#2559.
	corruptJournalSignature = "corrupted journal"
)

// logTailBytes is the size of the tail scanned when looking for the corrupt
// manifest signature in the dolt server log. 64 KiB comfortably covers the
// last few startup attempts without loading huge log files into memory.
const logTailBytes = 64 * 1024

// logHasCorruptManifestError returns true if the tail of the dolt server log
// contains the corrupt-manifest signature.
func logHasCorruptManifestError(logPath string) (bool, error) {
	return logHasSignature(logPath, corruptManifestSignature)
}

// logHasCorruptJournalError returns true if the tail of the dolt server log
// contains Dolt's journal-corruption signature.
func logHasCorruptJournalError(logPath string) (bool, error) {
	return logHasSignature(logPath, corruptJournalSignature)
}

func logHasSignature(logPath, signature string) (bool, error) {
	f, err := os.Open(logPath) //nolint:gosec // G304: path derived from beadsDir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, err
	}

	start := int64(0)
	if info.Size() > logTailBytes {
		start = info.Size() - logTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return false, err
	}

	buf, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(buf), signature), nil
}

func corruptJournalRecoveryHint(beadsDir string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	doltDir := filepath.Join(beadsDir, "dolt")
	backupDir := filepath.Join(beadsDir, "dolt.corrupt."+ts)
	return fmt.Sprintf(`Dolt journal corruption detected in %s.

bd will not run automatic journal repair because Dolt's repair mode can discard data.
Recommended recovery when your remote is current:
  mv %s %s
  bd bootstrap --dry-run
  bd bootstrap --yes
  bd stats

If the remote may be stale, snapshot %s first and inspect with:
  dolt fsck
  dolt fsck --revive-journal-with-data-loss

Only use the fsck revive path after reviewing Dolt's data-loss warning.`,
		logPath(beadsDir), doltDir, backupDir, doltDir)
}

// findCorruptNomsDirs walks doltDir and returns the paths of every
// .dolt/noms/ directory whose contents look like a corrupt manifest with no
// recoverable data: journal.idx is empty (or missing), every journal file is
// at most a bare header, and oldgen/ holds no chunk data.
//
// The "no data to lose" guard is intentionally conservative: recovery only
// fires when we can prove there is nothing the user would want to keep.
func findCorruptNomsDirs(doltDir string) ([]string, error) {
	var matches []string
	err := filepath.Walk(doltDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // best-effort scan; skip unreadable subtrees
		}
		if !info.IsDir() || filepath.Base(path) != "noms" {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != ".dolt" {
			return nil
		}
		corrupt, checkErr := nomsDirLooksCorrupt(path)
		if checkErr == nil && corrupt {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// nomsDirLooksCorrupt returns true if the .dolt/noms directory has a
// manifest but no recoverable chunk data. See findCorruptNomsDirs for the
// exact conditions.
func nomsDirLooksCorrupt(nomsDir string) (bool, error) {
	manifestPath := filepath.Join(nomsDir, "manifest")
	if _, err := os.Stat(manifestPath); err != nil {
		return false, err // no manifest = not the shape we're recovering
	}

	if info, err := os.Stat(filepath.Join(nomsDir, "journal.idx")); err == nil {
		if info.Size() > 0 {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	entries, err := os.ReadDir(nomsDir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		// Dolt journal files have a 32-char name (all 'v' until rotated).
		name := e.Name()
		if len(name) == 32 && isLowerAlphaName(name) {
			info, err := e.Info()
			if err != nil {
				return false, err
			}
			// Journal header is 40 bytes; anything larger may contain data.
			if info.Size() > 64 {
				return false, nil
			}
		}
	}

	oldgen := filepath.Join(nomsDir, "oldgen")
	if oldgenEntries, err := os.ReadDir(oldgen); err == nil {
		for _, e := range oldgenEntries {
			if e.Type().IsRegular() && e.Name() != "manifest" && e.Name() != "LOCK" {
				if info, infoErr := e.Info(); infoErr == nil && info.Size() > 0 {
					return false, nil
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	return true, nil
}

func isLowerAlphaName(s string) bool {
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// recoverCorruptManifest detects the GH#3290 corrupt-manifest condition by
// scanning the dolt server log for the "root hash doesn't exist" signature
// and confirming that every affected database has no recoverable data. If
// both conditions hold, each corrupt .dolt/ directory is backed up with a
// timestamped suffix and the database is reinitialized in place.
//
// Returns the list of backup paths created. If the preconditions do not
// hold, returns (nil, nil) so the caller can surface the original start
// failure.
func recoverCorruptManifest(beadsDir, doltDir string) ([]string, error) {
	hasErr, err := logHasCorruptManifestError(logPath(beadsDir))
	if err != nil || !hasErr {
		return nil, err
	}

	nomsDirs, err := findCorruptNomsDirs(doltDir)
	if err != nil {
		return nil, err
	}
	if len(nomsDirs) == 0 {
		return nil, nil
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	var backups []string
	for _, nomsDir := range nomsDirs {
		dotDolt := filepath.Dir(nomsDir) // .../X/.dolt
		dbDir := filepath.Dir(dotDolt)   // .../X
		backupPath := dotDolt + "." + ts + ".corrupt.backup"

		if err := os.Rename(dotDolt, backupPath); err != nil {
			return backups, fmt.Errorf("backing up corrupt dolt database at %s: %w", dotDolt, err)
		}
		backups = append(backups, backupPath)

		if err := ensureDoltInit(dbDir); err != nil {
			// Best-effort restore so the user is no worse off than before.
			_ = os.RemoveAll(dotDolt)
			_ = os.Rename(backupPath, dotDolt)
			return backups[:len(backups)-1], fmt.Errorf("reinitializing dolt database at %s: %w", dbDir, err)
		}
	}
	return backups, nil
}
