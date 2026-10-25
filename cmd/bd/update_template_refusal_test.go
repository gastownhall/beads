package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestTemplateReadOnlyRefusalKeepsTheCLISentence pins what `bd update`,
// `bd assign` and the proxied `bd tag` print now that the template guard is the
// role's: the sentence they printed when it was a CLI pre-read, naming the id
// as the caller typed it — however the refusal travelled (bare from the store,
// wrapped by the unit of work, or rebuilt by the served client against the
// resolved id).
func TestTemplateReadOnlyRefusalKeepsTheCLISentence(t *testing.T) {
	const typed = "tp-ab"
	want := "cannot modify template " + typed + ": templates are read-only; use 'bd mol pour' to create a work item"
	for name, err := range map[string]error{
		"bare":    &issueops.TemplateReadOnlyError{IssueID: "tp-abc123"},
		"wrapped": fmt.Errorf("update tp-abc123: %w", &issueops.TemplateReadOnlyError{IssueID: "tp-abc123"}),
	} {
		refusal, ok := templateReadOnlyRefusal(typed, err)
		if !ok {
			t.Fatalf("%s: templateReadOnlyRefusal did not recognize %v", name, err)
		}
		if refusal.Error() != want {
			t.Errorf("%s: refusal reads %q, want %q", name, refusal.Error(), want)
		}
	}
	if _, ok := templateReadOnlyRefusal(typed, errors.New("something else")); ok {
		t.Errorf("an unrelated error was taken for the template refusal")
	}
}
