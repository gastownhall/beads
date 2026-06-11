// Package attachments stores and retrieves attachment bytes on disk.
//
// Attachment metadata lives in Dolt SQL. The bytes stay outside the database
// under .beads/attachments so large files do not bloat Dolt history.
package attachments

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

const (
	// DirName is the directory under .beads where attachment bytes are stored.
	DirName = "attachments"
	// HashAlgorithm is the initial content-addressing algorithm.
	HashAlgorithm = "sha256"
)

// StoredFile describes bytes copied into the attachment store.
type StoredFile struct {
	IssueID          string
	OriginalFilename string
	HashAlgorithm    string
	ContentHash      string
	MimeType         string
	ByteSize         int64
	StorageRelPath   string
	StorageAbsPath   string
}

// BeadsDir returns the .beads root for a store.
func BeadsDir(st any) (string, error) {
	loc, ok := st.(storage.StoreLocator)
	if !ok {
		return "", fmt.Errorf("store does not expose filesystem location")
	}
	path := filepath.Clean(loc.Path())
	if path == "." || path == "" {
		return "", fmt.Errorf("store path is empty")
	}
	base := filepath.Base(path)
	switch base {
	case ".beads":
		return path, nil
	case "dolt", "embeddeddolt":
		return filepath.Dir(path), nil
	}

	cliDir := filepath.Clean(loc.CLIDir())
	if cliDir != "." && cliDir != "" {
		parent := filepath.Dir(cliDir)
		switch filepath.Base(parent) {
		case "dolt", "embeddeddolt":
			return filepath.Dir(parent), nil
		}
	}

	return "", fmt.Errorf("cannot derive .beads directory from store path %q", path)
}

// Root returns the directory that stores attachment bytes for the store.
func Root(st any) (string, error) {
	beadsDir, err := BeadsDir(st)
	if err != nil {
		return "", err
	}
	return filepath.Join(beadsDir, DirName), nil
}

// Store copies sourcePath into .beads/attachments/<issueID>/<sha256>.
// The copy is streamed through SHA-256 and lands via same-directory rename so
// callers never record metadata for a partially written stored object.
func Store(st any, issueID, sourcePath string) (*StoredFile, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, fmt.Errorf("issue ID is empty")
	}

	filename, err := SafeFilename(filepath.Base(sourcePath))
	if err != nil {
		return nil, err
	}

	source, err := os.Open(sourcePath) //nolint:gosec // user-provided attachment source path
	if err != nil {
		return nil, fmt.Errorf("open attachment source: %w", err)
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat attachment source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment source is not a regular file")
	}

	return StoreReader(st, issueID, filename, source)
}

// StoreBytes stores downloaded attachment bytes in the same content-addressed
// local byte store used by `bd attachment add`.
func StoreBytes(st any, issueID, filename string, data []byte) (*StoredFile, error) {
	return StoreReader(st, issueID, filename, bytes.NewReader(data))
}

// StoreReader streams attachment bytes into .beads/attachments/<issueID>/<sha256>.
// It exists for tracker integrations that download bytes from an API instead of
// a local source path, while preserving the same hashing, MIME detection, and
// atomic write behavior as Store.
func StoreReader(st any, issueID, filename string, source io.Reader) (*StoredFile, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, fmt.Errorf("issue ID is empty")
	}
	filename, err := SafeFilename(filename)
	if err != nil {
		return nil, err
	}

	root, err := Root(st)
	if err != nil {
		return nil, err
	}
	issueDir := filepath.Join(root, issueID)
	if err := os.MkdirAll(issueDir, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}

	tmp, err := os.CreateTemp(issueDir, ".~attach-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary attachment: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	sniffer := &sniffWriter{limit: 512}
	w := io.MultiWriter(tmp, hasher, sniffer)
	n, err := io.Copy(w, source)
	if err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("copy attachment source: %w", err)
	}
	if err := tmp.Chmod(config.BeadsFilePerm); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("chmod temporary attachment: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync temporary attachment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temporary attachment: %w", err)
	}

	contentHash := hex.EncodeToString(hasher.Sum(nil))
	relPath := filepath.ToSlash(filepath.Join(DirName, issueID, contentHash))
	targetPath := filepath.Join(issueDir, contentHash)
	if _, err := os.Stat(targetPath); err == nil {
		cleanup = true
	} else if os.IsNotExist(err) {
		if err := os.Rename(tmpPath, targetPath); err != nil {
			return nil, fmt.Errorf("store attachment: %w", err)
		}
		cleanup = false
	} else {
		return nil, fmt.Errorf("stat stored attachment: %w", err)
	}

	return &StoredFile{
		IssueID:          issueID,
		OriginalFilename: filename,
		HashAlgorithm:    HashAlgorithm,
		ContentHash:      contentHash,
		MimeType:         DetectMimeType(filename, sniffer.Bytes()),
		ByteSize:         n,
		StorageRelPath:   relPath,
		StorageAbsPath:   targetPath,
	}, nil
}

// SafeFilename validates a display filename before it is written to metadata
// or used as the default copy-out filename.
func SafeFilename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("attachment filename is empty")
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("attachment filename %q must not contain path separators", name)
	}
	return name, nil
}

// DetectMimeType returns a MIME type from file contents with an extension
// fallback for formats that content sniffing reports only as generic text.
func DetectMimeType(filename string, sample []byte) string {
	detected := http.DetectContentType(sample)
	byExt := mime.TypeByExtension(filepath.Ext(filename))
	if byExt == "" {
		switch strings.ToLower(filepath.Ext(filename)) {
		case ".md", ".markdown":
			byExt = "text/markdown; charset=utf-8"
		}
	}
	if byExt == "" {
		return detected
	}
	if detected == "application/octet-stream" || strings.HasPrefix(detected, "text/plain") {
		return byExt
	}
	return detected
}

// StoredPath resolves attachment metadata's storage_relpath safely under
// the store's .beads directory.
func StoredPath(st any, relPath string) (string, error) {
	beadsDir, err := BeadsDir(st)
	if err != nil {
		return "", err
	}
	clean, err := cleanRelPath(relPath)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(beadsDir, clean)
	if err := ensureWithin(beadsDir, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// Exists reports whether the attachment bytes referenced by metadata exist.
func Exists(st any, attachment *types.Attachment) bool {
	if attachment == nil {
		return false
	}
	path, err := StoredPath(st, attachment.StorageRelPath)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// CopyOut copies attachment bytes to target. If target is an existing
// directory, the attachment's original filename is used inside that directory.
// Existing files are rejected unless force is true.
func CopyOut(st any, attachment *types.Attachment, target string, force bool) (string, error) {
	if attachment == nil {
		return "", fmt.Errorf("attachment is nil")
	}
	sourcePath, err := StoredPath(st, attachment.StorageRelPath)
	if err != nil {
		return "", err
	}
	filename, err := SafeFilename(attachment.OriginalFilename)
	if err != nil {
		return "", err
	}

	dest := target
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		dest = filepath.Join(target, filename)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat copy target: %w", err)
	}
	if !force {
		if _, err := os.Stat(dest); err == nil {
			return "", fmt.Errorf("copy target %s already exists", dest)
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat copy target: %w", err)
		}
	}

	source, err := os.Open(sourcePath) //nolint:gosec // path is resolved under .beads above
	if err != nil {
		return "", fmt.Errorf("open stored attachment: %w", err)
	}
	defer source.Close()

	writer, err := atomicfile.Create(dest, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(writer, source); err != nil {
		_ = writer.Abort()
		return "", fmt.Errorf("copy attachment out: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	return dest, nil
}

// RemoveStoredFile removes attachment bytes. Missing files are treated as
// already removed so metadata cleanup can stay idempotent.
func RemoveStoredFile(st any, attachment *types.Attachment) error {
	if attachment == nil {
		return fmt.Errorf("attachment is nil")
	}
	path, err := StoredPath(st, attachment.StorageRelPath)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stored attachment: %w", err)
	}
	return nil
}

type sniffWriter struct {
	limit int
	buf   []byte
}

func (w *sniffWriter) Write(p []byte) (int, error) {
	remaining := w.limit - len(w.buf)
	if remaining > 0 {
		if len(p) < remaining {
			remaining = len(p)
		}
		w.buf = append(w.buf, p[:remaining]...)
	}
	return len(p), nil
}

func (w *sniffWriter) Bytes() []byte {
	return w.buf
}

func cleanRelPath(relPath string) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("attachment storage path is empty")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("attachment storage path must be relative")
	}
	parts := strings.FieldsFunc(relPath, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("attachment storage path %q is unsafe", relPath)
		}
	}
	clean := filepath.Clean(filepath.FromSlash(relPath))
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("attachment storage path %q is unsafe", relPath)
	}
	return clean, nil
}

func ensureWithin(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("resolve attachment path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("attachment path escapes .beads")
	}
	return nil
}
