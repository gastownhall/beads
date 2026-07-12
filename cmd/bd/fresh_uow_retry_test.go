package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

type proxiedRetryTestProvider struct {
	uows  []uow.UnitOfWork
	calls int
}

func (p *proxiedRetryTestProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	if p.calls >= len(p.uows) {
		return nil, fmt.Errorf("unexpected NewUOW call %d", p.calls+1)
	}
	uw := p.uows[p.calls]
	p.calls++
	return uw, nil
}

func (*proxiedRetryTestProvider) Close(context.Context) error { return nil }

type proxiedRetryTestUOW struct {
	uow.UnitOfWork
	issueUC       domain.IssueUseCase
	depUC         domain.DependencyUseCase
	configUC      domain.ConfigUseCase
	commitErr     error
	commitCalls   int
	closeCalls    int
	commitMessage string
}

func (u *proxiedRetryTestUOW) IssueUseCase() domain.IssueUseCase { return u.issueUC }
func (u *proxiedRetryTestUOW) DependencyUseCase() domain.DependencyUseCase {
	if u.depUC != nil {
		return u.depUC
	}
	return &proxiedRetryTestDepUC{}
}
func (u *proxiedRetryTestUOW) ConfigUseCase() domain.ConfigUseCase {
	if u.configUC != nil {
		return u.configUC
	}
	return &proxiedRetryTestConfigUC{}
}
func (u *proxiedRetryTestUOW) Commit(_ context.Context, message string) error {
	u.commitCalls++
	u.commitMessage = message
	return u.commitErr
}
func (u *proxiedRetryTestUOW) Close(context.Context) { u.closeCalls++ }

type proxiedRetryTestDepUC struct {
	domain.DependencyUseCase
	addCalls    int
	removeCalls int
}

func (*proxiedRetryTestDepUC) GetForIssueIDs(context.Context, []string) (map[string][]*types.Dependency, error) {
	return map[string][]*types.Dependency{}, nil
}
func (*proxiedRetryTestDepUC) IsBlocked(context.Context, string) (bool, []string, error) {
	return false, nil, nil
}
func (*proxiedRetryTestDepUC) DetectCycles(context.Context) ([][]*types.Issue, error) {
	return nil, nil
}
func (d *proxiedRetryTestDepUC) AddDependencies(context.Context, []*types.Dependency, string, domain.BulkAddDepsOpts) (domain.BulkAddDepsResult, error) {
	d.addCalls++
	return domain.BulkAddDepsResult{}, nil
}
func (d *proxiedRetryTestDepUC) RemoveDependency(context.Context, string, string, string) error {
	d.removeCalls++
	return nil
}

type proxiedRetryTestConfigUC struct {
	domain.ConfigUseCase
	cctx domain.CreateContext
}

func (c *proxiedRetryTestConfigUC) LoadCreateContext(context.Context) (domain.CreateContext, error) {
	return c.cctx, nil
}
func (*proxiedRetryTestConfigUC) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

type proxiedRetryTestIssueUC struct {
	domain.IssueUseCase
	current         *types.Issue
	updated         *types.Issue
	closed          *types.Issue
	created         *types.Issue
	ready           domain.ClaimReadyResult
	getCalls        int
	applyCalls      int
	claimReadyCalls int
	closeCalls      int
	createCalls     int
}

func (u *proxiedRetryTestIssueUC) GetIssue(context.Context, string) (*types.Issue, error) {
	u.getCalls++
	if u.current == nil {
		// Title lookup and optional resolve paths treat missing as not-found.
		return nil, nil
	}
	return u.current, nil
}

func (*proxiedRetryTestIssueUC) GetWisp(context.Context, string) (*types.Issue, error) {
	return nil, nil
}

func (u *proxiedRetryTestIssueUC) ApplyUpdate(context.Context, string, domain.UpdateSpec, string) (*types.Issue, error) {
	u.applyCalls++
	return u.updated, nil
}

func (u *proxiedRetryTestIssueUC) ClaimReadyIssue(context.Context, types.WorkFilter, string) (domain.ClaimReadyResult, error) {
	u.claimReadyCalls++
	return u.ready, nil
}


func (u *proxiedRetryTestIssueUC) CloseIssue(_ context.Context, _ string, _ domain.CloseIssueParams, _ string) (domain.CloseIssueResult, error) {
	u.closeCalls++
	if u.closed == nil {
		return domain.CloseIssueResult{}, errors.New("missing test close result")
	}
	return domain.CloseIssueResult{Issue: u.closed, Closed: true}, nil
}

func (u *proxiedRetryTestIssueUC) CreateIssue(_ context.Context, _ domain.CreateIssueParams, _ string) (domain.CreateIssueResult, error) {
	u.createCalls++
	if u.created == nil {
		return domain.CreateIssueResult{}, errors.New("missing test create result")
	}
	return domain.CreateIssueResult{Issue: u.created}, nil
}

func TestApplyUpdateProxiedOneRetriesWholeOperationOnFreshUOW(t *testing.T) {
	t.Chdir(t.TempDir())
	oldProvider, oldActor := uowProvider, actor
	t.Cleanup(func() { uowProvider, actor = oldProvider, oldActor })
	actor = "retry-agent"

	firstUC := &proxiedRetryTestIssueUC{
		current: &types.Issue{ID: "retry-1", Title: "first snapshot", Status: types.StatusOpen, IssueType: types.TypeTask},
		updated: &types.Issue{ID: "retry-1", Title: "failed attempt", Status: types.StatusOpen, IssueType: types.TypeTask},
	}
	secondUC := &proxiedRetryTestIssueUC{
		current: &types.Issue{ID: "retry-1", Title: "fresh snapshot", Status: types.StatusOpen, IssueType: types.TypeTask},
		updated: &types.Issue{ID: "retry-1", Title: "durable attempt", Status: types.StatusOpen, IssueType: types.TypeTask},
	}
	firstUOW := &proxiedRetryTestUOW{
		issueUC:   firstUC,
		commitErr: &mysql.MySQLError{Number: 1213, Message: "serialization conflict"},
	}
	secondUOW := &proxiedRetryTestUOW{issueUC: secondUC}
	provider := &proxiedRetryTestProvider{uows: []uow.UnitOfWork{firstUOW, secondUOW}}
	uowProvider = provider

	got, ok, claimLost, err := applyUpdateProxiedOne(context.Background(), "retry-1", &updateInput{
		fields: map[string]any{"title": "requested title"},
	})
	if err != nil {
		t.Fatalf("applyUpdateProxiedOne: %v", err)
	}
	if !ok || claimLost {
		t.Fatalf("result ok=%v claimLost=%v, want true/false", ok, claimLost)
	}
	if got != secondUC.updated {
		t.Fatalf("returned issue = %#v, want durable second-attempt issue %#v", got, secondUC.updated)
	}
	if provider.calls != 2 || firstUC.getCalls != 1 || firstUC.applyCalls != 1 || secondUC.getCalls != 1 || secondUC.applyCalls != 1 {
		t.Fatalf("fresh replay counts: provider=%d first(get=%d apply=%d) second(get=%d apply=%d)",
			provider.calls, firstUC.getCalls, firstUC.applyCalls, secondUC.getCalls, secondUC.applyCalls)
	}
	if firstUOW.closeCalls != 1 || secondUOW.closeCalls != 1 {
		t.Fatalf("UOW closes: first=%d second=%d, want 1/1", firstUOW.closeCalls, secondUOW.closeCalls)
	}
	for i, uw := range []*proxiedRetryTestUOW{firstUOW, secondUOW} {
		if uw.commitCalls != 1 || uw.commitMessage != "bd: update retry-1" {
			t.Errorf("UOW %d commit calls/message = %d/%q", i, uw.commitCalls, uw.commitMessage)
		}
	}
}

func TestRunReadyProxiedClaimRetriesWholeOperationOnFreshUOW(t *testing.T) {
	t.Chdir(t.TempDir())
	oldProvider, oldActor, oldReadonly := uowProvider, actor, readonlyMode
	t.Cleanup(func() { uowProvider, actor, readonlyMode = oldProvider, oldActor, oldReadonly })
	actor = "ready-retry-agent"
	readonlyMode = false

	firstUC := &proxiedRetryTestIssueUC{ready: domain.ClaimReadyResult{
		Issue:   &types.Issue{ID: "ready-failed", Title: "failed attempt", Status: types.StatusInProgress, IssueType: types.TypeTask},
		Claimed: true,
	}}
	secondUC := &proxiedRetryTestIssueUC{ready: domain.ClaimReadyResult{
		Issue:   &types.Issue{ID: "ready-durable", Title: "durable attempt", Status: types.StatusInProgress, IssueType: types.TypeTask},
		Claimed: true,
	}}
	firstUOW := &proxiedRetryTestUOW{
		issueUC:   firstUC,
		commitErr: &mysql.MySQLError{Number: 1213, Message: "serialization conflict"},
	}
	secondUOW := &proxiedRetryTestUOW{issueUC: secondUC}
	provider := &proxiedRetryTestProvider{uows: []uow.UnitOfWork{firstUOW, secondUOW}}
	uowProvider = provider

	out := captureStdout(t, func() error {
		return runReadyProxiedClaim(context.Background(), readyInput{claim: true})
	})
	if !strings.Contains(out, "ready-durable") || strings.Contains(out, "ready-failed") {
		t.Fatalf("output exposed failed attempt instead of durable retry: %q", out)
	}
	if provider.calls != 2 || firstUC.claimReadyCalls != 1 || secondUC.claimReadyCalls != 1 {
		t.Fatalf("fresh ready replay counts: provider=%d first=%d second=%d",
			provider.calls, firstUC.claimReadyCalls, secondUC.claimReadyCalls)
	}
	if firstUOW.closeCalls != 1 || secondUOW.closeCalls != 1 {
		t.Fatalf("UOW closes: first=%d second=%d, want 1/1", firstUOW.closeCalls, secondUOW.closeCalls)
	}
	wantMessages := []string{"bd: ready --claim ready-failed", "bd: ready --claim ready-durable"}
	for i, uw := range []*proxiedRetryTestUOW{firstUOW, secondUOW} {
		if uw.commitCalls != 1 || uw.commitMessage != wantMessages[i] {
			t.Errorf("UOW %d commit calls/message = %d/%q, want 1/%q", i, uw.commitCalls, uw.commitMessage, wantMessages[i])
		}
	}
}


func TestCloseProxiedOneFreshRetriesWholeOperationOnFreshUOW(t *testing.T) {
	t.Chdir(t.TempDir())
	oldProvider, oldActor := uowProvider, actor
	t.Cleanup(func() { uowProvider, actor = oldProvider, oldActor })
	actor = "close-retry-agent"

	firstUC := &proxiedRetryTestIssueUC{
		current: &types.Issue{ID: "close-1", Title: "first snapshot", Status: types.StatusOpen, IssueType: types.TypeTask},
		closed:  &types.Issue{ID: "close-1", Title: "failed attempt", Status: types.StatusClosed, IssueType: types.TypeTask},
	}
	secondUC := &proxiedRetryTestIssueUC{
		current: &types.Issue{ID: "close-1", Title: "fresh snapshot", Status: types.StatusOpen, IssueType: types.TypeTask},
		closed:  &types.Issue{ID: "close-1", Title: "durable attempt", Status: types.StatusClosed, IssueType: types.TypeTask},
	}
	firstUOW := &proxiedRetryTestUOW{
		issueUC:   firstUC,
		commitErr: &mysql.MySQLError{Number: 1213, Message: "serialization conflict"},
	}
	secondUOW := &proxiedRetryTestUOW{issueUC: secondUC}
	provider := &proxiedRetryTestProvider{uows: []uow.UnitOfWork{firstUOW, secondUOW}}
	uowProvider = provider

	got, ok, err := closeProxiedOneFresh(context.Background(), "close-1", "done", closeProxiedInput{force: true})
	if err != nil {
		t.Fatalf("closeProxiedOneFresh: %v", err)
	}
	if !ok {
		t.Fatal("expected successful close")
	}
	if got.after != secondUC.closed {
		t.Fatalf("returned issue = %#v, want durable second-attempt issue %#v", got.after, secondUC.closed)
	}
	if provider.calls != 2 || firstUC.getCalls != 1 || firstUC.closeCalls != 1 || secondUC.getCalls != 1 || secondUC.closeCalls != 1 {
		t.Fatalf("fresh close replay counts: provider=%d first(get=%d close=%d) second(get=%d close=%d)",
			provider.calls, firstUC.getCalls, firstUC.closeCalls, secondUC.getCalls, secondUC.closeCalls)
	}
	if firstUOW.closeCalls != 1 || secondUOW.closeCalls != 1 {
		t.Fatalf("UOW closes: first=%d second=%d, want 1/1", firstUOW.closeCalls, secondUOW.closeCalls)
	}
	for i, uw := range []*proxiedRetryTestUOW{firstUOW, secondUOW} {
		if uw.commitCalls != 1 || uw.commitMessage != "bd: close close-1" {
			t.Errorf("UOW %d commit calls/message = %d/%q", i, uw.commitCalls, uw.commitMessage)
		}
	}
}

func TestRunCreateProxiedSingleRetriesWholeOperationOnFreshUOW(t *testing.T) {
	t.Chdir(t.TempDir())
	oldProvider, oldActor := uowProvider, actor
	t.Cleanup(func() { uowProvider, actor = oldProvider, oldActor })
	actor = "create-retry-agent"

	firstUC := &proxiedRetryTestIssueUC{
		created: &types.Issue{ID: "create-failed", Title: "failed attempt", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
	}
	secondUC := &proxiedRetryTestIssueUC{
		created: &types.Issue{ID: "create-durable", Title: "durable attempt", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
	}
	firstUOW := &proxiedRetryTestUOW{
		issueUC:   firstUC,
		commitErr: &mysql.MySQLError{Number: 1213, Message: "serialization conflict"},
	}
	secondUOW := &proxiedRetryTestUOW{issueUC: secondUC}
	provider := &proxiedRetryTestProvider{uows: []uow.UnitOfWork{firstUOW, secondUOW}}
	uowProvider = provider

	out := captureStdout(t, func() error {
		return runCreateProxiedSingle(nil, context.Background(), createInput{
			title:     "new issue",
			issueType: "task",
			priority:  2,
			createdBy: actor,
		})
	})
	if !strings.Contains(out, "create-durable") || strings.Contains(out, "create-failed") {
		t.Fatalf("output exposed failed attempt instead of durable retry: %q", out)
	}
	if provider.calls != 2 || firstUC.createCalls != 1 || secondUC.createCalls != 1 {
		t.Fatalf("fresh create replay counts: provider=%d first=%d second=%d",
			provider.calls, firstUC.createCalls, secondUC.createCalls)
	}
	wantMessages := []string{"bd: create create-failed", "bd: create create-durable"}
	for i, uw := range []*proxiedRetryTestUOW{firstUOW, secondUOW} {
		if uw.commitCalls != 1 || uw.commitMessage != wantMessages[i] {
			t.Errorf("UOW %d commit calls/message = %d/%q, want 1/%q", i, uw.commitCalls, uw.commitMessage, wantMessages[i])
		}
	}
}

func TestRunDepBlocksProxiedServerRetriesWholeOperationOnFreshUOW(t *testing.T) {
	t.Chdir(t.TempDir())
	oldProvider, oldActor, oldJSON := uowProvider, actor, jsonOutput
	t.Cleanup(func() { uowProvider, actor, jsonOutput = oldProvider, oldActor, oldJSON })
	actor = "dep-retry-agent"
	jsonOutput = false

	firstDep := &proxiedRetryTestDepUC{}
	secondDep := &proxiedRetryTestDepUC{}
	firstUOW := &proxiedRetryTestUOW{
		issueUC:   &proxiedRetryTestIssueUC{},
		depUC:     firstDep,
		commitErr: &mysql.MySQLError{Number: 1213, Message: "serialization conflict"},
	}
	secondUOW := &proxiedRetryTestUOW{
		issueUC: &proxiedRetryTestIssueUC{},
		depUC:   secondDep,
	}
	provider := &proxiedRetryTestProvider{uows: []uow.UnitOfWork{firstUOW, secondUOW}}
	uowProvider = provider

	// cobra command only needed for --no-cycle-check flag; use a minimal flag set.
	cmd := &cobra.Command{}
	cmd.Flags().Bool("no-cycle-check", true, "")

	out := captureStdout(t, func() error {
		return runDepBlocksProxiedServer(cmd, context.Background(), "blocker-1", "blocked-1")
	})
	if !strings.Contains(out, "blocker-1") || !strings.Contains(out, "blocked-1") {
		t.Fatalf("unexpected dep output: %q", out)
	}
	if provider.calls != 2 || firstDep.addCalls != 1 || secondDep.addCalls != 1 {
		t.Fatalf("fresh dep replay counts: provider=%d first=%d second=%d",
			provider.calls, firstDep.addCalls, secondDep.addCalls)
	}
	for i, uw := range []*proxiedRetryTestUOW{firstUOW, secondUOW} {
		if uw.commitCalls != 1 || uw.commitMessage != "bd: dep add blocked-1 blocker-1" {
			t.Errorf("UOW %d commit calls/message = %d/%q", i, uw.commitCalls, uw.commitMessage)
		}
	}
}
