package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// resolveUnresolvedDepTarget is the decision point `bd dep add` reaches when
// its depends-on target could not be resolved locally or via cross-store
// routing (resolveIDWithRouting's error return). It has to separate two things
// the old prefix-comparison heuristic conflated:
//
//   - A "type:id"-shaped positional argument (e.g. "discovered-from:ga-dguvnb",
//     the syntax `bd create --deps` accepts via parseDepSpec) was misparsed:
//     types.ExtractPrefix takes everything up to the first "-", so it read
//     "discovered-" as a bd ID prefix instead of recognizing "discovered-from"
//     as a dependency-type keyword. The intended type was silently dropped
//     (defaulting to blocks) and the whole "type:id" string was stored as a
//     bogus external reference. That is bd-gmdx5, and it is a real bug.
//
//   - A bare, differently-prefixed target (e.g. "xy-999" from a "be-" issue) is
//     NOT a bug. storeissueops.IsExternalDepTarget defines exactly this shape as
//     belonging in depends_on_external, and calls that the single rule every
//     backend classifies by. It is the multi-rig "add now, route later" shape,
//     and `bd dep remove` still addresses such an edge. dep add must keep
//     accepting it, or it would refuse to create an edge the store holds and
//     dep remove can still delete.
func TestResolveUnresolvedDepTarget(t *testing.T) {
	resolveErr := errors.New("issue not found")

	tests := []struct {
		name         string
		sourceID     string
		dependsOnArg string
		wantToID     string
		// wantUnverified is the outcome-3 flag: true only for the bare
		// cross-prefix pass-through, so the caller (GH#6556) knows to warn
		// after the write that the target was accepted unresolved.
		wantUnverified  bool
		wantErr         bool
		wantErrContains string
		// wantErrOmits is the direction/diagnosis guard: for the reversing
		// spelling it holds the operand order the message must NOT suggest, so a
		// refusal that merely names the target cannot pass by accident.
		wantErrOmits string
	}{
		{
			name:            "bare cross-prefix target is passed through as a cross-store edge",
			sourceID:        "be-abc",
			dependsOnArg:    "xy-999",
			wantToID:        "xy-999",
			wantUnverified:  true,
			wantErr:         false,
			wantErrContains: "",
			wantErrOmits:    "",
		},
		{
			name:            "type:id-shaped positional arg is rejected, not silently misparsed",
			sourceID:        "be-abc",
			dependsOnArg:    "discovered-from:ga-dguvnb",
			wantErr:         true,
			wantErrContains: "discovered-from:ga-dguvnb",
		},
		{
			name:            "type:id rejection names the flag form that would have worked",
			sourceID:        "be-abc",
			dependsOnArg:    "discovered-from:ga-dguvnb",
			wantErr:         true,
			wantErrContains: "bd dep add be-abc ga-dguvnb --type discovered-from",
		},
		{
			// `bd create --deps blocks:B` on A stores "B depends on A":
			// parseDepSpec sets SwapDirection for the literal "blocks" spelling
			// and both consumers swap the endpoints. The equivalent dep add
			// therefore names the target first. Suggesting the natural order
			// here would hand the user a reversed bd ready/bd blocked gate.
			name:            "blocks: is the reversing spelling, so the suggestion swaps the operands",
			sourceID:        "be-abc",
			dependsOnArg:    "blocks:xy-999",
			wantErr:         true,
			wantErrContains: "bd dep add xy-999 be-abc --type blocks",
			wantErrOmits:    "bd dep add be-abc xy-999",
		},
		{
			// The documented aliases are compared before canonicalDependencyType,
			// so they do not swap — the operand order stays natural. This is the
			// twin that stops the blocks: fix from swapping everything.
			name:            "depends-on: alias keeps direction, so the suggestion keeps the operand order",
			sourceID:        "be-abc",
			dependsOnArg:    "depends-on:xy-999",
			wantErr:         true,
			wantErrContains: "bd dep add be-abc xy-999 --type depends-on",
			wantErrOmits:    "bd dep add xy-999 be-abc",
		},
		{
			// parseDepSpec TrimSpaces both halves, so `bd create --deps
			// "blocks : xy-999"` is accepted there. Read verbatim here the type
			// would be "blocks " — neither DepBlocks nor well-known — and the
			// paste of that same spec into dep add would lose the translation
			// and fall to the generic refusal. wantErrOmits is what pins that:
			// the message must still be the actionable one, and still swapped.
			name:            "whitespace-bearing spec keeps the actionable diagnosis, matching parseDepSpec",
			sourceID:        "be-abc",
			dependsOnArg:    "blocks : xy-999",
			wantErr:         true,
			wantErrContains: "bd dep add xy-999 be-abc --type blocks",
			wantErrOmits:    "not a bd ID and not a well-formed external:",
		},
		{
			// The `--deps` diagnosis is a claim about the token before the ":".
			// An unrelated colon-bearing typo must not be told that cause and
			// handed a --type the next validation rejects.
			name:            "colon-bearing target whose prefix is not a dependency type gets the generic refusal",
			sourceID:        "be-abc",
			dependsOnArg:    "https://example.com/x",
			wantErr:         true,
			wantErrContains: "not a bd ID and not a well-formed external:",
			wantErrOmits:    "--type https",
		},
		{
			name:         "well-formed external ref is still accepted",
			sourceID:     "be-abc",
			dependsOnArg: "external:otherproj:some-capability",
			wantToID:     "external:otherproj:some-capability",
			wantErr:      false,
		},
		{
			name:            "malformed external-looking ref missing capability is rejected",
			sourceID:        "be-abc",
			dependsOnArg:    "external:otherproj",
			wantErr:         true,
			wantErrContains: "external:otherproj",
		},
		{
			name:            "same-prefix target that resolves nowhere is still an error",
			sourceID:        "be-abc",
			dependsOnArg:    "be-nosuchbead",
			wantErr:         true,
			wantErrContains: "be-nosuchbead",
		},
		{
			name:            "target with no prefix at all is rejected",
			sourceID:        "be-abc",
			dependsOnArg:    "nodashhere",
			wantErr:         true,
			wantErrContains: "nodashhere",
		},
		{
			name:         "empty target is rejected",
			sourceID:     "be-abc",
			dependsOnArg: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toID, unverified, err := resolveUnresolvedDepTarget(tt.sourceID, tt.dependsOnArg, resolveErr)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveUnresolvedDepTarget(%q, %q): got nil error, want error naming the unresolved target", tt.sourceID, tt.dependsOnArg)
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("resolveUnresolvedDepTarget(%q, %q): error %q does not contain %q", tt.sourceID, tt.dependsOnArg, err.Error(), tt.wantErrContains)
				}
				if tt.wantErrOmits != "" && strings.Contains(err.Error(), tt.wantErrOmits) {
					t.Errorf("resolveUnresolvedDepTarget(%q, %q): error %q must not contain %q", tt.sourceID, tt.dependsOnArg, err.Error(), tt.wantErrOmits)
				}
				if toID != "" {
					t.Errorf("resolveUnresolvedDepTarget(%q, %q): got toID %q on error, want empty", tt.sourceID, tt.dependsOnArg, toID)
				}
				if unverified {
					t.Errorf("resolveUnresolvedDepTarget(%q, %q): got unverified on a refusal; only the cross-prefix pass-through reports unverified", tt.sourceID, tt.dependsOnArg)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveUnresolvedDepTarget(%q, %q): unexpected error: %v", tt.sourceID, tt.dependsOnArg, err)
			}
			if unverified != tt.wantUnverified {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q): unverified = %v, want %v", tt.sourceID, tt.dependsOnArg, unverified, tt.wantUnverified)
			}
			if toID != tt.wantToID {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q) = %q, want %q", tt.sourceID, tt.dependsOnArg, toID, tt.wantToID)
			}
		})
	}
}

// TestResolveUnresolvedDepTargetMatchesStorageClassification pins the CLI's
// accept/refuse decision to the storage layer's own definition of an external
// dependency target. Every target dep add passes through here must be one
// IsExternalDepTarget classifies as external — i.e. one depends_on_external can
// actually hold. If these two ever disagree, dep add is writing edges a backend
// will route to the wrong column (or refusing edges it would have held fine),
// which is the failure this whole change is about.
func TestResolveUnresolvedDepTargetMatchesStorageClassification(t *testing.T) {
	resolveErr := errors.New("issue not found")

	const sourceID = "be-abc"
	// wantUnverified mirrors the helper's outcome-3 report for each accepted
	// shape: the bare cross-prefix target is stored unverified (the write-time
	// warning exists because of it, GH#6556), the well-formed external: ref is
	// not — it is resolved later by the external-deps resolver by design.
	accepted := []struct {
		target         string
		wantUnverified bool
	}{
		{target: "xy-999", wantUnverified: true},
		{target: "external:otherproj:some-capability", wantUnverified: false},
	}

	for _, tt := range accepted {
		t.Run(tt.target, func(t *testing.T) {
			toID, unverified, err := resolveUnresolvedDepTarget(sourceID, tt.target, resolveErr)
			if err != nil {
				t.Fatalf("resolveUnresolvedDepTarget(%q, %q): unexpected error: %v", sourceID, tt.target, err)
			}
			if unverified != tt.wantUnverified {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q): unverified = %v, want %v", sourceID, tt.target, unverified, tt.wantUnverified)
			}
			if !storeissueops.IsExternalDepTarget(sourceID, toID) {
				t.Errorf("dep add accepted %q but storage would not classify it as an external dep target; "+
					"the edge would be written to a column that cannot hold it", toID)
			}
		})
	}
}

// TestUnresolvedDepTargetWarning pins the write-time notice for the outcome-3
// edge (GH#6556): the first line says the target was not found and the edge
// stored unverified, the second is a paste-ready bd dep remove naming the
// stored row. Every value the user could have typed goes into it spelled the
// way the remedy helpers spell pasted words, so a hostile target can neither
// run when pasted nor redraw the command, and there is no backtick to trip a
// paste into markdown.
func TestUnresolvedDepTargetWarning(t *testing.T) {
	base := unresolvedDepTargetWarning("", "be-abc", "xy-999")
	if !strings.Contains(base, "xy-999 was not found") {
		t.Errorf("warning = %q, want the target named as not found", base)
	}
	if !strings.Contains(base, "stored unverified") {
		t.Errorf("warning = %q, want the unverified storage named", base)
	}
	if !strings.Contains(base, "bd dep remove be-abc xy-999") {
		t.Errorf("warning = %q, want the paste-ready bd dep remove be-abc xy-999", base)
	}
	if !strings.HasPrefix(base, "warning: dependency target") {
		t.Errorf("warning = %q, want it to start with the lowercase warning: prefix", base)
	}
	if strings.ContainsRune(base, '`') {
		t.Errorf("warning = %q, want no backtick anywhere in it", base)
	}
	if !strings.HasSuffix(base, "\n") {
		t.Errorf("warning = %q, want a trailing newline so the printed notice ends its line", base)
	}

	// The --file route prefixes the notice with the input line it came from.
	line2 := unresolvedDepTargetWarning("line 2: ", "be-abc", "xy-999")
	if !strings.HasPrefix(line2, "warning: line 2: ") {
		t.Errorf("--file warning = %q, want the line 2: prefix after warning:", line2)
	}

	// A hostile target — the user's argument verbatim — is spelled by the
	// remedy helpers: escaped in prose, a placeholder plus the escaped note in
	// the command, and no raw terminal control byte reaches the output.
	const hostile = "xy-9\x1b[2K"
	got := unresolvedDepTargetWarning("", "be-abc", hostile)
	if strings.ContainsRune(got, '\x1b') {
		t.Errorf("warning for %q = %q, want no raw ESC byte in it", hostile, got)
	}
	if !strings.Contains(got, "<target-id>") {
		t.Errorf("warning for %q = %q, want the <target-id> placeholder in the command", hostile, got)
	}
	if !strings.Contains(got, `\x1b[2K`) {
		t.Errorf("warning for %q = %q, want the escaped target named in the placeholder note", hostile, got)
	}
}

// newDepAddCommandForTest mirrors depAddCmd's flag set (dep.go's init) so the
// proxied RunE helpers can be driven directly. Only the flags read before the
// target decision matter, but registering all of them keeps the fixture honest
// if that order ever changes.
func newDepAddCommandForTest() *cobra.Command {
	cmd := &cobra.Command{Use: "add"}
	cmd.Flags().StringP("type", "t", "blocks", "")
	cmd.Flags().String("blocked-by", "", "")
	cmd.Flags().String("depends-on", "", "")
	cmd.Flags().String("file", "", "")
	cmd.Flags().Bool("no-cycle-check", false, "")
	return cmd
}

// TestDepAddProxiedServerRefusesDepsSpecTarget pins the refusal on the proxied
// route, where be-gmdx5 survived the original fix: usesProxiedServer() is
// consulted before any flag handling (dep.go), this path resolves no IDs, and
// nothing below it validates target shape — ExecuteAddDependencies classifies by
// ExtractPrefix alone (internal/storage/issueops/dependency_editor.go). Without
// the refusal the identical invocation that now errors in direct mode stores the
// bogus external ref with the type dropped, so `bd dep add` would be
// mode-dependently correct.
//
// The assertion is on the message, not merely on "an error came back": the
// unguarded path also fails here (it reaches proxiedDependencyEditor with no
// server configured), so only the refusal text distinguishes the two.
func TestDepAddProxiedServerRefusesDepsSpecTarget(t *testing.T) {
	var err error
	stderr := captureStderr(t, func() {
		err = runDepAddProxiedServer(newDepAddCommandForTest(), context.Background(),
			[]string{"be-abc", "discovered-from:ga-dguvnb"})
	})

	if err == nil {
		t.Fatal("proxied dep add accepted discovered-from:ga-dguvnb; the be-gmdx5 edge would be stored with its type dropped")
	}
	if !strings.Contains(stderr, "bd dep add be-abc ga-dguvnb --type discovered-from") {
		t.Errorf("proxied refusal = %q, want the same target refusal the direct route emits", stderr)
	}
}

// TestDepAddBulkProxiedRefusesDepsSpecTarget covers the fourth decision site.
// The bulk format carries an explicit "type" field, so a "type:id" value in
// "to" is precisely the malformed input — and it was accepted and stored.
func TestDepAddBulkProxiedRefusesDepsSpecTarget(t *testing.T) {
	file := filepath.Join(t.TempDir(), "edges.jsonl")
	if err := os.WriteFile(file, []byte(`{"from":"be-abc","to":"discovered-from:ga-dguvnb"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write bulk edge file: %v", err)
	}

	var err error
	stderr := captureStderr(t, func() {
		err = runDepAddBulkProxied(newDepAddCommandForTest(), context.Background(), file, "blocks")
	})

	if err == nil {
		t.Fatal("proxied bulk dep add accepted discovered-from:ga-dguvnb in \"to\"")
	}
	if !strings.Contains(stderr, "line 1:") ||
		!strings.Contains(stderr, "bd dep add be-abc ga-dguvnb --type discovered-from") {
		t.Errorf("proxied bulk refusal = %q, want the per-line target refusal", stderr)
	}
}

// bulkDepStubStore resolves exactly one issue ID and nothing else, which is
// all validateBulkDepEdges needs to reach its target decision: the source has
// to resolve (or the line fails earlier with "resolving issue ID"), and the
// target has to not resolve anywhere (or the decision point is skipped).
// Four methods carry that. SearchIssues is ResolvePartialID's fast path and
// GetIssue is resolveAndGetFromStore's follow-up; GetConfig and GetAllConfig
// are consulted by the prefix-routing and auto-routing fallbacks the target
// falls through on its way to failing. Nothing here opens a store, so this
// needs no cgo build tag and no container — the same reason the proxied pins
// above are cheap.
type bulkDepStubStore struct {
	storage.DoltStorage
	resolvableID string
}

func (s *bulkDepStubStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	for _, id := range filter.IDs {
		if id == s.resolvableID {
			return []*types.Issue{{ID: s.resolvableID}}, nil
		}
	}
	return nil, nil
}

// SearchIssueIDs is ResolvePartialID's narrow-projection twin of SearchIssues;
// resolving an unresolvable bare target (xy-999) can reach it, and leaving it
// to the nil embedded interface panics instead of answering "not found".
func (s *bulkDepStubStore) SearchIssueIDs(_ context.Context, _ string, _ types.IssueFilter) ([]string, error) {
	return nil, nil
}

func (s *bulkDepStubStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if id == s.resolvableID {
		return &types.Issue{ID: s.resolvableID}, nil
	}
	return nil, storage.ErrNotFound
}

func (s *bulkDepStubStore) GetConfig(_ context.Context, _ string) (string, error) {
	return "be", nil
}

func (s *bulkDepStubStore) GetAllConfig(_ context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// TestValidateBulkDepEdgesRefusesDepsSpecTarget is the call-site pin for the
// direct `bd dep add --file` route — the one converted site with the worst
// regression history, because it is where the be-gmdx5 shape survived the
// first fix by carrying its own private copy of the prefix ladder.
//
// TestResolveUnresolvedDepTarget pins the decision in isolation, but that is
// coverage by construction: it stays green if a future change re-inlines a
// prefix comparison at the call site, or short-circuits before reaching the
// helper at all. This asserts on the per-line text the route actually emits,
// so the wiring itself is what goes red.
func TestValidateBulkDepEdgesRefusesDepsSpecTarget(t *testing.T) {
	oldStore := store
	t.Cleanup(func() { store = oldStore })
	store = &bulkDepStubStore{resolvableID: "be-abc"}

	_, err := validateBulkDepEdges(context.Background(), []bulkDepEdge{
		{Line: 1, IssueID: "be-abc", DependsOnID: "discovered-from:ga-dguvnb", Type: types.DepBlocks},
	})

	if err == nil {
		t.Fatal("bulk dep add --file accepted discovered-from:ga-dguvnb; the be-gmdx5 edge would be stored with its type dropped")
	}
	if !strings.Contains(err.Error(), "line 1:") ||
		!strings.Contains(err.Error(), "bd dep add be-abc ga-dguvnb --type discovered-from") {
		t.Errorf("bulk validation error = %q, want the per-line target refusal", err.Error())
	}
}

// TestValidateBulkDepEdgesMarksUnverifiedTarget is the --file wiring pin for
// the unverified report: validateBulkDepEdges passes the unresolved
// cross-prefix target through resolveUnresolvedDepTarget (the be-gmdx5 call
// site), and addBulkDependencies warns about an edge only when that helper
// flagged it, so the flag must survive the round trip into bulkDepEdge.
// Without it the bulk route would re-introduce the silent accept the
// positional route just stopped having, mode-dependently.
func TestValidateBulkDepEdgesMarksUnverifiedTarget(t *testing.T) {
	oldStore := store
	t.Cleanup(func() { store = oldStore })
	store = &bulkDepStubStore{resolvableID: "be-abc"}

	resolved, err := validateBulkDepEdges(context.Background(), []bulkDepEdge{
		{Line: 1, IssueID: "be-abc", DependsOnID: "xy-999", Type: types.DepBlocks},
		{Line: 2, IssueID: "be-abc", DependsOnID: "external:proj:cap", Type: types.DepBlocks},
	})

	if err != nil {
		t.Fatalf("validateBulkDepEdges: unexpected error: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved = %d edges, want 2", len(resolved))
	}
	if !resolved[0].Unverified {
		t.Errorf("unresolvable cross-prefix target xy-999: Unverified = false, want true — the bulk route's warning hangs off this flag")
	}
	if resolved[0].DependsOnID != "xy-999" {
		t.Errorf("resolved[0].DependsOnID = %q, want the target stored verbatim", resolved[0].DependsOnID)
	}
	if resolved[1].Unverified {
		t.Errorf("well-formed external ref external:proj:cap: Unverified = true, want false — it resolves later by design, not an unverified stored edge")
	}
}

// TestWarnUnresolvedDepTargetHonorsQuiet pins the --quiet gate of the
// write-time notice: --quiet is documented as "Suppress non-essential output
// (errors only)", and this advisory notice is exactly that — an edge the
// operator can still see with bd show, whose message is noise for a script
// that asked for errors only. It saves and restores the package-global
// quietFlag, so it must not run in parallel with anything else reading it.
func TestWarnUnresolvedDepTargetHonorsQuiet(t *testing.T) {
	oldQuiet := quietFlag
	t.Cleanup(func() { quietFlag = oldQuiet })

	quietFlag = true
	stderr := captureStderr(t, func() { warnUnresolvedDepTarget("", "be-abc", "xy-999") })
	if stderr != "" {
		t.Errorf("quietFlag = true: stderr = %q, want the notice suppressed", stderr)
	}

	quietFlag = false
	stderr = captureStderr(t, func() { warnUnresolvedDepTarget("", "be-abc", "xy-999") })
	if want := unresolvedDepTargetWarning("", "be-abc", "xy-999"); stderr != want {
		t.Errorf("quietFlag = false: stderr = %q, want exactly the notice:\n%q", stderr, want)
	}
}

// TestDepBlocksProxiedServerRefusesDepsSpecTarget covers the fifth decision
// site. `bd dep <blocker> --blocks <blocked>` is dispatched before any dep add
// helper runs, and `bd dep --help` documents it as "equivalent to: bd dep add
// <blocked-id> <blocker-id>" — so leaving it unguarded kept exactly the
// be-gmdx5 mode-dependence this change exists to remove: the direct twin
// hard-errors on the resolve, while the proxied twin built the edge straight
// from the raw args and stored the bogus external ref with its type forced to
// blocks.
//
// The operands are inverted here relative to dep add: the blocker is the
// target (it becomes DependsOnID) and the blocked issue is the source, which
// is why the suggested command still reads `bd dep add <blocked> <blocker>`.
// As with the two pins above, the assertion is on the message — the unguarded
// path errors too, just from the missing proxied UOW provider.
func TestDepBlocksProxiedServerRefusesDepsSpecTarget(t *testing.T) {
	var err error
	stderr := captureStderr(t, func() {
		err = runDepBlocksProxiedServer(newDepAddCommandForTest(), context.Background(),
			"discovered-from:ga-dguvnb", "be-abc")
	})

	if err == nil {
		t.Fatal("proxied dep --blocks accepted discovered-from:ga-dguvnb as the blocker; the be-gmdx5 edge would be stored")
	}
	if !strings.Contains(stderr, "bd dep add be-abc ga-dguvnb --type discovered-from") {
		t.Errorf("proxied --blocks refusal = %q, want the same target refusal the dep add surfaces emit", stderr)
	}
}
