package attachments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

type fakeStore struct {
	path   string
	cliDir string
}

func (s fakeStore) Path() string   { return s.path }
func (s fakeStore) CLIDir() string { return s.cliDir }

func TestRootFromStoreLocator(t *testing.T) {
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")

	tests := []struct {
		name string
		st   fakeStore
	}{
		{
			name: "dolt",
			st:   fakeStore{path: filepath.Join(beadsDir, "dolt"), cliDir: filepath.Join(beadsDir, "dolt", "beads")},
		},
		{
			name: "embedded",
			st:   fakeStore{path: filepath.Join(beadsDir, "embeddeddolt"), cliDir: filepath.Join(beadsDir, "embeddeddolt", "beads")},
		},
		{
			name: ".beads",
			st:   fakeStore{path: beadsDir},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Root(tt.st)
			if err != nil {
				t.Fatalf("Root() error = %v", err)
			}
			want := filepath.Join(beadsDir, DirName)
			if got != want {
				t.Fatalf("Root() = %q, want %q", got, want)
			}
		})
	}
}

func TestStoreCopiesAndHashesAttachment(t *testing.T) {
	tmp := t.TempDir()
	st := fakeStore{path: filepath.Join(tmp, ".beads", "embeddeddolt")}
	source := filepath.Join(tmp, "body.md")
	if err := os.WriteFile(source, []byte("# Hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Store(st, "bd-abc", source)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if got.OriginalFilename != "body.md" {
		t.Fatalf("OriginalFilename = %q", got.OriginalFilename)
	}
	if got.HashAlgorithm != HashAlgorithm {
		t.Fatalf("HashAlgorithm = %q", got.HashAlgorithm)
	}
	if got.ByteSize != int64(len("# Hello\n")) {
		t.Fatalf("ByteSize = %d", got.ByteSize)
	}
	if !strings.HasPrefix(got.MimeType, "text/markdown") {
		t.Fatalf("MimeType = %q, want text/markdown", got.MimeType)
	}
	wantRel := filepath.ToSlash(filepath.Join(DirName, "bd-abc", got.ContentHash))
	if got.StorageRelPath != wantRel {
		t.Fatalf("StorageRelPath = %q, want %q", got.StorageRelPath, wantRel)
	}
	data, err := os.ReadFile(got.StorageAbsPath)
	if err != nil {
		t.Fatalf("ReadFile(stored) error = %v", err)
	}
	if string(data) != "# Hello\n" {
		t.Fatalf("stored data = %q", data)
	}
}

func TestStoredPathRejectsTraversal(t *testing.T) {
	tmp := t.TempDir()
	st := fakeStore{path: filepath.Join(tmp, ".beads", "dolt")}

	for _, rel := range []string{
		"../outside",
		"attachments/../outside",
		"attachments/bd-abc/../outside",
	} {
		t.Run(rel, func(t *testing.T) {
			if _, err := StoredPath(st, rel); err == nil {
				t.Fatalf("StoredPath(%q) succeeded, want error", rel)
			}
		})
	}
}

func TestCopyOutRefusesOverwriteUnlessForced(t *testing.T) {
	tmp := t.TempDir()
	st := fakeStore{path: filepath.Join(tmp, ".beads", "embeddeddolt")}
	source := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	stored, err := Store(st, "bd-abc", source)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	attachment := &types.Attachment{
		OriginalFilename: stored.OriginalFilename,
		StorageRelPath:   stored.StorageRelPath,
	}

	targetDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath, err := CopyOut(st, attachment, targetDir, false)
	if err != nil {
		t.Fatalf("CopyOut() error = %v", err)
	}
	if targetPath != filepath.Join(targetDir, "note.txt") {
		t.Fatalf("CopyOut() path = %q", targetPath)
	}
	if _, err := CopyOut(st, attachment, targetDir, false); err == nil {
		t.Fatal("CopyOut() overwrite succeeded, want error")
	}
	if _, err := CopyOut(st, attachment, targetDir, true); err != nil {
		t.Fatalf("CopyOut(force) error = %v", err)
	}
}

func TestRemoveStoredFile(t *testing.T) {
	tmp := t.TempDir()
	st := fakeStore{path: filepath.Join(tmp, ".beads", "dolt")}
	source := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	stored, err := Store(st, "bd-abc", source)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	attachment := &types.Attachment{StorageRelPath: stored.StorageRelPath}
	if !Exists(st, attachment) {
		t.Fatal("Exists() = false, want true")
	}
	if err := RemoveStoredFile(st, attachment); err != nil {
		t.Fatalf("RemoveStoredFile() error = %v", err)
	}
	if Exists(st, attachment) {
		t.Fatal("Exists() = true, want false")
	}
	if err := RemoveStoredFile(st, attachment); err != nil {
		t.Fatalf("RemoveStoredFile() second call error = %v", err)
	}
}
