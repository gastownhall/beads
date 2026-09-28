package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// assertCloseFailedErrorIsTyped checks one failed[].error entry against the
// route-parity contract both close routes owe a --json consumer: the field
// carries the typed error, while the line a person reads keeps the --force hint.
//
// The two integration suites share it, which is the point — a contract that is
// supposed to be identical per route should be asserted by identical code. It is
// written against the RELATIONSHIP between the two spellings rather than a
// transcribed sentence so neither suite has to hardcode how the engine renders a
// blocker list: closeDirectRefusal and closeProxiedRefusal both produce exactly
// the typed error plus the hint, so the typed error must appear on stderr with
// the hint appended and must not contain the hint itself.
//
// Lives in an untagged file so it is compiled in the CGO_ENABLED=0 lane too,
// alongside the pins below; its callers are the cgo-gated suites.
func assertCloseFailedErrorIsTyped(t *testing.T, failedError, stderr string) {
	t.Helper()
	const hint = " (use --force to override)"
	if !strings.HasPrefix(failedError, "cannot close blocked issue:") {
		t.Errorf("failed[].error = %q, want the engine's typed ErrCloseBlocked vocabulary: both routes must spell one refusal one way",
			failedError)
	}
	if strings.Contains(failedError, hint) {
		t.Errorf("failed[].error = %q carries the --force hint; that decoration is advice for a human reader and belongs on the stderr line only",
			failedError)
	}
	if want := failedError + hint; !strings.Contains(stderr, want) {
		t.Errorf("stderr is missing the decorated refusal %q; the hint must still reach the person reading it\nstderr:\n%s",
			want, stderr)
	}
}

// The route-parity contract for the machine-readable half of the partial-failure
// report: `failed[].error` carries the TYPED error on both routes, and the
// decorated display line stays on stderr where a human reads it.
//
// The direct route already splits the two (close.go records res.Err.Error() in
// closeIDFailure while printing closeDirectRefusal's decorated line). The proxied
// route used to copy pre.errors — the display line — into the JSON field, so the
// same blocked-id refusal answered "cannot close blocked issue: X is blocked by
// [Y]" embedded and "... (use --force to override)" proxied. These live here
// rather than in the proxied integration suite because nothing in the split needs
// a server: the refusal slots are filled by closeProxiedOutcomes and read by
// closeProxiedFailures, both pure.
func TestCloseProxiedFailuresRecordTheTypedErrorNotTheDisplayLine(t *testing.T) {
	blockedErr := fmt.Errorf("%w: pb-2 is blocked by [pb-1]", storage.ErrCloseBlocked)

	// arg 0 closes, arg 1 is refused by the engine.
	args := []string{"pb-3", "pb-2"}
	pre := closeProxiedPreflight{
		items: []issueops.BatchCloseItem{
			{IssueID: "pb-3", Reason: "done"},
			{IssueID: "pb-2", Reason: "done"},
		},
		itemArgs:      []int{0, 1},
		before:        map[string]*types.Issue{"pb-3": {ID: "pb-3"}},
		errors:        make([]string, len(args)),
		failureErrors: make([]string, len(args)),
	}
	outcomes, _ := closeProxiedOutcomes(&pre, issueops.CloseBatchResult{
		Outcomes: []issueops.CloseOutcome{
			{IssueID: "pb-3", Issue: &types.Issue{ID: "pb-3"}, Changed: true},
			{IssueID: "pb-2", Err: blockedErr},
		},
	})
	if len(outcomes) != 1 || outcomes[0].id != "pb-3" {
		t.Fatalf("outcomes = %+v, want only the survivor pb-3", outcomes)
	}

	failures := closeProxiedFailures(&pre, args)
	if len(failures) != 1 || failures[0].ID != "pb-2" {
		t.Fatalf("failures = %+v, want exactly the refused id pb-2", failures)
	}

	// The machine field is the typed error, byte for byte.
	if failures[0].Error != blockedErr.Error() {
		t.Errorf("failed[].error = %q, want the typed error %q: a --json consumer keying off this field must see the same string on both routes",
			failures[0].Error, blockedErr.Error())
	}
	if strings.Contains(failures[0].Error, "use --force to override") {
		t.Errorf("failed[].error = %q, must not carry the --force hint: that is advice for a human reader, and the direct route records res.Err.Error() here",
			failures[0].Error)
	}

	// ...and the human line keeps the decoration. Without this half the fix
	// could have passed by stripping the hint from stderr too, which is a
	// different regression.
	if want := closeProxiedRefusal("pb-2", blockedErr); pre.errors[1] != want {
		t.Errorf("stderr line = %q, want the decorated refusal %q", pre.errors[1], want)
	}
	if !strings.Contains(pre.errors[1], "use --force to override") {
		t.Errorf("stderr line = %q, want it to keep the --force hint for the person reading it", pre.errors[1])
	}
}

// Every engine refusal class closeProxiedRefusal decorates, checked against the
// typed error it decorates. ErrCloseOpenChildren is in the table on purpose: it
// passes through undecorated, so it is the row that would still pass if the fix
// had been written as "special-case the blocked hint" instead of "record the
// typed error".
func TestCloseProxiedFailuresTypedErrorPerRefusalClass(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "blocked gains the force hint on stderr",
			err:  fmt.Errorf("%w: rc-1 is blocked by [rc-9]", storage.ErrCloseBlocked),
		},
		{
			name: "not found is rewritten on stderr",
			err:  fmt.Errorf("%w: rc-1", storage.ErrNotFound),
		},
		{
			name: "default class is wrapped on stderr",
			err:  fmt.Errorf("dolt: connection reset"),
		},
		{
			name: "open children passes through undecorated",
			err:  &storage.CloseOpenChildrenError{IssueID: "rc-1", OpenChildren: 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"rc-1"}
			pre := closeProxiedPreflight{
				items:         []issueops.BatchCloseItem{{IssueID: "rc-1", Reason: "done"}},
				itemArgs:      []int{0},
				before:        map[string]*types.Issue{},
				errors:        make([]string, len(args)),
				failureErrors: make([]string, len(args)),
			}
			closeProxiedOutcomes(&pre, issueops.CloseBatchResult{
				Outcomes: []issueops.CloseOutcome{{IssueID: "rc-1", Err: tc.err}},
			})

			failures := closeProxiedFailures(&pre, args)
			if len(failures) != 1 {
				t.Fatalf("failures = %+v, want one", failures)
			}
			if failures[0].Error != tc.err.Error() {
				t.Errorf("failed[].error = %q, want the typed error %q", failures[0].Error, tc.err.Error())
			}
			if pre.errors[0] != closeProxiedRefusal("rc-1", tc.err) {
				t.Errorf("stderr line = %q, want closeProxiedRefusal's spelling %q", pre.errors[0], closeProxiedRefusal("rc-1", tc.err))
			}
		})
	}
}

// The second half of the route-parity contract, and the one the decoration fix
// alone does not deliver: a refusal that reached the proxied route arrives
// wrapped by the layers it travelled through, so even the undecorated typed
// error read differently per route until closeProxiedTypedRefusal unwrapped it.
//
// The wrapper strings here are transcribed from the two production sites named
// in that function's comment, and the "want" column is the byte-identical
// string the direct route records for the same refusal.
func TestCloseProxiedTypedRefusalUnwrapsTheRoleWrappers(t *testing.T) {
	blocked := fmt.Errorf("%w: wr-1 is blocked by [wr-2]", storage.ErrCloseBlocked)
	openChildren := &storage.CloseOpenChildrenError{IssueID: "wr-1", OpenChildren: 3}
	unexpected := fmt.Errorf("dolt: connection reset")

	// What the role actually hands back: db/issue.go wraps, then issue.go wraps.
	roleWrapped := func(inner error) error {
		return fmt.Errorf("close wr-1: %w", fmt.Errorf("db: IssueSQLRepository.CloseChecked wr-1: %w", inner))
	}

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "blocked, unwrapped",
			err:  blocked,
			want: blocked.Error(),
		},
		{
			name: "blocked, wrapped by the role",
			err:  roleWrapped(blocked),
			want: blocked.Error(),
		},
		{
			name: "open children, wrapped by the role",
			err:  roleWrapped(openChildren),
			want: openChildren.Error(),
		},
		{
			// No sentinel to converge on, and the chain is the only thing that
			// says where the failure came from, so it is kept whole.
			name: "unexpected error keeps its whole chain",
			err:  roleWrapped(unexpected),
			want: "close wr-1: db: IssueSQLRepository.CloseChecked wr-1: dolt: connection reset",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := closeProxiedTypedRefusal(tc.err); got != tc.want {
				t.Errorf("closeProxiedTypedRefusal = %q, want %q", got, tc.want)
			}
		})
	}

	// The walk must stop AT the refusal, not at the bare sentinel underneath
	// it: unwrapping one link too far would drop the subject and leave every
	// blocked id reporting the same reasonless sentence.
	if got := closeProxiedTypedRefusal(roleWrapped(blocked)); got == storage.ErrCloseBlocked.Error() {
		t.Errorf("closeProxiedTypedRefusal = %q, which is the bare sentinel: the id and its blockers were unwrapped away", got)
	}
}

// A refusal decided by the CLI's own close policy has no separate display form,
// so both fields hold the one sentence closeProxiedCheckOne returned — and the
// failures still come back in the order the caller typed the ids, across a mix
// of policy and engine refusals. errors is what decides WHICH arguments failed,
// so a policy slot with an empty failureErrors twin would be a silent
// reason-less entry; this pins that it is not.
func TestCloseProxiedFailuresKeepPolicyRefusalsAndTypedOrder(t *testing.T) {
	policyRefusal := "cannot close mx-1: gate not satisfied"
	engineErr := fmt.Errorf("%w: mx-3 is blocked by [mx-9]", storage.ErrCloseBlocked)

	// arg 0 refused by policy, arg 1 closes, arg 2 refused by the engine.
	args := []string{"mx-1", "mx-2", "mx-3"}
	pre := closeProxiedPreflight{
		items: []issueops.BatchCloseItem{
			{IssueID: "mx-2", Reason: "done"},
			{IssueID: "mx-3", Reason: "done"},
		},
		itemArgs:      []int{1, 2},
		before:        map[string]*types.Issue{"mx-2": {ID: "mx-2"}},
		errors:        make([]string, len(args)),
		failureErrors: make([]string, len(args)),
	}
	// closeProxiedRunPreflight fills both slots for a policy refusal.
	pre.errors[0] = policyRefusal
	pre.failureErrors[0] = policyRefusal

	closeProxiedOutcomes(&pre, issueops.CloseBatchResult{
		Outcomes: []issueops.CloseOutcome{
			{IssueID: "mx-2", Issue: &types.Issue{ID: "mx-2"}, Changed: true},
			{IssueID: "mx-3", Err: engineErr},
		},
	})

	failures := closeProxiedFailures(&pre, args)
	want := []closeIDFailure{
		{ID: "mx-1", Error: policyRefusal},
		{ID: "mx-3", Error: engineErr.Error()},
	}
	if len(failures) != len(want) {
		t.Fatalf("failures = %+v, want %+v", failures, want)
	}
	for i := range want {
		if failures[i] != want[i] {
			t.Errorf("failures[%d] = %+v, want %+v", i, failures[i], want[i])
		}
	}

	// An entry never goes out reason-less. No production writer can reach this
	// state — both fill the twin — but the degradation is defined rather than
	// accidental, because an empty reason is the one answer a caller asking why
	// an id failed cannot use.
	pre.failureErrors[0] = ""
	if got := closeProxiedFailures(&pre, args); got[0].Error != policyRefusal {
		t.Errorf("failures[0].Error = %q, want the display line %q as the fallback", got[0].Error, policyRefusal)
	}
}
