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

func TestSharedSectionStrip(t *testing.T) {
	for _, tt := range []struct {
		name, content, want string
		stripped, empty     bool
	}{
		{name: "no section", content: "# Mine\n", want: "# Mine\n"},
		{name: "appended section round-trips", content: "# Mine\n\n" + sharedWrapped(), want: "# Mine\n", stripped: true},
		{name: "text after", content: "# Mine\n\n" + sharedWrapped() + "\n## After\n", want: "# Mine\n\n## After\n", stripped: true},
		{name: "section at the top", content: sharedWrapped() + "\n## After\n", want: "## After\n", stripped: true},
		// removeBeadsSection's separator rule: extra blank lines around
		// the section become one.
		{name: "extra blank lines", content: "# Mine\n\n\n\n" + sharedWrapped() + "\n\n\n## After\n", want: "# Mine\n\n## After\n", stripped: true},
		{name: "section only", content: sharedWrapped(), want: "", stripped: true, empty: true},
		{name: "section and whitespace", content: "\n\n" + sharedWrapped() + "\n  \n", want: "  \n", stripped: true, empty: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, stripped, empty, err := StripSharedSection(tt.content)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || stripped != tt.stripped || empty != tt.empty {
				t.Fatalf("StripSharedSection = %q, %v, %v; want %q, %v, %v", got, stripped, empty, tt.want, tt.stripped, tt.empty)
			}
		})
	}
}

func TestSharedSectionRemoveFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	mine := write("mine.md", "# Mine\n\n"+sharedWrapped())
	if got, err := RemoveSharedFile(mine); err != nil || got != SharedRemovedSection {
		t.Fatalf("RemoveSharedFile(user + section) = %v, %v", got, err)
	}
	if data, _ := os.ReadFile(mine); string(data) != "# Mine\n" {
		t.Fatalf("file = %q, want the user's content", data)
	}
	if info, err := os.Stat(mine); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after strip = %v, %v; want 0600 kept", info, err)
	}
	if got, err := RemoveSharedFile(mine); err != nil || got != SharedRemovedNothing {
		t.Fatalf("RemoveSharedFile(no section) = %v, %v", got, err)
	}
	only := write("only.md", sharedWrapped())
	if got, err := RemoveSharedFile(only); err != nil || got != SharedRemovedFile {
		t.Fatalf("RemoveSharedFile(section only) = %v, %v", got, err)
	}
	if _, err := os.Stat(only); !os.IsNotExist(err) {
		t.Fatalf("section-only file kept, err = %v", err)
	}
	if got, err := RemoveSharedFile(filepath.Join(dir, "missing.md")); err != nil || got != SharedRemovedNothing {
		t.Fatalf("RemoveSharedFile(missing) = %v, %v", got, err)
	}
}

// Markers that do not make exactly one section are refused by every
// operation, which leaves the content as it was.
func TestSharedSectionMalformed(t *testing.T) {
	const begin, end = agentsBeginMarker, agentsEndMarker
	for _, tt := range []struct{ name, content string }{
		{"dangling BEGIN", "# Mine\n" + begin + "\nOld.\n"},
		{"dangling metadata BEGIN", "<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:00000000 -->\nOld.\n"},
		{"dangling END", "# Mine\n" + end + "\n"},
		{"reversed", end + "\nMine.\n" + begin + "\n"},
		{"two sections", begin + "\nA\n" + end + "\n" + begin + "\nB\n" + end + "\n"},
		{"nested", begin + "\n" + begin + "\nA\n" + end + "\n" + end + "\n"},
		{"unterminated BEGIN comment", "<!-- BEGIN BEADS INTEGRATION\nA\n" + end + "\n"},
		{"BEGIN closed after END", "<!-- BEGIN BEADS INTEGRATION " + end + " -->\n"},
		{"BEGIN prefix of another word", "<!-- BEGIN BEADS INTEGRATIONS -->\nA\n" + end + "\n"},
		{"END with extra text", begin + "\nA\n<!-- END BEADS INTEGRATION v:1 -->\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := UpsertSharedSection(tt.content, sharedBody); !errors.Is(err, ErrMalformedSection) || got != tt.content {
				t.Errorf("UpsertSharedSection = %q, %v; want the content back and ErrMalformedSection", got, err)
			}
			if got, stripped, _, err := StripSharedSection(tt.content); !errors.Is(err, ErrMalformedSection) || stripped || got != tt.content {
				t.Errorf("StripSharedSection = %q, %v, %v; want the content back and ErrMalformedSection", got, stripped, err)
			}
			path := filepath.Join(t.TempDir(), "instructions.md")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := PreflightSharedFile(path); !errors.Is(err, ErrMalformedSection) {
				t.Errorf("PreflightSharedFile = %v, want ErrMalformedSection", err)
			}
			if _, err := InstallSharedFile(path, sharedBody); !errors.Is(err, ErrMalformedSection) {
				t.Errorf("InstallSharedFile = %v, want ErrMalformedSection", err)
			}
			if _, err := RemoveSharedFile(path); !errors.Is(err, ErrMalformedSection) {
				t.Errorf("RemoveSharedFile = %v, want ErrMalformedSection", err)
			}
			if data, _ := os.ReadFile(path); string(data) != tt.content {
				t.Errorf("file changed to %q", data)
			}
		})
	}
}

func TestSharedSectionPreflight(t *testing.T) {
	dir := t.TempDir()
	if err := PreflightSharedFile(filepath.Join(dir, "missing.md")); err != nil {
		t.Errorf("PreflightSharedFile(missing) = %v, want nil", err)
	}
	ok := filepath.Join(dir, "ok.md")
	if err := os.WriteFile(ok, []byte("# Mine\n\n"+sharedWrapped()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PreflightSharedFile(ok); err != nil {
		t.Errorf("PreflightSharedFile(section) = %v, want nil", err)
	}
	// A directory where the file should be cannot be read.
	if err := PreflightSharedFile(dir); err == nil {
		t.Errorf("PreflightSharedFile(directory) = nil, want a read error")
	}
}
