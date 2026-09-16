package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dotted key must round-trip: whatever SetYamlConfigInDir writes,
// GetStringFromDir must read back. That is the whole contract between the two,
// and it is not a property either function can hold alone.
//
// It was broken in two directions, and both were reachable from ordinary files:
//
//   - An empty or comment-only config.yaml has no mapping node, so the nested
//     writer declined and the caller appended a literal `dolt.host: ...` line —
//     a key whose NAME contains a dot. GetStringFromDir splits on the dot and
//     looks for a nested mapping, so it never finds it.
//   - A file that already carried such a flat key had it updated in place,
//     which preserved the unreadable shape forever.
//
// The observable consequence was a caller writing a value and immediately being
// unable to read it back. Both consumers found it the hard way (bd-zj95).
func TestDottedKeysRoundTripThroughEveryConfigShape(t *testing.T) {
	cases := []struct {
		name string
		seed string
		// present is true when the file should exist before the write.
		present bool
	}{
		{name: "absent file", present: false},
		{name: "empty file", seed: "", present: true},
		{name: "comment only", seed: "# a workspace config\n", present: true},
		{name: "flat dotted keys", seed: "dolt.host: 10.0.0.1\ndolt.port: 3307\n", present: true},
		{name: "flat commented key", seed: "# dolt.host: 10.0.0.1\n", present: true},
		{name: "existing dolt section", seed: "dolt:\n    host: 10.0.0.1\n", present: true},
		{name: "unrelated nested section", seed: "sync:\n    branch: beads-sync\n", present: true},
		{name: "unrelated flat key", seed: "node_id: somewhere\n", present: true},
		{name: "mixed flat and nested", seed: "dolt.host: 10.0.0.1\ndolt:\n    port: 3307\n", present: true},
	}

	writes := map[string]string{
		"dolt.host":       "127.0.0.1",
		"dolt.port":       "45678",
		"dolt.auto-start": "true",
		"dolt.mode":       "server",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if tc.present {
				if err := os.WriteFile(path, []byte(tc.seed), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			} else {
				// SetYamlConfigInDir refuses a workspace with no config.yaml at
				// all; that refusal is deliberate and not what this test is
				// about, so give it the empty file it asks for.
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatalf("create: %v", err)
				}
			}

			for key, value := range writes {
				if err := SetYamlConfigInDir(dir, key, value); err != nil {
					t.Fatalf("SetYamlConfigInDir(%q): %v", key, err)
				}
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			for key, want := range writes {
				if got := GetStringFromDir(dir, key); got != want {
					t.Errorf("GetStringFromDir(%q) = %q, want %q\nfile:\n%s", key, got, want, body)
				}
			}
			// No LIVE flat key may survive: it is the unreadable shape, and a
			// reader that happens to prefer it finds a stale answer. A commented
			// one is inert — nothing reads it — and it is the operator's text,
			// which the assertion below says bd has to leave alone.
			for _, line := range strings.Split(string(body), "\n") {
				if line != strings.TrimSpace(line) || strings.HasPrefix(line, "#") {
					continue // indented: inside a mapping. commented: not a key.
				}
				name, _, isKeyValue := strings.Cut(line, ":")
				if isKeyValue && writes[strings.TrimSpace(name)] != "" {
					t.Errorf("a live flat %q survived the write:\n%s", strings.TrimSpace(name), body)
				}
			}
			// Comments the file arrived with are the operator's, and a write that
			// drops them is a committed diff nobody asked for. `bd init` writes a
			// template that is nothing BUT comments, so this is the common case,
			// not an exotic one.
			for _, line := range strings.Split(tc.seed, "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "#") {
					continue
				}
				if !strings.Contains(string(body), line) {
					t.Errorf("the write dropped the comment %q:\n%s", line, body)
				}
			}
		})
	}
}

// Keys the caller owns are left exactly as written. Migrating a flat key is only
// correct for the key being written; rewriting the whole file would be bd
// deciding how someone else's config should look.
func TestDottedWriteLeavesOtherKeysAlone(t *testing.T) {
	dir := t.TempDir()
	seed := "# keep this comment\nsync.branch: keep-me\ndolt.host: 10.0.0.1\nnode_id: mini\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := SetYamlConfigInDir(dir, "dolt.host", "127.0.0.1"); err != nil {
		t.Fatalf("set: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "sync.branch: keep-me") {
		t.Errorf("a flat key the write does not own was rewritten:\n%s", text)
	}
	if !strings.Contains(text, "node_id: mini") {
		t.Errorf("an unrelated key was lost:\n%s", text)
	}
	if got := GetStringFromDir(dir, "dolt.host"); got != "127.0.0.1" {
		t.Errorf("dolt.host reads back as %q\n%s", got, text)
	}
}

// A single-segment key has no nesting to do and must keep working exactly as it
// did: this is the shape most of bd's config keys have.
func TestUndottedKeysAreUnaffected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("# seed\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SetYamlConfigInDir(dir, "node_id", "mini"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := GetStringFromDir(dir, "node_id"); got != "mini" {
		body, _ := os.ReadFile(filepath.Join(dir, "config.yaml")) //nolint:errcheck // diagnostic
		t.Fatalf("node_id reads back as %q\n%s", got, body)
	}
}

// Writing a dotted key nested and then being unable to unset it is the same
// round-trip break in the other direction: UnsetYamlConfig comments out the
// line matching the key, and its pattern only ever matched a FLAT
// `sync.remote:` line. Once the writer nests, an unset silently does nothing
// and the value stays live — which for sync.remote means bd keeps a remote the
// operator asked it to forget.
func TestUnsetRemovesADottedKeyInEveryShape(t *testing.T) {
	cases := []struct {
		name string
		seed string
	}{
		{name: "nested", seed: "sync:\n    remote: \"file:///origin.git\"\n"},
		{name: "nested among siblings", seed: "sync:\n    branch: beads-sync\n    remote: \"file:///origin.git\"\n"},
		{name: "legacy flat", seed: "sync.remote: \"file:///origin.git\"\n"},
		{name: "nested with other sections", seed: "dolt:\n    port: 3307\nsync:\n    remote: \"file:///origin.git\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}

			t.Setenv("BEADS_DIR", dir)
			if err := UnsetYamlConfig("sync.remote"); err != nil {
				t.Fatalf("UnsetYamlConfig: %v", err)
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := GetStringFromDir(dir, "sync.remote"); got != "" {
				t.Errorf("sync.remote still reads back as %q after unset:\n%s", got, body)
			}
			// The key is preserved as documentation, which is this function's
			// stated contract, so it must still be visible — commented.
			if !strings.Contains(string(body), "remote:") {
				t.Errorf("unset removed the key instead of commenting it out:\n%s", body)
			}
			// A sibling under the same section is none of the unset's business.
			if tc.name == "nested among siblings" {
				if got := GetStringFromDir(dir, "sync.branch"); got != "beads-sync" {
					t.Errorf("unset took a sibling with it: sync.branch = %q\n%s", got, body)
				}
			}
			if tc.name == "nested with other sections" {
				if got := GetStringFromDir(dir, "dolt.port"); got != "3307" {
					t.Errorf("unset touched an unrelated section: dolt.port = %q\n%s", got, body)
				}
			}
		})
	}
}

// set, unset, set is one command sequence, not three independent ones, and the
// round trip has to survive all of it. An unset comments the leaf out and leaves
// the section behind holding nothing — `sync:` and no more. The writer used to
// refuse that section, because a null is not a mapping, and fell through to the
// flat writer, which put a literal `sync.remote:` key back into the file: the
// unreadable shape, re-created one command after being fixed. bd said "Set
// sync.remote = ..." while GetStringFromDir saw nothing.
func TestSettingADottedKeyAgainAfterUnsetStaysNested(t *testing.T) {
	cases := []struct {
		name string
		seed string
	}{
		// The section is left holding nothing at all once its only leaf is
		// commented out. This is the shape that broke.
		{name: "section empties out", seed: "node_id: mini\n"},
		// A sibling keeps the section a mapping, so this shape always worked.
		// Pin it anyway: the fix must not trade one for the other.
		{name: "sibling keeps the section", seed: "sync:\n    branch: beads-sync\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			t.Setenv("BEADS_DIR", dir)

			if err := SetYamlConfigInDir(dir, "sync.remote", "file:///a.git"); err != nil {
				t.Fatalf("first set: %v", err)
			}
			if err := UnsetYamlConfig("sync.remote"); err != nil {
				t.Fatalf("unset: %v", err)
			}
			if got := GetStringFromDir(dir, "sync.remote"); got != "" {
				body, _ := os.ReadFile(path) //nolint:errcheck // diagnostic
				t.Fatalf("unset left sync.remote at %q:\n%s", got, body)
			}
			if err := SetYamlConfigInDir(dir, "sync.remote", "file:///b.git"); err != nil {
				t.Fatalf("second set: %v", err)
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := GetStringFromDir(dir, "sync.remote"); got != "file:///b.git" {
				t.Errorf("sync.remote reads back as %q after set/unset/set:\n%s", got, body)
			}
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "sync.remote:") {
					t.Errorf("the unreadable flat spelling came back:\n%s", body)
				}
			}
			if tc.name == "sibling keeps the section" {
				if got := GetStringFromDir(dir, "sync.branch"); got != "beads-sync" {
					t.Errorf("the sibling was lost: sync.branch = %q\n%s", got, body)
				}
			}
		})
	}
}

// A config.yaml that is nothing but comments is not an edge case: it is what
// `bd init` writes, so it is the shape every fresh workspace's FIRST dotted
// write lands on — and config.yaml is git-tracked, so losing it is a committed
// diff the operator never asked for.
//
// yaml.v3 parses such a document to no nodes at all and keeps none of its text,
// so a writer that marshals a node tree has nothing to write back but the key it
// just added. Asserting the value reads back is not enough to catch that; the
// file is only correct if everything that was in it is still in it.
func TestFirstDottedWriteKeepsACommentOnlyFile(t *testing.T) {
	seed := `# Beads Configuration File
# This file configures default behavior for all bd commands in this repository

# Issue prefix for this repository (used by bd init)
# issue-prefix: ""

# Use no-db mode: JSONL-only, no Dolt database
# no-db: false

# Default actor for audit trails (overridden by BEADS_ACTOR or --actor)
# actor: ""
`

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Two writes: the first lands on the comment-only file, the second on
	// whatever the first produced. Both have to keep the comments.
	if err := SetYamlConfigInDir(dir, "sync.remote", "file:///origin.git"); err != nil {
		t.Fatalf("set sync.remote: %v", err)
	}
	if err := SetYamlConfigInDir(dir, "dolt.port", "3307"); err != nil {
		t.Fatalf("set dolt.port: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(body)
	for _, line := range strings.Split(strings.TrimRight(seed, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(text, line) {
			t.Errorf("the write dropped a line that was already in the file: %q\ngot:\n%s", line, text)
		}
	}
	if got := GetStringFromDir(dir, "sync.remote"); got != "file:///origin.git" {
		t.Errorf("sync.remote reads back as %q\n%s", got, text)
	}
	if got := GetStringFromDir(dir, "dolt.port"); got != "3307" {
		t.Errorf("dolt.port reads back as %q\n%s", got, text)
	}
}

// When a key's own parent already holds a value there is no section to nest
// under it, and bd has nothing correct to write. It used to write the flat
// `sync.remote:` spelling and report success, so the operator was told the
// value was set and every reader that splits on the dot saw nothing. Say so
// instead, and leave the file alone.
func TestSettingUnderAScalarParentIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "sync: enabled\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := SetYamlConfigInDir(dir, "sync.remote", "file:///origin.git")
	if err == nil {
		body, _ := os.ReadFile(path) //nolint:errcheck // diagnostic
		t.Fatalf("setting sync.remote under a scalar sync reported success\n%s", body)
	}
	if !strings.Contains(err.Error(), "sync") {
		t.Errorf("the error does not name the key in the way: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != seed {
		t.Errorf("a refused write still changed the file:\n%s", body)
	}
}

// Unset matches by line, so it has to know which lines are YAML and which are
// somebody's prose. A literal block scalar's body is data: text indented under
// `notes: |` only looks like a mapping. Commenting inside it edits the value the
// operator wrote.
func TestUnsetLeavesBlockScalarTextAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "notes: |\n  sync:\n    remote: keep-this-text\nsync:\n    remote: \"file:///origin.git\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Setenv("BEADS_DIR", dir)
	if err := UnsetYamlConfig("sync.remote"); err != nil {
		t.Fatalf("UnsetYamlConfig: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "    remote: keep-this-text") {
		t.Errorf("unset rewrote text inside the notes block:\n%s", text)
	}
	if got := GetStringFromDir(dir, "sync.remote"); got != "" {
		t.Errorf("sync.remote still reads back as %q after unset:\n%s", got, text)
	}
	if got := GetStringFromDir(dir, "notes"); got != "sync:\n  remote: keep-this-text\n" {
		t.Errorf("the notes value changed: %q\n%s", got, text)
	}
}
