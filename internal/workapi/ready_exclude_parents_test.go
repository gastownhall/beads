package workapi

import (
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// GH#6817: ReadyRequest.ExcludeParentsWithOpenChildren is `bd ready
// --exclude-parents`, and BuildReadyFilter is the single definition of what
// `bd ready` means — a field dropped here is a flag the CLI accepts and the
// query silently ignores, which is exactly the silently-wider-answer defect
// this builder exists to prevent. The count filter is the same builder one
// step down, so it inherits the copy; the assertion pins that too.
func TestBuildReadyFilterCarriesExcludeParentsWithOpenChildren(t *testing.T) {
	off, err := BuildReadyFilter(issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	if off.ExcludeParentsWithOpenChildren {
		t.Errorf("an unflagged request must resolve ExcludeParentsWithOpenChildren to false")
	}

	on, err := BuildReadyFilter(issueops.ReadyRequest{ExcludeParentsWithOpenChildren: true})
	if err != nil {
		t.Fatalf("BuildReadyFilter (flagged): %v", err)
	}
	if !on.ExcludeParentsWithOpenChildren {
		t.Errorf("BuildReadyFilter dropped ExcludeParentsWithOpenChildren: the flag would silently widen the answer")
	}

	counted, err := BuildReadyCountFilter(issueops.ReadyRequest{ExcludeParentsWithOpenChildren: true})
	if err != nil {
		t.Fatalf("BuildReadyCountFilter (flagged): %v", err)
	}
	if !counted.ExcludeParentsWithOpenChildren {
		t.Errorf("BuildReadyCountFilter dropped ExcludeParentsWithOpenChildren: the published total would count parents the listing hides")
	}
}
