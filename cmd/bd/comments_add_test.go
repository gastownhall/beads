package main

import "testing"

// TestValidateCommentsAddArgs is a pure unit test for "comments add"'s Args
// guard — it calls the validator directly with no store, no Dolt, and no
// cobra dispatch, so it runs regardless of cgo/Docker availability. Mirrors
// TestValidateCommentArgs (the singular "comment" sibling in comment_test.go)
// for the plural form: PR #5393 review item M1 established that "comments
// add" must reject a reserved-word id exactly like "comment" does, since the
// two are documented as guarded twins (CHANGELOG.md, hash-ids.md).
func TestValidateCommentsAddArgs(t *testing.T) {
	origJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = origJSONOutput })

	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "bare list is rejected", args: []string{"list", "some text"}, wantErr: true},
		{name: "bare add is rejected", args: []string{"add", "some text"}, wantErr: true},
		{name: "bare rm is rejected", args: []string{"rm", "some text"}, wantErr: true},
		{name: "bare delete is rejected", args: []string{"delete", "some text"}, wantErr: true},
		{name: "real id with text starting with the word list is fine", args: []string{"test-abc123", "list", "of", "things", "to", "do"}, wantErr: false},
		{name: "real id with text starting with the word add is fine", args: []string{"test-abc123", "add", "one", "more", "item"}, wantErr: false},
		{name: "real id alone (text comes from --stdin/--file) is fine", args: []string{"test-abc123"}, wantErr: false},
		{name: "no args at all is rejected by the base MinimumNArgs check", args: []string{}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommentsAddArgs(commentsAddCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Fatalf("validateCommentsAddArgs(%q): expected an error, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateCommentsAddArgs(%q): expected no error, got %v", tc.args, err)
			}
		})
	}
}
