// journal.go implements pre-image journaling for RunInTransaction rollback.
// Flat-file has no database transaction to abort, so the store records each
// mutated file's prior contents (or absence) the first time a transaction
// touches it; a failed transaction restores every pre-image, making the
// "all succeed or all fail" contract in storage.RunInTransaction hold.
package flatfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// journalEntry is one file's state before the active transaction first
// mutated it. exists=false means the file was absent (rollback removes it).
type journalEntry struct {
	data   []byte
	exists bool
}

// journalPreImage records path's current contents into the active
// transaction's journal. Outside a transaction (journal == nil) it is a no-op.
// Only the first touch per path is recorded — later writes must not overwrite
// the true pre-image. Callers invoke it before mutating path; issue-file
// callers are already inside the writeMu span, so the read cannot race the
// write it precedes.
func (s *FlatFileStore) journalPreImage(path string) error {
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	if s.journal == nil {
		return nil
	}
	if _, ok := s.journal[path]; ok {
		return nil
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		s.journal[path] = journalEntry{data: data, exists: true}
	case errors.Is(err, fs.ErrNotExist):
		s.journal[path] = journalEntry{}
	default:
		return fmt.Errorf("flatfile: journal pre-image %s: %w", path, err)
	}
	return nil
}

// rollbackJournal restores every journaled pre-image, undoing all writes the
// failed transaction made. It detaches the journal first so the restore
// writes are not themselves journaled, and holds writeMu so the restored
// files land as atomically as any other mutation.
func (s *FlatFileStore) rollbackJournal() error {
	s.journalMu.Lock()
	entries := s.journal
	s.journal = nil
	s.journalMu.Unlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var firstErr error
	for path, e := range entries {
		var err error
		if e.exists {
			// The transaction may have removed the file's parent directory
			// (DeleteIssue cascades remove the now-empty comments/<id>/ dir,
			// which is not itself journaled). Recreate it, or the pre-image
			// write below fails ENOENT and the data is lost with the journal.
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = atomicWriteFile(path, e.data)
			}
		} else if err = os.Remove(path); errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("flatfile: rollback %s: %w", path, err)
		}
	}
	return firstErr
}

// atomicWrite is the journal-aware wrapper every store-file mutation goes
// through: it records the pre-image for the active transaction (if any), then
// delegates durability to atomicWriteFile.
func (s *FlatFileStore) atomicWrite(path string, data []byte) error {
	if err := s.journalPreImage(path); err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

// removeFile is the journal-aware counterpart of os.Remove; callers keep
// their own fs.ErrNotExist mapping, which errors.Is still sees through.
func (s *FlatFileStore) removeFile(path string) error {
	if err := s.journalPreImage(path); err != nil {
		return err
	}
	return os.Remove(path)
}
