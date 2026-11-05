package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
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
		// lookupErr is the resolveIDWithRouting error the row passes in;
		// nil means the shared resolveErr.
		lookupErr error
		wantToID  string
		// wantUnverified is the cross-prefix pass-through signal the caller
		// turns into the post-write stderr warning (GH#6556): a non-nil miss,
		// which must be the lookup error itself, unwrapped, so the warning can
		// tell a missing target from a lookup that failed. Only outcome 3 sets
		// it: an external: ref is resolved later by the external-deps
		// resolver, and a refusal stores nothing.
		wantUnverified  bool
		wantErr         bool
		wantErrContains string
		// wantErrOmits is the direction/diagnosis guard: for the reversing
		// spelling it holds the operand order the message must NOT suggest, so a
		// refusal that merely names the target cannot pass by accident.
		wantErrOmits string
	}{
		{
			name:           "bare cross-prefix target is passed through as a cross-store edge",
			sourceID:       "be-abc",
			dependsOnArg:   "xy-999",
			wantToID:       "xy-999",
			wantUnverified: true,
			wantErr:        false,
		},
		{
			// The accept on any resolution error predates the warning; what
			// the row pins is that the routing failure reaches the warning as
			// itself, not as the shared not-found.
			name:           "bare cross-prefix target whose route failed is passed through with that failure",
			sourceID:       "be-abc",
			dependsOnArg:   "xy-999",
			lookupErr:      errors.New(`resolving issue ID xy-999: prefix route "xy-" to rig: target rig has no dolt_database configured`),
			wantToID:       "xy-999",
			wantUnverified: true,
			wantErr:        false,
		},
		{
			// A route into a rig in proxied-server mode ends in the store
			// factory's refusal, wrapped as resolveIDWithRouting wraps it. bd
			// could not look the target up (#5861), so it leaves no miss: a
			// notice here would offer a bd dep remove for an edge that may be
			// valid.
			name:         "bare cross-prefix target routed to a proxied-server rig is passed through with no miss",
			sourceID:     "be-abc",
			dependsOnArg: "xy-999",
			lookupErr:    fmt.Errorf("resolving issue ID xy-999: opening routed store for rig: %w", errProxiedStoreUnrouted()),
			wantToID:     "xy-999",
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
			lookupErr := tt.lookupErr
			if lookupErr == nil {
				lookupErr = resolveErr
			}
			toID, miss, err := resolveUnresolvedDepTarget(tt.sourceID, tt.dependsOnArg, lookupErr)

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
				if miss != nil {
					t.Errorf("resolveUnresolvedDepTarget(%q, %q): reported unverified (miss %v) on a refusal, which stores nothing", tt.sourceID, tt.dependsOnArg, miss)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveUnresolvedDepTarget(%q, %q): unexpected error: %v", tt.sourceID, tt.dependsOnArg, err)
			}
			if toID != tt.wantToID {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q) = %q, want %q", tt.sourceID, tt.dependsOnArg, toID, tt.wantToID)
			}
			if (miss != nil) != tt.wantUnverified {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q): miss = %v, want unverified %v", tt.sourceID, tt.dependsOnArg, miss, tt.wantUnverified)
			}
			if tt.wantUnverified && miss != lookupErr {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q): miss = %v, want the lookup error %v itself", tt.sourceID, tt.dependsOnArg, miss, lookupErr)
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
	// unverified is true only for the bare cross-prefix target: both land in
	// depends_on_external, but only the external: ref has a resolver behind it.
	accepted := []struct {
		target     string
		unverified bool
	}{
		{"xy-999", true},
		{"external:otherproj:some-capability", false},
	}

	for _, tc := range accepted {
		target := tc.target
		t.Run(target, func(t *testing.T) {
			toID, miss, err := resolveUnresolvedDepTarget(sourceID, target, resolveErr)
			if err != nil {
				t.Fatalf("resolveUnresolvedDepTarget(%q, %q): unexpected error: %v", sourceID, target, err)
			}
			if (miss != nil) != tc.unverified {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q): miss = %v, want unverified %v", sourceID, target, miss, tc.unverified)
			}
			if !storeissueops.IsExternalDepTarget(sourceID, toID) {
				t.Errorf("dep add accepted %q but storage would not classify it as an external dep target; "+
					"the edge would be written to a column that cannot hold it", toID)
			}
		})
	}
}

// depTargetNotFound is a plain miss as the direct route reports one:
// resolveIDWithRouting wrapping utils.ResolvePartialID's not-found.
var depTargetNotFound = fmt.Errorf("resolving issue ID xy-999: %w", fmt.Errorf("no issue found matching %q", "xy-999"))

// TestUnresolvedDepTargetWarning pins the write-time notice dep add prints
// after storing an edge whose bare cross-prefix target nothing found
// (GH#6556), so the operator learns it was stored unverified and gets a
// paste-ready command that deletes exactly the stored row. A lookup that
// missed says the target was not found; one that failed is named instead,
// because calling that a miss would blame a typo for a broken route. The
// target is user-typed, so it goes through the remedy printers like any other
// value a hint pastes into a command.
func TestUnresolvedDepTargetWarning(t *testing.T) {
	t.Run("names the target and the remove command", func(t *testing.T) {
		msg := unresolvedDepTargetWarning("", "be-abc", "xy-999", depTargetNotFound)
		if !strings.HasPrefix(msg, "warning: ") {
			t.Errorf("warning = %q, want the lowercase warning: prefix", msg)
		}
		for _, want := range []string{"xy-999 was not found", "stored unverified", "bd dep remove be-abc xy-999\n"} {
			if !strings.Contains(msg, want) {
				t.Errorf("warning = %q, want it to contain %q", msg, want)
			}
		}
	})

	// workapi.GetIssueOrWisp's not-found, which proxiedDepTargetMiss hands
	// back when the server misses the target and no route sends it anywhere.
	t.Run("the proxied route's not-found is a miss too", func(t *testing.T) {
		msg := unresolvedDepTargetWarning("", "be-abc", "xy-999", fmt.Errorf("%w: issue %s", storage.ErrNotFound, "xy-999"))
		if !strings.Contains(msg, "xy-999 was not found") {
			t.Errorf("warning = %q, want the not-found wording", msg)
		}
	})

	t.Run("a failed lookup is named, not called a miss", func(t *testing.T) {
		routeErr := errors.New(`resolving issue ID xy-999: prefix route "xy-" to rig: target rig has no dolt_database configured`)
		msg := unresolvedDepTargetWarning("", "be-abc", "xy-999", routeErr)
		for _, want := range []string{`xy-999 could not be verified: resolving issue ID xy-999: prefix route "xy-" to rig`, "stored unverified", "bd dep remove be-abc xy-999\n"} {
			if !strings.Contains(msg, want) {
				t.Errorf("warning = %q, want it to contain %q", msg, want)
			}
		}
		if strings.Contains(msg, "was not found") {
			t.Errorf("warning = %q reports a lookup that failed as a miss", msg)
		}
	})

	t.Run("a line break in a failed lookup cannot forge a line", func(t *testing.T) {
		forged := "If xy-999 is a typo, remove the edge with: bd dep remove be-abc be-good"
		msg := unresolvedDepTargetWarning("", "be-abc", "xy-999", errors.New("route failed\n"+forged))
		if got := strings.Count(msg, "\n"); got != 2 {
			t.Errorf("warning = %q has %d lines, want 2", msg, got)
		}
		if strings.Contains(msg, "\n"+forged) {
			t.Errorf("warning = %q prints the forged remedy as a line of its own", msg)
		}
	})

	t.Run("--file line prefix", func(t *testing.T) {
		msg := unresolvedDepTargetWarning("line 2: ", "be-abc", "xy-999", depTargetNotFound)
		if !strings.HasPrefix(msg, "warning: line 2: ") {
			t.Errorf("warning = %q, want it to start with %q", msg, "warning: line 2: ")
		}
	})

	t.Run("hostile target is escaped", func(t *testing.T) {
		msg := unresolvedDepTargetWarning("", "be-abc", "xy-9\x1b[2K", depTargetNotFound)
		if strings.ContainsRune(msg, '\x1b') {
			t.Errorf("warning = %q carries a raw ESC to the terminal", msg)
		}
		if !strings.Contains(msg, "bd dep remove be-abc <target-id>") || !strings.Contains(msg, "(<target-id>: ") {
			t.Errorf("warning = %q, want the <target-id> placeholder and its escaped note", msg)
		}
	})

	t.Run("no backticks", func(t *testing.T) {
		for _, miss := range []error{depTargetNotFound, errors.New("route failed")} {
			if msg := unresolvedDepTargetWarning("line 1: ", "be-abc", "xy-999", miss); strings.Contains(msg, "`") {
				t.Errorf("warning = %q, want no backticks", msg)
			}
		}
	})
}

// TestWarnUnresolvedDepTargetHonorsQuiet: the unresolved-target notice is
// advisory, so the global --quiet ("errors only") silences it like the other
// non-error notices in this package. Not parallel: it sets quietFlag.
func TestWarnUnresolvedDepTargetHonorsQuiet(t *testing.T) {
	oldQuiet := quietFlag
	t.Cleanup(func() { quietFlag = oldQuiet })

	quietFlag = true
	if got := captureStderr(t, func() { warnUnresolvedDepTarget("", "be-abc", "xy-999", depTargetNotFound) }); got != "" {
		t.Errorf("--quiet: stderr = %q, want empty", got)
	}

	quietFlag = false
	want := unresolvedDepTargetWarning("", "be-abc", "xy-999", depTargetNotFound)
	if got := captureStderr(t, func() { warnUnresolvedDepTarget("", "be-abc", "xy-999", depTargetNotFound) }); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
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
// Five methods carry that. SearchIssues is ResolvePartialID's fast path and
// SearchIssueIDs its substring fallback (reached by a hash-shaped target such
// as xy-999); GetIssue is resolveAndGetFromStore's follow-up; GetConfig and
// GetAllConfig are consulted by the prefix-routing and auto-routing fallbacks
// the target falls through on its way to failing. Nothing here opens a store, so this
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
// the GH#6556 warning: validateBulkDepEdges must carry the helper's unverified
// signal onto the edge, or addBulkDependencies has nothing to warn about. A
// well-formed external: ref is not unverified (the external-deps resolver
// owns it).
func TestValidateBulkDepEdgesMarksUnverifiedTarget(t *testing.T) {
	oldStore := store
	t.Cleanup(func() { store = oldStore })
	store = &bulkDepStubStore{resolvableID: "be-abc"}
	withBeadGateTown(t) // no routes.jsonl: the local miss is the whole answer

	resolved, err := validateBulkDepEdges(context.Background(), []bulkDepEdge{
		{Line: 1, IssueID: "be-abc", DependsOnID: "xy-999", Type: types.DepBlocks},
		{Line: 2, IssueID: "be-abc", DependsOnID: "external:proj:cap", Type: types.DepBlocks},
	})
	if err != nil {
		t.Fatalf("validateBulkDepEdges: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		for _, edge := range resolved {
			for _, cleanup := range edge.Cleanups {
				cleanup()
			}
		}
	})
	if len(resolved) != 2 {
		t.Fatalf("validateBulkDepEdges returned %d edges, want 2", len(resolved))
	}
	// The miss is the real resolution's, so it must read as a not-found:
	// anything else would turn this plain typo's notice into "could not be
	// verified".
	if miss := resolved[0].TargetMiss; miss == nil || !isNotFoundErr(miss) {
		t.Errorf("line 1 (be-abc -> xy-999): TargetMiss = %v, want the lookup's not-found", miss)
	}
	if miss := resolved[1].TargetMiss; miss != nil {
		t.Errorf("line 2 (be-abc -> external:proj:cap): TargetMiss = %v, want nil", miss)
	}
}

// TestAddBulkDependenciesRefusedBatchWarnsNothing pins the half of the GH#6556
// contract that only the order inside addBulkDependencies keeps: the notice
// says the edge was stored, so it may print only once the all-or-nothing batch
// is. Line 1 is accepted unverified before line 2, a same-prefix target that
// resolves nowhere, refuses the batch; a notice printed when line 1 was
// decided would describe an edge that was never written, and hand the user a
// remove command for it. The failed-batch embedded case fails while reading
// the file, before any target is decided, so it cannot catch that.
func TestAddBulkDependenciesRefusedBatchWarnsNothing(t *testing.T) {
	oldStore, oldCtx := store, rootCtx
	t.Cleanup(func() { store, rootCtx = oldStore, oldCtx })
	store = &bulkDepStubStore{resolvableID: "be-abc"}
	rootCtx = context.Background()
	withBeadGateTown(t)

	file := filepath.Join(t.TempDir(), "edges.jsonl")
	body := `{"from":"be-abc","to":"xy-999"}` + "\n" + `{"from":"be-abc","to":"be-nope"}` + "\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write bulk edge file: %v", err)
	}

	var err error
	stderr := captureStderr(t, func() {
		err = addBulkDependencies(newDepAddCommandForTest(), file, "blocks")
	})
	if err == nil || !strings.Contains(err.Error(), "line 2:") {
		t.Fatalf("addBulkDependencies error = %v, want the batch refused at line 2", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing: the batch stored no edge, so none was stored unverified", stderr)
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

// proxiedDepAddIssues is the server's issue table for the proxied dep add
// pins. GetIssue finds the IDs in present and misses the rest the way the
// domain seam does (a wrapped sql.ErrNoRows, as stubLookupIssueUC explains),
// or fails every lookup with hardErr; GetWisp finds nothing.
type proxiedDepAddIssues struct {
	domain.IssueUseCase // any other method is a nil call
	present             map[string]bool
	hardErr             error
}

func (s proxiedDepAddIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if s.hardErr != nil {
		return nil, s.hardErr
	}
	if s.present[id] {
		return &types.Issue{ID: id, Title: "Present " + id}, nil
	}
	return nil, fmt.Errorf("get %s: %w", id, sql.ErrNoRows)
}

func (s proxiedDepAddIssues) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	if s.hardErr != nil {
		return nil, s.hardErr
	}
	return nil, fmt.Errorf("get %s: %w", id, sql.ErrNoRows)
}

// proxiedDepAddServer is the proxied server on the dep add writers' success
// path: its editor stores every edge it is handed, as a server classifying a
// cross-prefix target under depends_on_external does without looking it up,
// and each unit of work answers the post-write lookups from the issue table
// (lookupOnlyUOW).
type proxiedDepAddServer struct {
	lookupOnlyProvider
	stored []issueops.DependencyEdge
}

func (p *proxiedDepAddServer) DependencyEditor() (issueops.DependencyEditor, error) {
	return proxiedDepAddEditor{server: p}, nil
}

type proxiedDepAddEditor struct {
	issueops.DependencyEditor // any other method is a nil call
	server                    *proxiedDepAddServer
}

func (e proxiedDepAddEditor) AddDependencies(_ context.Context, request issueops.AddDependenciesRequest) (issueops.AddDependenciesResult, error) {
	e.server.stored = append(e.server.stored, request.Edges...)
	return issueops.AddDependenciesResult{Added: request.Edges}, nil
}

// withProxiedDepAddServer points the proxied writers at a proxiedDepAddServer
// holding issues, and dbPath at an empty town (withBeadGateTown), so no route
// sends a missed target anywhere until the test writes the routes.jsonl in the
// .beads directory it returns. Not parallel: it sets package globals.
func withProxiedDepAddServer(t *testing.T, issues proxiedDepAddIssues) (*proxiedDepAddServer, string) {
	t.Helper()
	server := &proxiedDepAddServer{lookupOnlyProvider: lookupOnlyProvider{issues: issues}}
	oldProvider, oldJSON, oldQuiet := uowProvider, jsonOutput, quietFlag
	uowProvider, jsonOutput, quietFlag = server, false, false
	t.Cleanup(func() { uowProvider, jsonOutput, quietFlag = oldProvider, oldJSON, oldQuiet })
	return server, withBeadGateTown(t)
}

// proxiedDepAddCmd is newDepAddCommandForTest with the post-write cycle sweep
// off: the stub server offers no cycle role, and the sweep is not what these
// pins are about.
func proxiedDepAddCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := newDepAddCommandForTest()
	if err := cmd.Flags().Set("no-cycle-check", "true"); err != nil {
		t.Fatalf("set --no-cycle-check: %v", err)
	}
	return cmd
}

// TestDepAddProxiedServerWarnsOnUnresolvedTarget is the proxied route's pin
// for GH#6556. The proxied writers resolve no IDs, so the server stored a bare
// cross-prefix typo with a success line and no notice while the direct route
// warned about the same input. The notice has to come from a lookup after the
// write and not from the prefix alone: a cross-prefix target the server holds
// is not a typo, and a lookup that failed must be named rather than called a
// miss.
func TestDepAddProxiedServerWarnsOnUnresolvedTarget(t *testing.T) {
	for _, tt := range []struct {
		name   string
		issues proxiedDepAddIssues
		target string
		quiet  bool
		routes string // routes.jsonl in the town; "" writes none
		want   []string
		// omit must not appear in stderr; with no want, the notice must not
		// appear at all.
		omit string
	}{
		{
			name:   "missing cross-prefix target warns once it is stored",
			target: "xy-999",
			want: []string{
				"warning: dependency target xy-999 was not found in this database or through routing; the edge was stored unverified",
				"If xy-999 is a typo, remove the edge with: bd dep remove be-abc xy-999\n",
			},
		},
		{
			name:   "cross-prefix target the server holds does not warn",
			issues: proxiedDepAddIssues{present: map[string]bool{"xy-999": true}},
			target: "xy-999",
		},
		{
			// The store's foreign key holds a same-prefix target, so the write
			// itself found it; the editor stub does not check, and the notice
			// must not either.
			name:   "same-prefix target does not warn",
			target: "be-def",
		},
		{
			name:   "external ref does not warn",
			target: "external:proj:cap",
		},
		{
			name:   "--quiet silences the notice and not the write",
			target: "xy-999",
			quiet:  true,
		},
		{
			name:   "a server lookup that fails is named, not called a miss",
			issues: proxiedDepAddIssues{hardErr: errors.New(stubBackendError)},
			target: "xy-999",
			want:   []string{"dependency target xy-999 could not be verified: " + stubBackendError, "bd dep remove be-abc xy-999\n"},
			omit:   "was not found",
		},
		{
			// The route matches, but its rig has no metadata.json, so the
			// server's not-found is not the answer: the target may live there.
			name:   "a route that cannot be followed is named, not called a miss",
			routes: `{"prefix":"xy-","path":"rig"}`,
			target: "xy-999",
			want:   []string{`dependency target xy-999 could not be verified: prefix route "xy-" to rig`, "bd dep remove be-abc xy-999\n"},
			omit:   "was not found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, town := withProxiedDepAddServer(t, tt.issues)
			quietFlag = tt.quiet
			if tt.routes != "" {
				if err := os.WriteFile(filepath.Join(town, "routes.jsonl"), []byte(tt.routes), 0o600); err != nil {
					t.Fatalf("write routes.jsonl: %v", err)
				}
			}

			var err error
			stderr := captureStderr(t, func() {
				err = runDepAddProxiedServer(proxiedDepAddCmd(t), context.Background(), []string{"be-abc", tt.target})
			})
			if err != nil {
				t.Fatalf("runDepAddProxiedServer: %v\nstderr:\n%s", err, stderr)
			}
			if len(server.stored) != 1 || server.stored[0].IssueID != "be-abc" || server.stored[0].DependsOnID != tt.target {
				t.Fatalf("stored edges = %+v, want the one edge be-abc -> %s", server.stored, tt.target)
			}
			if len(tt.want) == 0 && strings.Contains(stderr, "stored unverified") {
				t.Errorf("stderr = %q, want no unresolved-target notice", stderr)
			}
			for _, want := range tt.want {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, want)
				}
			}
			if tt.omit != "" && strings.Contains(stderr, tt.omit) {
				t.Errorf("stderr = %q, want it not to contain %q", stderr, tt.omit)
			}
		})
	}
}

// TestDepAddBulkProxiedWarnsPerUnresolvedLine is the proxied --file twin: one
// notice per stored line whose target nothing found, named by its line as the
// direct --file route names it, and none for a line whose target is
// same-prefix or held by the server.
func TestDepAddBulkProxiedWarnsPerUnresolvedLine(t *testing.T) {
	server, _ := withProxiedDepAddServer(t, proxiedDepAddIssues{present: map[string]bool{"xy-111": true}})
	file := filepath.Join(t.TempDir(), "edges.jsonl")
	body := `{"from":"be-abc","to":"be-def"}` + "\n" +
		`{"from":"be-abc","to":"xy-999"}` + "\n" +
		`{"from":"be-abc","to":"xy-111"}` + "\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write bulk edge file: %v", err)
	}

	var err error
	stderr := captureStderr(t, func() {
		err = runDepAddBulkProxied(proxiedDepAddCmd(t), context.Background(), file, "blocks")
	})
	if err != nil {
		t.Fatalf("runDepAddBulkProxied: %v\nstderr:\n%s", err, stderr)
	}
	if len(server.stored) != 3 {
		t.Fatalf("stored edges = %+v, want all three lines", server.stored)
	}
	for _, want := range []string{"warning: line 2: dependency target xy-999 was not found", "bd dep remove be-abc xy-999\n"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
	for _, line := range []string{"line 1:", "line 3:"} {
		if strings.Contains(stderr, line) {
			t.Errorf("stderr = %q, want no notice for %s", stderr, line)
		}
	}
}

// TestDepBlocksProxiedServerWarnsOnUnresolvedBlocker: the --blocks alias
// stores the blocker as the target, so the notice names the blocker, and its
// remove command takes the blocked issue first, in the stored row's order.
func TestDepBlocksProxiedServerWarnsOnUnresolvedBlocker(t *testing.T) {
	server, _ := withProxiedDepAddServer(t, proxiedDepAddIssues{})

	var err error
	stderr := captureStderr(t, func() {
		err = runDepBlocksProxiedServer(proxiedDepAddCmd(t), context.Background(), "xy-999", "be-abc")
	})
	if err != nil {
		t.Fatalf("runDepBlocksProxiedServer: %v\nstderr:\n%s", err, stderr)
	}
	if len(server.stored) != 1 || server.stored[0].IssueID != "be-abc" || server.stored[0].DependsOnID != "xy-999" {
		t.Fatalf("stored edges = %+v, want the one edge be-abc -> xy-999", server.stored)
	}
	for _, want := range []string{"dependency target xy-999 was not found", "If xy-999 is a typo, remove the edge with: bd dep remove be-abc xy-999\n"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}
