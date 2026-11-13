package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sharedBody = "# Beads\n\nUse bd ready.\n"

func sharedWrapped() string {
	return agentsBeginMarker + "\n" + sharedBody + agentsEndMarker + "\n"
}

func TestSharedSectionWrap(t *testing.T) {
	if got := wrapSharedSection(sharedBody); got != sharedWrapped() {
		t.Fatalf("wrapSharedSection = %q, want %q", got, sharedWrapped())
	}
	if got := wrapSharedSection(strings.TrimSuffix(sharedBody, "\n")); got != sharedWrapped() {
		t.Fatalf("wrapSharedSection without a final line break = %q, want %q", got, sharedWrapped())
	}
}

func TestSharedSectionUpsertAppends(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{"empty file", "", sharedWrapped()},
		{"user content", "# Mine\n", "# Mine\n\n" + sharedWrapped()},
		{"no final line break", "# Mine", "# Mine\n\n" + sharedWrapped()},
		{"CRLF", "# Mine\r\n", "# Mine\r\n\n" + sharedWrapped()},
		{"whitespace only is kept", " \n", " \n\n" + sharedWrapped()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UpsertSharedSection(tt.content, sharedBody)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("UpsertSharedSection(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestSharedSectionInstallFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(path, []byte("# Mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrote, err := InstallSharedFile(path, sharedBody)
	if err != nil || !wrote {
		t.Fatalf("InstallSharedFile = %v, %v; want a write", wrote, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Mine\n\n" + sharedWrapped(); string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want the file's 0600 kept", info.Mode().Perm())
	}
	if _, err := InstallSharedFile(filepath.Join(t.TempDir(), "missing", "x.md"), sharedBody); err == nil || errors.Is(err, ErrMalformedSection) {
		t.Fatalf("InstallSharedFile into a missing directory = %v, want an I/O error", err)
	}
}
