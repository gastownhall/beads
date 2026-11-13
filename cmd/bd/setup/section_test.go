package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedLegacy pins a frozen legacy template for classification: an exact
// whole-file match an earlier bd release would have written.
const sharedLegacy = "# Beads guidance\n\nRun `bd prime`.\n"

// sharedBody is a stand-in instruction body.
const sharedBody = "# Beads guidance, current\n\nRun `bd prime`.\n"

func TestSharedSectionClassify(t *testing.T) {
	const begin = "<!-- BEGIN BEADS INTEGRATION -->"
	const metaBegin = "<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:0123abcd -->"
	const end = "<!-- END BEADS INTEGRATION -->"
	for _, tt := range []struct {
		name    string
		content string
		legacy  string
		want    SharedSectionKind
	}{
		{name: "empty file", content: "", legacy: sharedLegacy, want: SharedSectionNone},
		{name: "user content only", content: "# My rules\n", legacy: sharedLegacy, want: SharedSectionNone},
		{name: "bare markers", content: "before\n\n" + begin + "\nbody\n" + end + "\nafter\n", legacy: sharedLegacy, want: SharedSectionPresent},
		{name: "metadata begin marker", content: metaBegin + "\nbody\n" + end + "\n", legacy: sharedLegacy, want: SharedSectionPresent},
		{name: "section only, no outside text", content: begin + "\n" + sharedBody + end + "\n", legacy: sharedLegacy, want: SharedSectionPresent},
		{name: "exact legacy file", content: sharedLegacy, legacy: sharedLegacy, want: SharedSectionLegacy},
		{name: "legacy with CRLF line endings", content: strings.ReplaceAll(sharedLegacy, "\n", "\r\n"), legacy: sharedLegacy, want: SharedSectionLegacy},
		{name: "legacy with trailing whitespace", content: sharedLegacy + "\n\n", legacy: sharedLegacy, want: SharedSectionLegacy},
		{name: "legacy plus user additions is the user's", content: sharedLegacy + "# My addition\n", legacy: sharedLegacy, want: SharedSectionNone},
		{name: "no pinned legacy", content: sharedLegacy, legacy: "", want: SharedSectionNone},
		{name: "user content matching no pin", content: "# My rules\n", legacy: "", want: SharedSectionNone},
		{name: "BEGIN without END", content: begin + "\nbody\n", legacy: sharedLegacy, want: SharedSectionMalformed},
		{name: "END without BEGIN", content: "body\n" + end + "\n", legacy: sharedLegacy, want: SharedSectionMalformed},
		{name: "END before BEGIN", content: end + "\nbody\n" + begin + "\n", legacy: sharedLegacy, want: SharedSectionMalformed},
		{name: "duplicate pair", content: begin + "\nbody\n" + end + "\n" + begin + "\nmore\n" + end + "\n", legacy: sharedLegacy, want: SharedSectionMalformed},
		{name: "nested pair", content: begin + "\nbody\n" + begin + "\nmore\n" + end + "\n" + end + "\n", legacy: sharedLegacy, want: SharedSectionMalformed},
		{name: "incomplete BEGIN comment", content: begin[:len(begin)-4] + "\nbody\n" + end + "\n", legacy: sharedLegacy, want: SharedSectionMalformed},
		{name: "incomplete BEGIN comment closing later", content: begin[:len(begin)-4] + " still the marker line\n-->\nbody\n" + end + "\n", legacy: sharedLegacy, want: SharedSectionMalformed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifySharedSection(tt.content, tt.legacy); got != tt.want {
				t.Fatalf("ClassifySharedSection(%q, pin=%v) = %d, want %d", tt.content, tt.legacy != "", got, tt.want)
			}
		})
	}
}

func TestSharedSectionUpsertText(t *testing.T) {
	const begin = agentsBeginMarker
	const end = agentsEndMarker
	wrapped := begin + "\n" + sharedBody + end + "\n"
	for _, tt := range []struct {
		name    string
		content string
		want    string
		wantErr error
	}{
		{name: "empty file takes the wrapped body alone", content: "", want: wrapped},
		{name: "user content keeps its bytes as a prefix", content: "# My rules\n", want: "# My rules\n\n" + wrapped},
		{name: "user content without a final newline", content: "# My rules", want: "# My rules\n\n" + wrapped},
		{
			name:    "whitespace-only content is kept, not discarded",
			content: " \n\t\n",
			want:    " \n\t\n\n" + wrapped,
		},
		{
			name:    "present section is replaced, outside bytes untouched",
			content: "before\n\n" + begin + "\nold guidance\n" + end + "\nafter\n",
			want:    "before\n\n" + begin + "\n" + sharedBody + end + "\nafter\n",
		},
		{
			name:    "present section at end of file",
			content: "# My rules\n\n" + begin + "\nold guidance\n" + end + "\n",
			want:    "# My rules\n\n" + begin + "\n" + sharedBody + end + "\n",
		},
		{name: "legacy file is converted, not duplicated", content: sharedLegacy, want: wrapped},
		{
			name:    "malformed markers are an error",
			content: begin + "\nbody\n",
			wantErr: ErrSharedSectionMalformed,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := upsertSharedSectionText(tt.content, sharedBody, sharedLegacy)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("upsertSharedSectionText error = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got != tt.want {
				t.Fatalf("upsertSharedSectionText =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestSharedSectionUpsertFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copilot-instructions.md")

	// A missing file gets the wrapped body, a fresh file its permissions.
	wrote, err := UpsertSharedSection(path, sharedBody, sharedLegacy)
	if err != nil || !wrote {
		t.Fatalf("UpsertSharedSection(missing) = %v, %v; want wrote, nil", wrote, err)
	}
	want := agentsBeginMarker + "\n" + sharedBody + agentsEndMarker + "\n"
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("file = %q, %v; want %q", got, err, want)
	}

	// An empty file is treated like a missing one.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertSharedSection(path, sharedBody, sharedLegacy); err != nil {
		t.Fatalf("UpsertSharedSection(empty): %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("file = %q, %v; want %q", got, err, want)
	}

	// A file with malformed markers is left alone, and the error says so.
	malformed := agentsBeginMarker + "\nbody\n"
	if err := os.WriteFile(path, []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertSharedSection(path, sharedBody, sharedLegacy); !errors.Is(err, ErrSharedSectionMalformed) {
		t.Fatalf("UpsertSharedSection(malformed) error = %v, want %v", err, ErrSharedSectionMalformed)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != malformed {
		t.Fatalf("malformed file was changed: %q, %v", got, err)
	}
}
