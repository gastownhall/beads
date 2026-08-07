package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/atomicfile"
)

// acquireSourceZip caches only catalog-authenticated module archives. Builds
// and their output bytes are deliberately private to one generator process.
func acquireSourceZip(cache string, entry catalogEntry, download func(string) error) (archive string, err error) {
	identity, err := sourceBuildIdentity(entry)
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "source-zip", "v1")
	zipPath := filepath.Join(root, identity+".zip")
	if _, err := os.Lstat(zipPath); err == nil {
		if err := verifySourceZip(zipPath, entry); err != nil {
			return "", fmt.Errorf("validate committed source zip: %w", err)
		}
		return zipPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	staging, err := os.CreateTemp(root, "."+identity+".staging-")
	if err != nil {
		return "", err
	}
	staged := staging.Name()
	if err := staging.Close(); err != nil {
		return "", err
	}
	defer func() {
		if removeErr := os.Remove(staged); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove source zip staging: %w", removeErr))
		}
	}()
	if err := download(staged); err != nil {
		return "", err
	}
	if err := verifySourceZip(staged, entry); err != nil {
		return "", fmt.Errorf("validate downloaded source zip: %w", err)
	}
	if err := os.Rename(staged, zipPath); err != nil {
		if _, statErr := os.Lstat(zipPath); statErr == nil {
			if verifyErr := verifySourceZip(zipPath, entry); verifyErr != nil {
				return "", fmt.Errorf("validate concurrently committed source zip: %w", verifyErr)
			}
			return zipPath, nil
		}
		return "", err
	}
	return zipPath, nil
}

func verifySourceZip(name string, entry catalogEntry) error {
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("source zip is not a regular non-symlink file")
	}
	if info.Size() != entry.SourceZip.Size {
		return errors.New("source zip size does not match catalog")
	}
	raw, err := os.ReadFile(name) //nolint:gosec // checked cache entry.
	if err != nil {
		return err
	}
	if digest(raw) != entry.SourceZip.SHA256 {
		return errors.New("source zip digest does not match catalog")
	}
	return nil
}

// validateSourceBuildCache retains its command-facing name while validating
// only persistent authenticated inputs, never generated executables.
func validateSourceBuildCache(cache string, catalog catalog) error {
	info, err := os.Lstat(cache)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cache root is not a real directory")
	}
	topLevel, err := os.ReadDir(cache)
	if err != nil {
		return err
	}
	for _, item := range topLevel {
		switch item.Name() {
		case "assets", "source-zip":
			path := filepath.Join(cache, item.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s cache root is not a real directory", item.Name())
			}
		case "source-build":
			return errors.New("legacy executable source cache is not permitted")
		default:
			return fmt.Errorf("unknown top-level cache entry %q", item.Name())
		}
	}

	sourceRoot := filepath.Join(cache, "source-zip")
	sourceChildren, err := os.ReadDir(sourceRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, item := range sourceChildren {
		if item.Name() != "v1" {
			return fmt.Errorf("unknown source zip cache generation %q", item.Name())
		}
	}
	root := filepath.Join(sourceRoot, "v1")
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("source zip cache v1 root is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return validateReleaseAssetCache(cache, catalog)
	}
	if err != nil {
		return err
	}
	known := make(map[string]catalogEntry, len(catalog.Versions))
	for _, entry := range catalog.Versions {
		identity, err := sourceBuildIdentity(entry)
		if err != nil {
			return err
		}
		known[identity+".zip"] = entry
	}
	for _, item := range entries {
		if sourceZipStagingIdentity(item.Name(), known) {
			path := filepath.Join(root, item.Name())
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("invalid source zip staging entry %q", item.Name())
			}
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove interrupted source zip staging entry %q: %w", item.Name(), err)
			}
			continue
		}
		entry, ok := known[item.Name()]
		if !ok {
			return fmt.Errorf("unknown source zip cache entry %q", item.Name())
		}
		if err := verifySourceZip(filepath.Join(root, item.Name()), entry); err != nil {
			return fmt.Errorf("%s: %w", item.Name(), err)
		}
	}
	return validateReleaseAssetCache(cache, catalog)
}

func sourceZipStagingIdentity(name string, known map[string]catalogEntry) bool {
	for archive := range known {
		identity := strings.TrimSuffix(archive, ".zip")
		prefix := "." + identity + ".staging-"
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	return false
}

func validateReleaseAssetCache(cache string, catalog catalog) error {
	root := filepath.Join(cache, "assets")
	if info, err := os.Lstat(root); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("release asset cache root is not a real directory")
	}
	versions, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	byVersion := make(map[string]catalogEntry, len(catalog.Versions))
	for _, entry := range catalog.Versions {
		if releaseAssetMatchesProxyOrigin(entry) {
			byVersion[entry.Version] = entry
		}
	}
	for _, version := range versions {
		entry, known := byVersion[version.Name()]
		path := filepath.Join(root, version.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !known {
			return fmt.Errorf("unknown release asset version %q", version.Name())
		}
		asset := entry.GitHubRelease.LinuxAMD64Asset
		digestDir := filepath.Join(path, strings.TrimPrefix(asset.Digest, "sha256:"))
		children, err := os.ReadDir(path)
		if err != nil || len(children) != 1 || children[0].Name() != filepath.Base(digestDir) {
			return fmt.Errorf("%s: malformed release asset digest path", version.Name())
		}
		info, err = os.Lstat(digestDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: invalid release asset digest directory", version.Name())
		}
		files, err := os.ReadDir(digestDir)
		if err != nil {
			return err
		}
		archive, foundArchive := filepath.Join(digestDir, asset.Name), false
		for _, file := range files {
			filePath := filepath.Join(digestDir, file.Name())
			if file.Name() == asset.Name+".tmp" {
				fileInfo, err := os.Lstat(filePath)
				if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
					return fmt.Errorf("%s: invalid release asset staging entry %q", version.Name(), file.Name())
				}
				if err := os.Remove(filePath); err != nil {
					return fmt.Errorf("%s: remove interrupted release asset staging entry %q: %w", version.Name(), file.Name(), err)
				}
				continue
			}
			if file.Name() != asset.Name {
				return fmt.Errorf("%s: malformed release asset cache entry %q", version.Name(), file.Name())
			}
			fileInfo, err := os.Lstat(filePath)
			if err != nil || !fileInfo.Mode().IsRegular() {
				return fmt.Errorf("%s: malformed release asset file %q", version.Name(), file.Name())
			}
			if file.Name() == asset.Name {
				foundArchive = true
			}
		}
		if !foundArchive {
			return fmt.Errorf("%s: missing release archive", version.Name())
		}
		if err := verifyReleaseAsset(entry, archive); err != nil {
			return fmt.Errorf("%s: %w", version.Name(), err)
		}
	}
	return nil
}

func extractVerifiedSourceZip(archive string, entry catalogEntry, destination string) (err error) {
	reader, err := zip.OpenReader(archive) //nolint:gosec // caller verified catalog facts.
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close source zip: %w", closeErr))
		}
	}()
	prefix := modulePath + "@" + entry.Version + "/"
	seen := map[string]bool{}
	var expanded int64
	for _, file := range reader.File {
		name := file.Name
		if !strings.HasPrefix(name, prefix) || strings.TrimPrefix(name, prefix) == "" {
			return fmt.Errorf("invalid module zip path %q", name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(rel, "../") || rel == ".." || seen[rel] {
			return fmt.Errorf("unsafe module zip path %q", name)
		}
		seen[rel] = true
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || mode&os.ModeType != 0 && !mode.IsDir() {
			return fmt.Errorf("module zip has non-regular file %q", name)
		}
		if mode.IsDir() {
			if file.UncompressedSize64 != 0 {
				return fmt.Errorf("module zip directory has content %q", name)
			}
			if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(rel)), 0o700); err != nil {
				return err
			}
			continue
		}
		if file.UncompressedSize64 > uint64(maxSourceZipExpandedBytes) || expanded > maxSourceZipExpandedBytes-int64(file.UncompressedSize64) {
			return errors.New("module zip expanded size exceeds limit")
		}
		expanded += int64(file.UncompressedSize64)
		target := filepath.Join(destination, filepath.FromSlash(rel))
		if relative, err := filepath.Rel(destination, target); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("module zip escapes destination: %q", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		out, err := atomicfile.Create(target, 0o600)
		if err != nil {
			return errors.Join(err, in.Close())
		}
		written, copyErr := io.Copy(out, io.LimitReader(in, int64(file.UncompressedSize64)+1))
		closeErr := errors.Join(in.Close(), out.Close())
		if copyErr != nil || closeErr != nil || written != int64(file.UncompressedSize64) {
			return errors.Join(copyErr, closeErr, errors.New("module zip member size mismatch"))
		}
	}
	return nil
}

func copyFile(destination, source string) (err error) {
	in, err := os.Open(source) //nolint:gosec // source is the module zip just verified against catalog provenance, size, and digest.
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close source file: %w", closeErr))
		}
	}()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // destination is acquireSourceZip's cache-owned CreateTemp staging path.
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}
