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

func TestSharedSectionUpsertReplacesInPlace(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{
			name:    "bare markers with text around",
			content: "# Mine\n\n" + agentsBeginMarker + "\nOld.\n" + agentsEndMarker + "\n\n## After\n",
			want:    "# Mine\n\n" + sharedWrapped() + "\n## After\n",
		},
		{
			name:    "metadata marker",
			content: "<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:00000000 -->\nOld.\n" + agentsEndMarker + "\n",
			want:    sharedWrapped(),
		},
		{
			name:    "END marker without a final line break",
			content: "# Mine\n" + agentsBeginMarker + "\nOld.\n" + agentsEndMarker,
			want:    "# Mine\n" + strings.TrimSuffix(sharedWrapped(), "\n"),
		},
		{
			name:    "CRLF around the section",
			content: "# Mine\r\n\r\n" + agentsBeginMarker + "\r\nOld.\r\n" + agentsEndMarker + "\r\n\r\nAfter\r\n",
			want:    "# Mine\r\n\r\n" + strings.TrimSuffix(sharedWrapped(), "\n") + "\r\n\r\nAfter\r\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UpsertSharedSection(tt.content, sharedBody)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("UpsertSharedSection = %q, want %q", got, tt.want)
			}
			again, err := UpsertSharedSection(got, sharedBody)
			if err != nil || again != got {
				t.Fatalf("second UpsertSharedSection = %q, %v; want it unchanged", again, err)
			}
		})
	}
}

func TestSharedSectionCheckFile(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name, content string
		want          SharedSectionState
		malformed     bool
	}{
		{name: "user content only", content: "# Mine\n", want: SharedSectionNone},
		{name: "section", content: "# Mine\n\n" + sharedWrapped(), want: SharedSectionPresent},
		{name: "dangling BEGIN", content: agentsBeginMarker + "\n", want: SharedSectionMalformed, malformed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".md")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := CheckSharedFile(path)
			if got != tt.want || errors.Is(err, ErrMalformedSection) != tt.malformed {
				t.Fatalf("CheckSharedFile = %v, %v; want %v, malformed %v", got, err, tt.want, tt.malformed)
			}
		})
	}
	if _, err := CheckSharedFile(filepath.Join(dir, "missing.md")); !os.IsNotExist(err) {
		t.Fatalf("CheckSharedFile(missing) err = %v, want not-exist", err)
	}
}
