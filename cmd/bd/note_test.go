package main

import "testing"

// TestValidateNoteArgs is a pure unit test for the "note" command's Args
// guard — it calls the validator directly with no store, no Dolt, and no
// cobra dispatch, so it runs regardless of cgo/Docker availability.
// Message-content assertions for the two rejected cases live in the
// CLI-level TestCLI_Note{List,Add}MisplacedSyntax tests; this test only
// pins down which shapes are accepted vs rejected. Mirrors
// TestValidateCommentArgs (#5369) for GH#5370.
func TestValidateNoteArgs(t *testing.T) {
	origJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = origJSONOutput })

	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "bare list is rejected (the reported typo)", args: []string{"list", "some text"}, wantErr: true},
		{name: "bare list with no text is still rejected", args: []string{"list"}, wantErr: true},
		{name: "bare add is rejected (mirrors comment swapped-add)", args: []string{"add", "some text"}, wantErr: true},
		{name: "bare show is rejected (invited by show-hint error text)", args: []string{"show", "some text"}, wantErr: true},
		{name: "bare edit is rejected", args: []string{"edit", "some text"}, wantErr: true},
		{name: "bare rm is rejected", args: []string{"rm", "some text"}, wantErr: true},
		{name: "bare remove is rejected", args: []string{"remove", "some text"}, wantErr: true},
		{name: "bare delete is rejected", args: []string{"delete", "some text"}, wantErr: true},
		{name: "bare update is rejected (invited by update-hint error text)", args: []string{"update", "some text"}, wantErr: true},
		{name: "bare update with no text is still rejected", args: []string{"update"}, wantErr: true},
		{name: "real id with text starting with the word list is fine", args: []string{"test-abc123", "list", "of", "things", "to", "do"}, wantErr: false},
		{name: "real id with text starting with the word add is fine", args: []string{"test-abc123", "add", "one", "more", "item"}, wantErr: false},
		{name: "real id with text starting with the word show is fine", args: []string{"test-abc123", "show", "me", "the", "notes"}, wantErr: false},
		{name: "real id with text starting with the word update is fine", args: []string{"test-abc123", "update", "the", "notes", "please"}, wantErr: false},
		{name: "real id alone (text comes from --stdin/--file) is fine", args: []string{"test-abc123"}, wantErr: false},
		{name: "no args at all is rejected by the base MinimumNArgs check", args: []string{}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNoteArgs(noteCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Fatalf("validateNoteArgs(%q): expected an error, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateNoteArgs(%q): expected no error, got %v", tc.args, err)
			}
		})
	}
}
