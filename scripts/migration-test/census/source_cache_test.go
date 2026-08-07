package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSourceZipCacheHitAvoidsDownloader(t *testing.T) {
	cache, entry := t.TempDir(), sourceZipEntry(t, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"})
	calls := 0
	download := func(dst string) error {
		calls++
		return os.WriteFile(dst, sourceZipBytes(t, entry, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"}), 0o600)
	}
	first, err := acquireSourceZip(cache, entry, download)
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireSourceZip(cache, entry, func(string) error { t.Fatal("cache hit downloaded"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first != second {
		t.Fatalf("calls=%d paths=%q %q", calls, first, second)
	}
}

func TestSourceZipCacheValidationRejectsTamperSymlinkAndUnknown(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, string, catalogEntry){
		"tamper": func(t *testing.T, p string, _ catalogEntry) {
			if err := os.WriteFile(p, []byte("bad"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, p string, _ catalogEntry) {
			target := filepath.Join(t.TempDir(), "zip")
			if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		},
		"unknown": func(t *testing.T, p string, _ catalogEntry) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(p), strings.Repeat("a", 64)+".zip"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unknown staging": func(t *testing.T, p string, _ catalogEntry) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(p), ".candidate.staging-dead"), []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"top-level executable": func(t *testing.T, p string, _ catalogEntry) {
			cache := filepath.Dir(filepath.Dir(filepath.Dir(p)))
			if err := os.WriteFile(filepath.Join(cache, "rogue"), []byte("executable"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked source root": func(t *testing.T, p string, _ catalogEntry) {
			sourceRoot := filepath.Dir(filepath.Dir(p))
			target := t.TempDir()
			if err := os.Mkdir(filepath.Join(target, "v1"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(sourceRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, sourceRoot); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cache, entry := t.TempDir(), sourceZipEntry(t, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"})
			p, err := acquireSourceZip(cache, entry, func(dst string) error {
				return os.WriteFile(dst, sourceZipBytes(t, entry, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"}), 0o600)
			})
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, p, entry)
			if err := validateSourceBuildCache(cache, catalog{Versions: []catalogEntry{entry}}); err == nil {
				t.Fatal("accepted invalid cache")
			}
		})
	}
}

func TestSourceZipCacheValidationRemovesKnownOrphanStaging(t *testing.T) {
	cache, entry := t.TempDir(), sourceZipEntry(t, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"})
	p, err := acquireSourceZip(cache, entry, func(dst string) error {
		return os.WriteFile(dst, sourceZipBytes(t, entry, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"}), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := sourceBuildIdentity(entry)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(filepath.Dir(p), "."+identity+".staging-interrupted")
	if err := os.WriteFile(staging, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceBuildCache(cache, catalog{Versions: []catalogEntry{entry}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(staging); !os.IsNotExist(err) {
		t.Fatalf("orphan staging remains after validation: %v", err)
	}
}

func TestSourceZipCacheValidationRejectsLegacyExecutableCache(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "source-build", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceBuildCache(cache, catalog{}); err == nil {
		t.Fatal("accepted historical executable cache")
	}
}

func TestReleaseAssetCacheValidationRejectsExtractedExecutable(t *testing.T) {
	cache := t.TempDir()
	entry := releaseAssetEntryForTest(t, cache, []byte("authenticated executable"))
	asset := entry.GitHubRelease.LinuxAMD64Asset
	digestDir := filepath.Join(cache, "assets", entry.Version, strings.TrimPrefix(asset.Digest, "sha256:"))
	if err := os.WriteFile(filepath.Join(digestDir, "bd"), []byte("stale executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceBuildCache(cache, catalog{Versions: []catalogEntry{entry}}); err == nil {
		t.Fatal("accepted persistent extracted executable")
	}
}

func TestReleaseAssetCacheValidationRemovesKnownOrphanStaging(t *testing.T) {
	cache := t.TempDir()
	entry := releaseAssetEntryForTest(t, cache, []byte("authenticated executable"))
	asset := entry.GitHubRelease.LinuxAMD64Asset
	archive := filepath.Join(cache, "assets", entry.Version, strings.TrimPrefix(asset.Digest, "sha256:"), asset.Name)
	staging := archive + ".tmp"
	if err := os.WriteFile(staging, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceBuildCache(cache, catalog{Versions: []catalogEntry{entry}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(staging); !os.IsNotExist(err) {
		t.Fatalf("orphan staging remains after validation: %v", err)
	}
}

func TestReleaseAssetCacheValidationRejectsUnknownStaging(t *testing.T) {
	cache := t.TempDir()
	entry := releaseAssetEntryForTest(t, cache, []byte("authenticated executable"))
	asset := entry.GitHubRelease.LinuxAMD64Asset
	archive := filepath.Join(cache, "assets", entry.Version, strings.TrimPrefix(asset.Digest, "sha256:"), asset.Name)
	if err := os.WriteFile(archive+".tmp-unrecognized", []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceBuildCache(cache, catalog{Versions: []catalogEntry{entry}}); err == nil {
		t.Fatal("accepted unknown release asset staging")
	}
}

func TestExtractVerifiedSourceZipRejectsAttacks(t *testing.T) {
	for name, member := range map[string]string{
		"traversal":    modulePath + "@v0.9.1/../x",
		"absolute":     "/x",
		"wrong prefix": "other@v1.0.0/x",
		"duplicate":    modulePath + "@v0.9.1/x",
		"symlink":      modulePath + "@v0.9.1/link",
	} {
		t.Run(name, func(t *testing.T) {
			entry := testCatalog().Versions[0]
			archive := filepath.Join(t.TempDir(), "source.zip")
			raw := namedZipBytes(t, map[string]string{member: "x"})
			entry.SourceZip.Size, entry.SourceZip.SHA256 = int64(len(raw)), digest(raw)
			if err := os.WriteFile(archive, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "duplicate" {
				archive = writeDuplicateZip(t, entry)
			}
			if name == "symlink" {
				archive = writeSymlinkZip(t, entry)
			}
			if err := extractVerifiedSourceZip(archive, entry, t.TempDir()); err == nil {
				t.Fatal("accepted unsafe zip")
			}
		})
	}
}

func TestFreshSourceBuildOutputsAreDistinctAndPersistentCacheHasNoExecutable(t *testing.T) {
	entry := sourceZipEntry(t, map[string]string{"go.mod": "module github.com/steveyegge/beads\n", "cmd/bd/main.go": "package main\nfunc main() {}\n"})
	cache := t.TempDir()
	raw := sourceZipBytes(t, entry, map[string]string{"go.mod": "module github.com/steveyegge/beads\n", "cmd/bd/main.go": "package main\nfunc main() {}\n"})
	if _, err := acquireSourceZip(cache, entry, func(dst string) error { return os.WriteFile(dst, raw, 0o600) }); err != nil {
		t.Fatal(err)
	}
	firstDir, first, err := freshBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(firstDir)
	secondDir, second, err := freshBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(secondDir)
	if first == second {
		t.Fatal("expected independent fresh build paths")
	}
	if err := filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Type()&0o111 != 0 {
			t.Fatalf("persistent cache contains executable %q", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func sourceZipEntry(t *testing.T, files map[string]string) catalogEntry {
	t.Helper()
	entry := testCatalog().Versions[0]
	raw := sourceZipBytes(t, entry, files)
	entry.SourceZip.Size = int64(len(raw))
	entry.SourceZip.SHA256 = digest(raw)
	return entry
}
func sourceZipBytes(t *testing.T, entry catalogEntry, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var raw bytes.Buffer
	w := zip.NewWriter(&raw)
	for _, name := range names {
		body := files[name]
		f, err := w.Create(modulePath + "@" + entry.Version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}
func namedZipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := zip.NewWriter(&raw)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}
func writeDuplicateZip(t *testing.T, entry catalogEntry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "duplicate.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for _, body := range []string{"one", "two"} {
		z, err := w.Create(modulePath + "@" + entry.Version + "/x")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := z.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
func writeSymlinkZip(t *testing.T, entry catalogEntry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "symlink.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	h := &zip.FileHeader{Name: modulePath + "@" + entry.Version + "/link"}
	h.SetMode(os.ModeSymlink | 0o777)
	z, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := z.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
