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
//   - A file that already carried such a flat key had it updated in place, but
//     the direct reader did not understand that spelling.
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
			// Do not introduce a new flat spelling. An existing flat spelling is
			// preserved because another config writer may own that representation,
			// and GetStringFromDir now reads it consistently with Viper.
			for _, line := range strings.Split(string(body), "\n") {
				if line != strings.TrimSpace(line) || strings.HasPrefix(line, "#") {
					continue // indented: inside a mapping. commented: not a key.
				}
				name, _, isKeyValue := strings.Cut(line, ":")
				name = strings.TrimSpace(name)
				if isKeyValue && writes[name] != "" && !hasLiveFlatKey(tc.seed, name) {
					t.Errorf("the write introduced a live flat %q:\n%s", name, body)
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

func hasLiveFlatKey(content, key string) bool {
	prefix := key + ":"
	for _, line := range strings.Split(content, "\n") {
		if line == strings.TrimSpace(line) && strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// Keys the caller does not own are left exactly as written. Rewriting the whole
// file would be bd deciding how someone else's config should look.
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

func TestDottedWritePreservesExistingFlatSpelling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "# managed by another writer\ndolt.host: 10.0.0.1\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := SetYamlConfigInDir(dir, "dolt.host", "127.0.0.1"); err != nil {
		t.Fatalf("set: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "dolt.host: 127.0.0.1") {
		t.Fatalf("existing flat spelling was not updated in place:\n%s", text)
	}
	if strings.Contains(text, "dolt:\n") {
		t.Fatalf("writer replaced the existing flat spelling with a nested mapping:\n%s", text)
	}
	if got := GetStringFromDir(dir, "dolt.host"); got != "127.0.0.1" {
		t.Fatalf("GetStringFromDir(dolt.host) = %q, want %q", got, "127.0.0.1")
	}
}

// Updating an existing flat key must change that key's line and nothing else.
// The branch exists to leave a file other writers share alone, so rewriting the
// rest of it to bd's taste defeats the point: config.yaml is git-tracked, and a
// one-key set that re-indents unrelated sections and drops blank separators is a
// whole-file diff nobody asked for (bd-zj95 review, Finding 2).
//
// The key under test carries the trailing note, not just a bystander: an
// assertion that only watches other lines cannot see the rewritten line lose its
// own annotation, which is the one way "rewrite only this line" still destroys
// content (bd-zj95 review iteration 2, Finding 3).
func TestDottedSetOnAnExistingFlatKeyRewritesOnlyThatLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "# top comment\n\nnode_id: mini   # trailing note\n\n# section\ndolt.host: 10.0.0.1  # staging box, do not change\n\nsync:\n  branch: main\n  extras:\n    - a\n    - b\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := SetYamlConfigInDir(dir, "dolt.host", "127.0.0.1"); err != nil {
		t.Fatalf("set: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := strings.Replace(seed, "dolt.host: 10.0.0.1", "dolt.host: 127.0.0.1", 1)
	if string(body) != want {
		t.Errorf("a one-key set rewrote more than the key's line:\ngot:\n%s\nwant:\n%s", body, want)
	}
	if got := GetStringFromDir(dir, "dolt.host"); got != "127.0.0.1" {
		t.Errorf("dolt.host reads back as %q\n%s", got, body)
	}
}

// The comment the line rewrite has to carry over is found by matching yaml.v3's
// parsed comment text back from the right, so a "#" the value itself contains is
// never read as the start of one. An empty value is the shape where yaml.v3 has
// no value node line to hang the comment on and puts it on the key instead.
func TestDottedSetKeepsTheTrailingCommentOnTheKeyItRewrites(t *testing.T) {
	cases := []struct {
		name string
		seed string
		want string
	}{
		{
			name: "note after a plain value",
			seed: "dolt.host: 10.0.0.1  # staging box\nnode_id: mini\n",
			want: "dolt.host: 127.0.0.1  # staging box\nnode_id: mini\n",
		},
		{
			name: "note after a value containing a hash",
			seed: "dolt.host: 'a # b'  # the real note\nnode_id: mini\n",
			want: "dolt.host: 127.0.0.1  # the real note\nnode_id: mini\n",
		},
		{
			name: "note on a key with no value",
			seed: "dolt.host:\t# not set yet\nnode_id: mini\n",
			want: "dolt.host: 127.0.0.1\t# not set yet\nnode_id: mini\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}

			if err := SetYamlConfigInDir(dir, "dolt.host", "127.0.0.1"); err != nil {
				t.Fatalf("set: %v", err)
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(body) != tc.want {
				t.Errorf("the rewritten line did not keep its trailing comment:\ngot:\n%s\nwant:\n%s", body, tc.want)
			}
			if got := GetStringFromDir(dir, "dolt.host"); got != "127.0.0.1" {
				t.Errorf("dolt.host reads back as %q\n%s", got, body)
			}
			if got := GetStringFromDir(dir, "node_id"); got != "mini" {
				t.Errorf("node_id reads back as %q\n%s", got, body)
			}
		})
	}
}

// Rewriting the key's line means rewriting the line the key is ACTUALLY on,
// whatever it looks like. A quoted flat key is still that key — bd never writes
// one, but a shared config.yaml can arrive with one, and a writer that matches
// the unquoted spelling by text finds nothing and appends a second flat key
// beside the first, leaving the value it just "set" unreadable.
func TestDottedSetFindsAQuotedFlatKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "\"dolt.host\": 10.0.0.1\nnode_id: mini\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := SetYamlConfigInDir(dir, "dolt.host", "127.0.0.1"); err != nil {
		t.Fatalf("set: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := GetStringFromDir(dir, "dolt.host"); got != "127.0.0.1" {
		t.Errorf("dolt.host reads back as %q\n%s", got, body)
	}
	if want := strings.Replace(seed, "10.0.0.1", "127.0.0.1", 1); string(body) != want {
		t.Errorf("the write did not update the quoted key in place:\ngot:\n%s\nwant:\n%s", body, want)
	}
}

// A flat key whose value does not fit on the key's own line has no single line
// to swap. Correctness wins over formatting there: the value must still read
// back, even though the document gets marshaled and reformatted to do it.
func TestDottedSetOnAMultiLineFlatKeyStillWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "dolt.host: |\n  10.0.0.1\nnode_id: mini\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := SetYamlConfigInDir(dir, "dolt.host", "127.0.0.1"); err != nil {
		t.Fatalf("set: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := GetStringFromDir(dir, "dolt.host"); got != "127.0.0.1" {
		t.Errorf("dolt.host reads back as %q\n%s", got, body)
	}
	if got := GetStringFromDir(dir, "node_id"); got != "mini" {
		t.Errorf("node_id reads back as %q\n%s", got, body)
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
// The over-match cases matter as much as the removal cases, and the first
// version of this table could not express them: every case put the leaf exactly
// one level under a top-level parent, so it was blind to a walk that matched a
// leaf name at the wrong depth or under the wrong parent. Each survivor below is
// a key the unset was never asked about, and each one was destroyed — or left
// live while a bystander died — by the descent this test now pins (bd-zj95
// review, Finding 1).
func TestUnsetRemovesADottedKeyInEveryShape(t *testing.T) {
	cases := []struct {
		name string
		seed string
		key  string
		// survivors are keys the unset must not touch, and the values they must
		// still read back afterwards.
		survivors map[string]string
	}{
		{name: "nested", seed: "sync:\n    remote: \"file:///origin.git\"\n", key: "sync.remote"},
		{
			name:      "nested among siblings",
			seed:      "sync:\n    branch: beads-sync\n    remote: \"file:///origin.git\"\n",
			key:       "sync.remote",
			survivors: map[string]string{"sync.branch": "beads-sync"},
		},
		{name: "legacy flat", seed: "sync.remote: \"file:///origin.git\"\n", key: "sync.remote"},
		{
			name:      "nested with other sections",
			seed:      "dolt:\n    port: 3307\nsync:\n    remote: \"file:///origin.git\"\n",
			key:       "sync.remote",
			survivors: map[string]string{"dolt.port": "3307"},
		},
		{
			// The target is present AND a deeper key repeats its name. The walk
			// used to take the deeper one and report success, so the operator
			// lost sync.sub.remote and kept the sync.remote they asked to forget.
			name:      "deeper key repeats the leaf name",
			seed:      "sync:\n    sub:\n        remote: keep\n    remote: target\n",
			key:       "sync.remote",
			survivors: map[string]string{"sync.sub.remote": "keep"},
		},
		{
			// Same shape, two spaces of indent and a scalar sibling after the
			// interposed section: the target does not exist at all here, so the
			// only correct outcome is to change nothing.
			name:      "leaf repeats under a section the key does not own",
			seed:      "sync:\n  nested:\n    remote: should-survive\n  other: x\n",
			key:       "sync.remote",
			survivors: map[string]string{"sync.nested.remote": "should-survive", "sync.other": "x"},
		},
		{
			// The key's own section name repeated under a foreign top-level key.
			// Segment 0 used to match at any indent, so this was read as the
			// sync section and a key at a completely different path was cut.
			name:      "section name repeats under a foreign parent",
			seed:      "other:\n    sync:\n        remote: keep\n",
			key:       "sync.remote",
			survivors: map[string]string{"other.sync.remote": "keep"},
		},
		{
			// Both spellings present: the real dolt.host at top level and a
			// namesake under `other`. The unset used to comment out both.
			name:      "target and namesake in the same file",
			seed:      "other:\n  dolt:\n    host: should-survive\ndolt:\n  host: kill-me\n",
			key:       "dolt.host",
			survivors: map[string]string{"other.dolt.host": "should-survive"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			// Whether the key was set to begin with decides what "commented out
			// as documentation" can mean below: an unset of a key that was never
			// there is a no-op, not a removal.
			wasSet := GetStringFromDir(dir, tc.key) != ""

			t.Setenv("BEADS_DIR", dir)
			if err := UnsetYamlConfig(tc.key); err != nil {
				t.Fatalf("UnsetYamlConfig: %v", err)
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := GetStringFromDir(dir, tc.key); got != "" {
				t.Errorf("%s still reads back as %q after unset:\n%s", tc.key, got, body)
			}
			leaf := tc.key[strings.LastIndex(tc.key, ".")+1:]
			// The key is preserved as documentation, which is this function's
			// stated contract, so it must still be visible — commented.
			if wasSet && !strings.Contains(string(body), leaf+":") {
				t.Errorf("unset removed the key instead of commenting it out:\n%s", body)
			}
			// Anything the unset was not asked about is none of its business.
			for key, want := range tc.survivors {
				if got := GetStringFromDir(dir, key); got != want {
					t.Errorf("unset touched a key it was not asked about: %s = %q, want %q\n%s",
						key, got, want, body)
				}
			}
		})
	}
}

// bd cannot comment out a key it cannot find by line, and it used to pretend
// otherwise in both directions: a flow-style mapping came back unchanged with a
// success exit, leaving live the value the operator asked bd to forget — the
// PR's own headline symptom — and a block scalar had its key line commented
// while the body stayed behind, re-parsing as a plain multi-line value of the
// PARENT. Say so instead, and leave the file alone (bd-zj95 review, Finding 3).
func TestUnsetRefusesShapesItCannotEdit(t *testing.T) {
	cases := []struct {
		name string
		seed string
		key  string
		// want is a substring the error must carry beyond the key name.
		want string
	}{
		{name: "flow style section", seed: "sync: {remote: \"file:///origin.git\"}\n", key: "sync.remote", want: "flow style"},
		{name: "flow style nested deeper", seed: "dolt:\n    limits: {host: mini}\n", key: "dolt.limits.host", want: "flow style"},
		{name: "literal block scalar", seed: "sync:\n    remote: |\n        line1\n        line2\n", key: "sync.remote", want: "block scalar"},
		{name: "folded block scalar", seed: "sync:\n    remote: >\n        line1\n", key: "sync.remote", want: "block scalar"},
		// The flat spelling reaches the same body-left-behind outcome by the
		// other half of the line matcher, and the orphan lands at the top level,
		// so the whole file stops parsing rather than one section.
		{name: "block scalar under the flat spelling", seed: "dolt.host: |\n    10.0.0.1\nnode_id: mini\n", key: "dolt.host", want: "block scalar"},
		{name: "folded block scalar under the flat spelling", seed: "dolt.host: >\n    10.0.0.1\nnode_id: mini\n", key: "dolt.host", want: "block scalar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}

			t.Setenv("BEADS_DIR", dir)
			err := UnsetYamlConfig(tc.key)
			if err == nil {
				body, _ := os.ReadFile(path) //nolint:errcheck // diagnostic
				t.Fatalf("unsetting %s in a %s reported success\n%s", tc.key, tc.name, body)
			}
			if !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error names neither the key nor the shape: %v", err)
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(body) != tc.seed {
				t.Errorf("a refused unset still changed the file:\n%s", body)
			}
		})
	}
}

// A shape bd cannot edit is only worth refusing when the key is actually there.
// Unsetting a key that was never set has always been a successful no-op, and a
// flow-style mapping elsewhere in the file is not the operator's problem.
func TestUnsetOfAnAbsentKeyStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "sync: {branch: beads-sync}\ndolt:\n    port: 3307\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Setenv("BEADS_DIR", dir)
	if err := UnsetYamlConfig("sync.remote"); err != nil {
		t.Fatalf("UnsetYamlConfig on an unset key: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := GetStringFromDir(dir, "sync.branch"); got != "beads-sync" {
		t.Errorf("the no-op unset changed sync.branch to %q:\n%s", got, body)
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
