package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultImportRejectsPath is the quarantine destination a recovery import
// (auto-import, `bd init --from-jsonl`, bootstrap, the legacy git-hook sync
// importer) preserves rejected records in: next to the source, named
// <source-without-.jsonl>.rejected.jsonl (GH#4492 design §5). A source with
// any other suffix keeps it and appends: backup.txt rejects go to
// backup.txt.rejected.jsonl.
func defaultImportRejectsPath(sourcePath string) string {
	dir := filepath.Dir(sourcePath)
	base := filepath.Base(sourcePath)
	if stem, ok := strings.CutSuffix(base, ".jsonl"); ok {
		return filepath.Join(dir, stem+".rejected.jsonl")
	}
	return filepath.Join(dir, base+".rejected.jsonl")
}

// appendImportRejects merges newly rejected records into the rejects file,
// preserving the raw record bytes (never a reserialization — unknown fields
// must survive) and every record an earlier run quarantined: old and new
// records are concatenated, deduplicated by exact line bytes, and a clean
// run with nothing to add never touches the file (design §5/§6).
//
// Unusable destinations are refused before any write: a directory, a
// symlink, or an alias of the import source itself (a hardlink to it would
// append rejects into the file being imported). The write is a
// same-directory temporary file, fsynced, mode 0600, renamed over the
// destination, so a failed replacement leaves the old file intact.
func appendImportRejects(destPath, sourcePath string, newLines []string) error {
	if len(newLines) == 0 {
		return nil
	}
	if fi, err := os.Lstat(destPath); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("rejects destination %s is a directory", destPath)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("rejects destination %s is a symlink", destPath)
		}
	}
	// An alias of the source (the same file via a hardlink, or the path
	// itself) would quarantine rejects into the file being imported.
	if srcInfo, err := os.Stat(sourcePath); err == nil {
		if dstInfo, err := os.Stat(destPath); err == nil && os.SameFile(dstInfo, srcInfo) {
			return fmt.Errorf("rejects destination %s is the import source itself", destPath)
		}
	}

	var content []byte
	//nolint:gosec // G304: destPath derives from the user's --rejects flag or the source file's directory
	if existing, err := os.ReadFile(destPath); err == nil {
		content = existing
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing rejects file %s: %w", destPath, err)
	}

	var buf bytes.Buffer
	buf.Write(content)
	if len(content) > 0 && !bytes.HasSuffix(content, []byte("\n")) {
		buf.WriteByte('\n')
	}
	seen := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSuffix(string(content), "\n"), "\n") {
		if line != "" {
			seen[line] = struct{}{}
		}
	}
	for _, line := range newLines {
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		buf.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			buf.WriteByte('\n')
		}
	}

	return replaceFileAtomic(destPath, buf.Bytes())
}

// replaceFileAtomic writes data to path through a same-directory temporary
// file: create, write, fsync, 0600, rename. A failure anywhere before the
// rename removes the temporary and leaves the destination exactly as it was.
func replaceFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".bd-rejects-*")
	if err != nil {
		return fmt.Errorf("create temporary rejects file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write rejects data: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync rejects data: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temporary rejects file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("restrict temporary rejects file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install rejects file %s: %w", path, err)
	}
	return nil
}
