package main

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These pin --ttl on `bd update --claim`, `bd ready --claim` and
// `bd heartbeat` without a database: the flag, the refusals that fire before
// either route is chosen, the context value each in-process route hands its
// role, and the refusal on the remote bd serve route. The lease the flag arms
// in a real store is lease_ttl_flag_store_test.go's job.

// setTTLTestFlag sets one flag on cmd for the test and puts it back after.
// pflag.Set leaves Changed true, which would outlive the test.
func setTTLTestFlag(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		f := cmd.Flags().Lookup(name)
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
}

// runCommandForTTLTest runs cmd's RunE with stdout and stderr captured.
func runCommandForTTLTest(t *testing.T, cmd *cobra.Command, args []string) (string, error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	var err error
	stdout, stderr := captureStdio(t, func() {
		err = cmd.RunE(cmd, args)
	})
	return stdout + stderr, err
}

func TestLeaseTTLFlagIsADurationOnTheClaimAndHeartbeatCommands(t *testing.T) {
	for _, cmd := range []*cobra.Command{updateCmd, readyCmd, heartbeatCmd} {
		f := cmd.Flags().Lookup(leaseTTLFlagName)
		if f == nil {
			t.Errorf("bd %s has no --%s flag", cmd.Name(), leaseTTLFlagName)
			continue
		}
		if f.Value.Type() != "duration" {
			t.Errorf("bd %s --%s is a %s, want a duration", cmd.Name(), leaseTTLFlagName, f.Value.Type())
		}
		// The heartbeat question: a heartbeat without --ttl cuts a longer
		// lease back to the default, and the help must say so.
		if !strings.Contains(f.Usage, "every heartbeat") {
			t.Errorf("bd %s --%s help does not tell the worker to pass it on every heartbeat:\n%s", cmd.Name(), leaseTTLFlagName, f.Usage)
		}
	}
}

// TestLeaseTTLFlagRefusesMisuseBeforeTheRouteDispatch drives each command's
// RunE with no store at all: every refusal here must fire before anything is
// read, on either route, so a missing one falls through to a store error that
// does not name --ttl.
func TestLeaseTTLFlagRefusesMisuseBeforeTheRouteDispatch(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	store = nil
	oldCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldCtx })

	for _, tc := range []struct {
		name  string
		cmd   *cobra.Command
		args  []string
		flags map[string]string
		want  string
	}{
		{"update ttl without claim", updateCmd, []string{"bd-1"}, map[string]string{"ttl": "1h", "status": "open"}, "--ttl requires --claim"},
		{"update zero ttl", updateCmd, []string{"bd-1"}, map[string]string{"ttl": "0", "claim": "true"}, "--ttl must be a positive duration"},
		{"update negative ttl", updateCmd, []string{"bd-1"}, map[string]string{"ttl": "-5m", "claim": "true"}, "--ttl must be a positive duration"},
		{"ready ttl without claim", readyCmd, nil, map[string]string{"ttl": "1h"}, "--ttl requires --claim"},
		{"ready ttl on gated without claim", readyCmd, nil, map[string]string{"ttl": "1h", "gated": "true"}, "--ttl requires --claim"},
		{"ready zero ttl", readyCmd, nil, map[string]string{"ttl": "0s", "claim": "true"}, "--ttl must be a positive duration"},
		{"ready negative ttl", readyCmd, nil, map[string]string{"ttl": "-1h", "claim": "true"}, "--ttl must be a positive duration"},
		{"heartbeat zero ttl", heartbeatCmd, []string{"bd-1"}, map[string]string{"ttl": "0"}, "--ttl must be a positive duration"},
		{"heartbeat negative ttl", heartbeatCmd, []string{"bd-1"}, map[string]string{"ttl": "-30s"}, "--ttl must be a positive duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, value := range tc.flags {
				setTTLTestFlag(t, tc.cmd, name, value)
			}
			shown, err := runCommandForTTLTest(t, tc.cmd, tc.args)
			if err == nil {
				t.Fatalf("bd %s = nil error, want a usage error naming %q\n%s", tc.cmd.Name(), tc.want, shown)
			}
			if !strings.Contains(shown, tc.want) {
				t.Errorf("bd %s printed %q, want it to name %q", tc.cmd.Name(), shown, tc.want)
			}
		})
	}
}

// ttlRecordingLifecycle records the lease TTL the context handed to Update
// resolves to, which is what the lease code would use.
type ttlRecordingLifecycle struct {
	issueops.Lifecycle
	ttl     time.Duration
	request issueops.UpdateRequest
	result  issueops.UpdateResult
}

func (l *ttlRecordingLifecycle) Update(ctx context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	l.ttl = storageissueops.LeaseTTL(ctx)
	l.request = request
	return l.result, nil
}

func TestRunCommandUpdateMutationCarriesTheLeaseTTL(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  time.Duration
		want time.Duration
	}{
		{"given", 2 * time.Hour, 2 * time.Hour},
		{"absent keeps the default", 0, storageissueops.DefaultLeaseTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &ttlRecordingLifecycle{}
			if _, err := runCommandUpdateMutation(context.Background(), l, commandUpdateMutation{
				actor: "agent", issueID: "bd-1", claim: true, leaseTTL: tc.ttl,
			}); err != nil {
				t.Fatalf("runCommandUpdateMutation: %v", err)
			}
			if l.ttl != tc.want {
				t.Errorf("the lifecycle saw a lease TTL of %v, want %v", l.ttl, tc.want)
			}
		})
	}
}

// TestWithLeaseTTLKeepsAnOuterLengthWhenNoneIsGiven pins withLeaseTTL's zero
// guard: an absent --ttl adds nothing to the context, so a lease length a
// caller further out already set is not masked by a zero.
func TestWithLeaseTTLKeepsAnOuterLengthWhenNoneIsGiven(t *testing.T) {
	outer := storageissueops.WithLeaseTTL(context.Background(), time.Hour)
	if got := storageissueops.LeaseTTL(withLeaseTTL(outer, 0)); got != time.Hour {
		t.Errorf("withLeaseTTL(ctx with 1h, 0) resolves to %v, want the outer 1h", got)
	}
	if got := storageissueops.LeaseTTL(withLeaseTTL(outer, 2*time.Hour)); got != 2*time.Hour {
		t.Errorf("withLeaseTTL(ctx with 1h, 2h) resolves to %v, want 2h", got)
	}
}

// TestGatherInputsReadTheLeaseTTL pins the proxied routes' half of the flag:
// RunE validates --ttl, and each route's gatherer reads the value back for
// its claim.
func TestGatherInputsReadTheLeaseTTL(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		setTTLTestFlag(t, updateCmd, "claim", "true")
		setTTLTestFlag(t, updateCmd, "ttl", "2h")
		in, err := gatherUpdateInput(context.Background(), updateCmd)
		if err != nil {
			t.Fatalf("gatherUpdateInput: %v", err)
		}
		if in.leaseTTL != 2*time.Hour {
			t.Errorf("updateInput.leaseTTL = %v, want 2h", in.leaseTTL)
		}
	})
	t.Run("ready", func(t *testing.T) {
		setTTLTestFlag(t, readyCmd, "claim", "true")
		setTTLTestFlag(t, readyCmd, "ttl", "3h")
		in, err := gatherReadyInput(readyCmd, nil)
		if err != nil {
			t.Fatalf("gatherReadyInput: %v", err)
		}
		if in.leaseTTL != 3*time.Hour {
			t.Errorf("readyInput.leaseTTL = %v, want 3h", in.leaseTTL)
		}
	})
}

// ttlProxiedProvider is a proxied-server provider whose lifecycle and ready
// claimer record the lease TTL their context carries.
type ttlProxiedProvider struct {
	lifecycle *ttlRecordingLifecycle
	details   *types.IssueDetails
	claimTTL  time.Duration
}

func (p *ttlProxiedProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return nil, errors.New("the claim path opened a unit of work of its own")
}

func (p *ttlProxiedProvider) Close(context.Context) error { return nil }

func (p *ttlProxiedProvider) IssueLifecycle() (issueops.Lifecycle, error) { return p.lifecycle, nil }

func (p *ttlProxiedProvider) IssueReader() (issueops.Reader, error) {
	return &stubIssueReader{details: p.details}, nil
}

func (p *ttlProxiedProvider) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return ttlRecordingReadyClaimer{p}, nil
}

type ttlRecordingReadyClaimer struct{ p *ttlProxiedProvider }

func (c ttlRecordingReadyClaimer) ClaimNext(ctx context.Context, _ issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	c.p.claimTTL = storageissueops.LeaseTTL(ctx)
	return issueops.ClaimNextResult{}, nil
}

func withTTLProxiedProvider(t *testing.T, before *types.Issue) *ttlProxiedProvider {
	t.Helper()
	claimed := *before
	claimed.Status = types.StatusInProgress
	claimed.Assignee = "agent"
	p := &ttlProxiedProvider{
		lifecycle: &ttlRecordingLifecycle{result: issueops.UpdateResult{Issue: &claimed, Changed: true}},
		details:   &types.IssueDetails{Issue: *before},
	}
	withClaimRoleProvider(t, &claimRoleProvider{})
	uowProvider = p
	oldActor := actor
	actor = "agent"
	t.Cleanup(func() { actor = oldActor })
	return p
}

func TestProxiedClaimsCarryTheLeaseTTL(t *testing.T) {
	t.Run("update --claim", func(t *testing.T) {
		p := withTTLProxiedProvider(t, &types.Issue{ID: "bd-1", Status: types.StatusOpen})
		var err error
		stderr := captureStderrDuring(t, func() {
			_, _, err = applyUpdateProxiedOne(context.Background(), "bd-1",
				&updateInput{claim: true, fields: map[string]any{}, leaseTTL: 2 * time.Hour})
		})
		if err != nil {
			t.Fatalf("applyUpdateProxiedOne: %v", err)
		}
		if p.lifecycle.ttl != 2*time.Hour {
			t.Errorf("the proxied lifecycle saw a lease TTL of %v, want 2h", p.lifecycle.ttl)
		}
		if strings.Contains(stderr, "not applied") {
			t.Errorf("a fresh claim warned that --ttl was not applied:\n%s", stderr)
		}
	})
	t.Run("update --claim re-claim warns", func(t *testing.T) {
		withTTLProxiedProvider(t, &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "agent"})
		stderr := captureStderrDuring(t, func() {
			_, _, _ = applyUpdateProxiedOne(context.Background(), "bd-1",
				&updateInput{claim: true, fields: map[string]any{}, leaseTTL: 2 * time.Hour})
		})
		if !strings.Contains(stderr, "--ttl 2h0m0s not applied to bd-1") {
			t.Errorf("a re-claim with --ttl did not say the lease was left as it was:\n%s", stderr)
		}
	})
	t.Run("ready --claim", func(t *testing.T) {
		p := withTTLProxiedProvider(t, &types.Issue{ID: "bd-1", Status: types.StatusOpen})
		stdioMutex.Lock()
		defer stdioMutex.Unlock()
		var err error
		captureStdio(t, func() {
			err = runReadyProxiedClaim(context.Background(), readyInput{claim: true, leaseTTL: 3 * time.Hour})
		})
		if err != nil {
			t.Fatalf("runReadyProxiedClaim: %v", err)
		}
		if p.claimTTL != 3*time.Hour {
			t.Errorf("the proxied ready claimer saw a lease TTL of %v, want 3h", p.claimTTL)
		}
	})
}

// TestWarnLeaseTTLNotApplied pins the two claims that succeed without arming
// a lease of the --ttl length, and that each says why in its own words: the
// re-claim names the heartbeat that does set the length, and the wisp does
// not, because a wisp has no lease for a heartbeat to refresh.
func TestWarnLeaseTTLNotApplied(t *testing.T) {
	const reclaimWords, wispWords = "you already held it", "it is a wisp"
	for _, tc := range []struct {
		name   string
		before *types.Issue
		ttl    time.Duration
		words  string // "" when nothing is printed
	}{
		{"re-claim of a held issue", &types.Issue{Status: types.StatusInProgress, Assignee: "agent"}, time.Hour, reclaimWords},
		{"fresh claim of an open issue", &types.Issue{Status: types.StatusOpen}, time.Hour, ""},
		{"claim of a pool issue", &types.Issue{Status: types.StatusOpen, Assignee: "crew-pool"}, time.Hour, ""},
		{"re-claim without --ttl", &types.Issue{Status: types.StatusInProgress, Assignee: "agent"}, 0, ""},
		{"fresh claim of a wisp", &types.Issue{Status: types.StatusOpen, Ephemeral: true}, time.Hour, wispWords},
		{"re-claim of a held wisp", &types.Issue{Status: types.StatusInProgress, Assignee: "agent", Ephemeral: true}, time.Hour, wispWords},
		{"claim of a wisp without --ttl", &types.Issue{Status: types.StatusOpen, Ephemeral: true}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr := captureStderrDuring(t, func() {
				warnLeaseTTLNotApplied("bd-1", tc.before, "agent", tc.ttl)
			})
			if tc.words == "" {
				if stderr != "" {
					t.Errorf("printed a warning, want none; stderr:\n%s", stderr)
				}
				return
			}
			if !strings.Contains(stderr, "not applied to bd-1") || !strings.Contains(stderr, tc.words) {
				t.Errorf("stderr does not carry the %q warning for bd-1:\n%s", tc.words, stderr)
			}
			if tc.words == wispWords && strings.Contains(stderr, "bd heartbeat") {
				t.Errorf("the wisp warning names bd heartbeat, which has no lease to refresh on a wisp:\n%s", stderr)
			}
		})
	}
}

// The ID in the warnings is the store's, and the re-claim warning pastes it
// into a bd heartbeat command. An ID that is no plain word goes into the
// command the way every remedy spells a pasted value (remedy.CommandArg):
// quoted for the shell, or as a placeholder named escaped after the command.
// The prose names it escaped, so it cannot redraw the line.
func TestWarnLeaseTTLNotAppliedSpellsTheIDForAShell(t *testing.T) {
	held := &types.Issue{Status: types.StatusInProgress, Assignee: "agent"}
	wisp := &types.Issue{Status: types.StatusOpen, Ephemeral: true}
	for _, id := range []string{"bd-1", "bd-1 $(touch x)", "bd-1\r\x1b[2K"} {
		arg, note := remedy.CommandArg(runtime.GOOS, id, "<issue-id>")
		if plain := id == "bd-1"; (arg == id) != plain {
			t.Fatalf("remedy.CommandArg(%q) = %q: the case is not the one this test means", id, arg)
		}
		stderr := captureStderrDuring(t, func() { warnLeaseTTLNotApplied(id, held, "agent", time.Hour) })
		if want := "(bd heartbeat --ttl 1h0m0s " + arg + " sets its length)" + note + "\n"; !strings.HasSuffix(stderr, want) {
			t.Errorf("re-claim warning for %q does not end with %q:\n%q", id, want, stderr)
		}
		if want := "not applied to " + remedy.PrintedWord(id) + ": you already held it"; !strings.Contains(stderr, want) {
			t.Errorf("re-claim warning for %q does not say %q:\n%q", id, want, stderr)
		}
		stderr = captureStderrDuring(t, func() { warnLeaseTTLNotApplied(id, wisp, "agent", time.Hour) })
		if want := "not applied to " + remedy.PrintedWord(id) + ": it is a wisp"; !strings.Contains(stderr, want) {
			t.Errorf("wisp warning for %q does not say %q:\n%q", id, want, stderr)
		}
	}
}

// ttlRemoteStore is a network client of a remote bd serve: it resolves one
// issue and records which role a command reached for.
type ttlRemoteStore struct {
	storage.DoltStorage
	issue *types.Issue
	roles []string
}

func (s *ttlRemoteStore) IsRemoteBackendStore() bool { return true }

func (s *ttlRemoteStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return []*types.Issue{s.issue}, nil
}

func (s *ttlRemoteStore) GetIssue(context.Context, string) (*types.Issue, error) {
	return s.issue, nil
}

func (s *ttlRemoteStore) IssueLifecycle() (issueops.Lifecycle, error) {
	s.roles = append(s.roles, "lifecycle")
	return nil, errors.New("the claim reached the remote lifecycle")
}

func (s *ttlRemoteStore) ReadyClaimer() (issueops.ReadyClaimer, error) {
	s.roles = append(s.roles, "ready-claimer")
	return nil, errors.New("the claim reached the remote ready claimer")
}

func (s *ttlRemoteStore) Heartbeater() (issueops.Heartbeater, error) {
	s.roles = append(s.roles, "heartbeater")
	return nil, errors.New("the heartbeat reached the remote heartbeater")
}

func TestLeaseTTLIsRefusedOnTheRemoteBackendRoute(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cmd   *cobra.Command
		args  []string
		flags map[string]string
	}{
		{"update --claim", updateCmd, []string{"rb-1"}, map[string]string{"claim": "true", "ttl": "2h"}},
		{"ready --claim", readyCmd, nil, map[string]string{"claim": "true", "ttl": "2h"}},
		{"heartbeat", heartbeatCmd, []string{"rb-1"}, map[string]string{"ttl": "2h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ensureCleanGlobalState(t)
			saveAndRestoreGlobals(t)
			oldCtx := rootCtx
			rootCtx = context.Background()
			t.Cleanup(func() { rootCtx = oldCtx })
			remote := &ttlRemoteStore{issue: &types.Issue{ID: "rb-1", Title: "remote", Status: types.StatusOpen}}
			store = remote
			for name, value := range tc.flags {
				setTTLTestFlag(t, tc.cmd, name, value)
			}

			shown, err := runCommandForTTLTest(t, tc.cmd, tc.args)
			if err == nil {
				t.Fatalf("bd %s --ttl on a remote backend = nil error, want the refusal\n%s", tc.cmd.Name(), shown)
			}
			if !strings.Contains(shown, "remote bd serve route") {
				t.Errorf("bd %s printed %q, want the refusal naming the remote bd serve route", tc.cmd.Name(), shown)
			}
			if len(remote.roles) != 0 {
				t.Errorf("bd %s reached %v on the remote backend; --ttl must be refused before the request is sent", tc.cmd.Name(), remote.roles)
			}
		})
	}
}

func TestLeaseTTLRemoteRefusalIsTheRemoteRouteWithATTLOnly(t *testing.T) {
	remote := &ttlRemoteStore{}
	if err := leaseTTLRemoteRefusal(remote, 0, "claim"); err != nil {
		t.Errorf("no --ttl on a remote backend refused: %v", err)
	}
	if err := leaseTTLRemoteRefusal(&ttlLocalStore{}, time.Hour, "claim"); err != nil {
		t.Errorf("--ttl on a local store refused: %v", err)
	}
	if err := leaseTTLRemoteRefusal(remote, time.Hour, "claim"); err == nil {
		t.Error("--ttl on a remote backend was not refused")
	}
}

// ttlLocalStore is any store that is not a remote backend.
type ttlLocalStore struct{ storage.DoltStorage }
