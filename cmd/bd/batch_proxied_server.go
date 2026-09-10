package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// runBatchProxiedServer executes an already-parsed batch script in ONE
// proxied-server unit of work.
//
// The two modes divide exactly where the command's contract does. Parsing,
// validation, the dry-run echo, the empty-input no-op, the default commit
// message and every byte of output stay in batch.go and are shared: a batch
// script means the same thing whichever backend runs it, and a script rejected
// by the parser is rejected before either backend is reached. What is per-mode
// is the transaction primitive and the verbs the ops dispatch against —
// storage.Transaction there, the unit of work's use cases here.
//
// uow.RunTx is the analog of the classic `transact`: one transaction, one
// DOLT_COMMIT carrying the caller's message, and rollback of the WHOLE batch on
// the first failing line (the line is named in the error exactly as the classic
// path names it). RunTx additionally redoes the entire batch in a fresh unit of
// work when Dolt reports a serialization failure — the batch is re-executed
// against the winner's committed rows, never re-committed on a session the
// server already rolled back.
//
// NOT RunTxEphemeral: a batch writes versioned tables, and the ephemeral form
// commits without a Dolt commit (its whole point for the leases table), which
// would persist these writes while silently bypassing Dolt history.
func runBatchProxiedServer(ctx context.Context, ops []batchOp, commitMsg string) ([]batchOpResult, error) {
	if uowProvider == nil {
		return nil, HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	return uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) ([]batchOpResult, string, error) {
		// Built per attempt, not per call: a retried batch must report the
		// results of the attempt that actually committed.
		results := make([]batchOpResult, 0, len(ops))
		for _, op := range ops {
			res, rerr := runBatchOpUOW(ctx, uw, op)
			if rerr != nil {
				return nil, "", fmt.Errorf("line %d (%s): %w", op.line, op.raw, rerr)
			}
			results = append(results, res)
		}
		return results, commitMsg, nil
	})
}

// runBatchOpUOW dispatches one parsed op against the unit of work, the proxied
// twin of runBatchOp's dispatch against storage.Transaction. Same grammar, same
// arity checks, same error strings, same per-op result — only the verb changes.
//
// Plane routing is explicit here because it has to be: the classic transaction
// methods route an id to the issues or wisps tables themselves, while the
// domain use cases expose the two planes as separate verbs. Every op that names
// an existing id resolves it through workapi.GetIssueOrWisp first (both planes,
// this transaction's own snapshot) and then calls the matching verb, so a batch
// line that touches a wisp touches the wisp tables on both backends.
func runBatchOpUOW(ctx context.Context, uw uow.UnitOfWork, op batchOp) (batchOpResult, error) {
	actorName := getActor()
	result := batchOpResult{Line: op.line, Op: op.cmd}
	issues := uw.IssueUseCase()

	switch op.cmd {
	case "close":
		if len(op.args) < 1 {
			return result, fmt.Errorf("close requires <id>")
		}
		id := op.args[0]
		reason := "Closed"
		if len(op.args) > 1 {
			reason = strings.Join(op.args[1:], " ")
		}
		resolved, isWisp, err := batchResolveOpTarget(ctx, uw, id)
		if err != nil {
			return result, err
		}
		// Deliberately the UNCHECKED close, matching the classic batch's
		// issueops.CloseIssueInTx: `close <id>` in a batch does not apply close
		// policy at all (the asymmetry with `update status=closed` is documented
		// in the command's help and is contract, not oversight).
		params := domain.CloseIssueParams{Reason: reason}
		if isWisp {
			_, err = issues.CloseWisp(ctx, resolved, params, actorName)
		} else {
			_, err = issues.CloseIssue(ctx, resolved, params, actorName)
		}
		if err != nil {
			return result, err
		}
		result.Target = id
		return result, nil

	case "update":
		if len(op.args) < 2 {
			return result, fmt.Errorf("update requires <id> and at least one key=value")
		}
		id := op.args[0]
		updates, err := parseUpdateKVs(op.args[1:])
		if err != nil {
			return result, err
		}
		resolved, isWisp, err := batchResolveOpTarget(ctx, uw, id)
		if err != nil {
			return result, err
		}
		if isWisp {
			err = issues.UpdateWisp(ctx, resolved, updates, actorName)
		} else {
			err = issues.UpdateIssue(ctx, resolved, updates, actorName)
		}
		if err != nil {
			return result, err
		}
		result.Target = id
		return result, nil

	case "create":
		if len(op.args) < 3 {
			return result, fmt.Errorf("create requires <type> <priority> <title>")
		}
		issueType := types.IssueType(op.args[0])
		if strings.TrimSpace(op.args[0]) == "" {
			return result, fmt.Errorf("create: type cannot be empty")
		}
		priority, err := strconv.Atoi(op.args[1])
		if err != nil {
			return result, fmt.Errorf("create: invalid priority %q: %w", op.args[1], err)
		}
		title := strings.Join(op.args[2:], " ")
		if strings.TrimSpace(title) == "" {
			return result, fmt.Errorf("create: title cannot be empty")
		}
		issue := &types.Issue{
			Title:     title,
			IssueType: issueType,
			Status:    types.StatusOpen,
			Priority:  priority,
		}
		created, err := issues.CreateIssue(ctx, domain.CreateIssueParams{Issue: issue}, actorName)
		if err != nil {
			return result, err
		}
		// The classic path reports the id the store minted into the caller's
		// struct; the use case hands the same row back as a result.
		if created.Issue != nil {
			result.Target = created.Issue.ID
		} else {
			result.Target = issue.ID
		}
		return result, nil

	case "dep.add":
		if len(op.args) < 2 {
			return result, fmt.Errorf("dep add requires <from-id> <to-id>")
		}
		from, to := op.args[0], op.args[1]
		depType := "blocks"
		if len(op.args) >= 3 {
			depType = op.args[2]
		}
		dt := types.DependencyType(depType)
		if !dt.IsValid() {
			return result, fmt.Errorf("dep add: invalid dependency type %q", depType)
		}
		dep := &types.Dependency{
			IssueID:     from,
			DependsOnID: to,
			Type:        dt,
		}
		// AddDependencies (not the single-edge AddDependency) because it lands
		// each edge in the plane its own SOURCE lives in, which is the routing
		// the classic transaction does from isActiveWisp. One edge is still one
		// edge, and the per-edge cycle probe runs.
		//
		// KNOWN DIVERGENCE, in the HISTORY only: every domain edge verb records
		// a dependency_added event, while the classic batch reaches
		// storage.Transaction.AddDependency, whose zero DependencyAddOptions
		// leaves EmitEvent false — the "structural, quiet" setting meant for
		// create-with-deps and reparenting, not for a verb a user typed. So a
		// batched `dep add` shows up in `bd history` here and does not there.
		// The edge itself, its plane, its type and the cycle verdict are
		// identical; `bd dep add` at the CLI emits the event on BOTH backends,
		// which is why the classic batch's silence reads as the outlier.
		// Silencing it here would need a quiet variant on the use case that
		// nothing else wants, so this port takes the event and names it rather
		// than growing the domain surface to reproduce an inconsistency.
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actorName, domain.BulkAddDepsOpts{}); err != nil {
			return result, err
		}
		result.Target = fmt.Sprintf("%s->%s", from, to)
		return result, nil

	case "dep.remove":
		if len(op.args) < 2 {
			return result, fmt.Errorf("dep remove requires <from-id> <to-id>")
		}
		from, to := op.args[0], op.args[1]
		// BySource for the same reason dep.add uses the bulk verb: the edge is
		// removed from the plane its source lives in. A missing edge is not an
		// error on either backend — the removal is idempotent, and a batch that
		// re-runs must not roll back on an edge already gone. The
		// dependency_removed event carries the same divergence, and the same
		// reasoning, as the added one above.
		if _, err := uw.DependencyUseCase().RemoveDependencyBySource(ctx, from, to, actorName); err != nil {
			return result, err
		}
		result.Target = fmt.Sprintf("%s->%s", from, to)
		return result, nil
	}
	return result, fmt.Errorf("internal: unhandled batch op %q", op.cmd)
}

// batchResolveOpTarget resolves an id an op names to the row it is about and
// the plane that row lives in, inside this transaction. A missing id is an
// error, which rolls the whole batch back — the same verdict the classic path
// reaches when its store call cannot find the row.
func batchResolveOpTarget(ctx context.Context, uw uow.UnitOfWork, id string) (string, bool, error) {
	issue, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
	if err != nil {
		return "", false, err
	}
	return issue.ID, isWisp, nil
}
