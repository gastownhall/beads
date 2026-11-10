package main

import "testing"

// TestValidateCommentArgs is a pure unit test for the singular "comment"
// shorthand's Args guard — it calls the validator directly with no store, no
// Dolt, and no cobra dispatch, so it runs regardless of cgo/Docker
// availability. Message-content assertions for the two rejected cases live in
// the CLI-level TestCLI_Comment{List,Add}MisplacedSyntax tests alongside the
// plural sibling's equivalents; this test only pins down which shapes are
// accepted vs rejected.
func TestValidateCommentArgs(t *testing.T) {
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
		{name: "bare add is rejected (mirrors comments swapped-add)", args: []string{"add", "some text"}, wantErr: true},
		{name: "real id with text starting with the word list is fine", args: []string{"test-abc123", "list", "of", "things", "to", "do"}, wantErr: false},
		{name: "real id with text starting with the word add is fine", args: []string{"test-abc123", "add", "one", "more", "item"}, wantErr: false},
		{name: "real id alone (text comes from --stdin/--file) is fine", args: []string{"test-abc123"}, wantErr: false},
		{name: "no args at all is rejected by the base MinimumNArgs check", args: []string{}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommentArgs(commentCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Fatalf("validateCommentArgs(%q): expected an error, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateCommentArgs(%q): expected no error, got %v", tc.args, err)
			}
		})
	}
}
