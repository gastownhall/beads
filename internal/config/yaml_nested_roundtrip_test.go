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
			// The unreadable shape must not survive anywhere in the file, or a
			// reader that happens to prefer it finds a stale answer.
			for _, line := range strings.Split(string(body), "\n") {
				if line != strings.TrimSpace(line) {
					continue // indented: inside a mapping, not a top-level key
				}
				name, _, isKeyValue := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#")), ":")
				if isKeyValue && writes[strings.TrimSpace(name)] != "" {
					t.Errorf("a flat %q survived the write:\n%s", strings.TrimSpace(name), body)
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
