package scripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// R3 (D5): the repository's Actions permissions are allowed_actions=selected
// with sha_pinning_required=true, and .github/allowed-actions.json is the
// exact body of the selected-actions setting:
//
//	gh api -X PUT repos/gastownhall/beads/actions/permissions \
//	  -F enabled=true -f allowed_actions=selected -F sha_pinning_required=true
//	gh api -X PUT repos/gastownhall/beads/actions/permissions/selected-actions \
//	  --input .github/allowed-actions.json
//
// It mirrors gascity's list plus the third-party actions beads uses. A fork
// PR's workflow can then call only these, so it cannot reach
// useblacksmith/stickydisk or another action that writes state trusted runs
// read. This test keeps the workflows inside the setting: every `uses:` under
// .github is local, or pinned to a full commit SHA and allowed by the file.
//
// Actions that third-party composites call are not visible here and must be
// listed by hand: DeterminateSystems/determinate-nix-action calls
// DeterminateSystems/nix-installer-action; codecov/codecov-action and
// actions/upload-pages-artifact call GitHub-owned actions only.

type allowedActions struct {
	GitHubOwnedAllowed bool     `json:"github_owned_allowed"`
	VerifiedAllowed    bool     `json:"verified_allowed"`
	PatternsAllowed    []string `json:"patterns_allowed"`
}

var (
	usesLine     = regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*['"]?([^\s'"#]+)`)
	pinnedAction = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)(/[^@]+)?@[0-9a-f]{40}$`)
)

func readAllowedActions(t *testing.T) allowedActions {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), ".github", "allowed-actions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a allowedActions
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		t.Fatalf(".github/allowed-actions.json: %v", err)
	}
	return a
}

// actionAllowed reports whether owner/repo[/path] is allowed by the setting.
// Patterns are owner/repo@* (any ref; sha_pinning_required fixes the ref).
func actionAllowed(a allowedActions, owner, repo string) bool {
	if a.GitHubOwnedAllowed && (owner == "actions" || owner == "github") {
		return true
	}
	return slices.Contains(a.PatternsAllowed, owner+"/"+repo+"@*")
}

func TestAllowedActionsSettingShape(t *testing.T) {
	a := readAllowedActions(t)
	if !a.GitHubOwnedAllowed {
		t.Error("github_owned_allowed = false; the workflows use actions/*")
	}
	if a.VerifiedAllowed {
		t.Error("verified_allowed = true; every third-party action must be listed by name")
	}
	if !slices.IsSorted(a.PatternsAllowed) {
		t.Errorf("patterns_allowed is not sorted: %v", a.PatternsAllowed)
	}
	for _, p := range a.PatternsAllowed {
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@\*$`).MatchString(p) {
			t.Errorf("pattern %q is not owner/repo@*", p)
		}
		if strings.EqualFold(strings.SplitN(p, "/", 2)[0], "useblacksmith") {
			t.Errorf("pattern %q: Blacksmith's own actions (sticky disks) stay off the list", p)
		}
	}
}

func TestWorkflowActionsArePinnedAndAllowed(t *testing.T) {
	a := readAllowedActions(t)
	root := sourceRepoRoot(t)
	seen := 0
	err := filepath.WalkDir(filepath.Join(root, ".github"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || (!strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml")) {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range usesLine.FindAllStringSubmatch(string(data), -1) {
			ref := m[1]
			seen++
			if strings.HasPrefix(ref, "./") {
				continue
			}
			pm := pinnedAction.FindStringSubmatch(ref)
			if pm == nil {
				t.Errorf("%s: uses %q is not owner/repo[/path]@<40-hex commit SHA> (sha_pinning_required)", rel, ref)
				continue
			}
			if !actionAllowed(a, pm[1], pm[2]) {
				t.Errorf("%s: uses %q, which .github/allowed-actions.json does not allow; add %s/%s@* there and to the repository setting", rel, ref, pm[1], pm[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("found no uses: lines under .github; the walk is broken")
	}
}
