package main

import "testing"

func TestRenameIDReferences(t *testing.T) {
	t.Parallel()
	// GH#7040: a mention is the old ID standing alone. A longer ID that
	// starts or ends with it names a different issue and is left alone.
	for _, tt := range []struct {
		name, text, oldID, newID, want string
	}{
		{"alone", "test-a", "test-a", "test-z", "test-z"},
		{"sentence period", "see test-a.", "test-a", "test-z", "see test-z."},
		{"ellipsis", "maybe test-a...", "test-a", "test-z", "maybe test-z..."},
		{"comma repeat", "test-a,test-a", "test-a", "test-z", "test-z,test-z"},
		{"repeated", "test-a test-a; test-a!", "test-a", "test-z", "test-z test-z; test-z!"},
		{"brackets", "(test-a) [test-a] `test-a`", "test-a", "test-z", "(test-z) [test-z] `test-z`"},
		{"possessive", "test-a's parent", "test-a", "test-z", "test-z's parent"},
		{"rig qualified", "waits on rig:test-a", "test-a", "test-z", "waits on rig:test-z"},
		{"url path", "https://example.com/issues/test-a/edit", "test-a", "test-z", "https://example.com/issues/test-z/edit"},
		{"lines", "test-a\ntest-a\n", "test-a", "test-z", "test-z\ntest-z\n"},
		{"non-ascii neighbours", "ab—test-a—cd", "test-a", "test-z", "ab—test-z—cd"},
		{"dotted child", "test-a tracks test-a.1 and test-a.1.2", "test-a", "test-z", "test-z tracks test-a.1 and test-a.1.2"},
		{"child period", "see test-a.1.", "test-a", "test-z", "see test-a.1."},
		{"hyphenated extension", "bd-auth-v2 replaces bd-auth", "bd-auth", "bd-login", "bd-auth-v2 replaces bd-login"},
		{"leading prefix", "beads-vscode-1 is not vscode-1", "vscode-1", "vscode-9", "beads-vscode-1 is not vscode-9"},
		{"pre prefix", "pre-test-a", "test-a", "test-z", "pre-test-a"},
		{"underscore", "test-a_x x_test-a", "test-a", "test-z", "test-a_x x_test-a"},
		{"trailing hyphen", "test-a- is open", "test-a", "test-z", "test-a- is open"},
		{"trailing hyphen id", "test-a- and test-a", "test-a-", "test-z", "test-z and test-a"},
		{"case", "Test-A TEST-A", "test-a", "test-z", "Test-A TEST-A"},
		{"no match", "nothing here", "test-a", "test-z", "nothing here"},
		{"empty", "", "test-a", "test-z", ""},
		{"new ID contains old", "test-a and test-a.", "test-a", "test-a-2", "test-a-2 and test-a-2."},
		{"contributor case", "test-a tracks test-a.1; test-a,test-a; see test-a.", "test-a", "test-z", "test-z tracks test-a.1; test-z,test-z; see test-z."},
	} {
		if got := replaceIDRefs(tt.text, tt.oldID, tt.newID); got != tt.want {
			t.Errorf("%s: replaceIDRefs(%q, %q, %q) = %q, want %q", tt.name, tt.text, tt.oldID, tt.newID, got, tt.want)
		}
	}
}
