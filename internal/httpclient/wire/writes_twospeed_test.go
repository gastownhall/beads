// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/writes_twospeed_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// The TWO-SPEED POLICY at the WRITE door.
//
// The store has one door for reads — (*Store).dispatch — and
// TestEveryDispatchSiteHonorsTheTwoSpeedPolicy proves nothing reaches the
// transport around it. The write roles do not use that door: they hold a typed
// WriteWire and call ClaimIssue, CloseIssue, UpdateIssue and the rest, each of
// which builds its own request here. So the property the read test proves for
// the store has to be proved AGAIN, one layer down, for these methods — and it
// is a different proof, because there is no single call site to point at.
//
// Two halves, because either alone is satisfiable by a mistake:
//
//	behavioral   an operation whose token the server does not advertise must
//	             refuse BEFORE its own request is made. Observed at the server,
//	             not at a spy: what matters is that the path was never dialed.
//	structural   every operation method routes through c.dispatch rather than
//	             c.Do. A method that preflighted by hand would satisfy the
//	             behavioral half today and drift the first time someone copies
//	             it, which is exactly how the sixth of six such methods gets
//	             written without the preflight.
//
// The failure this prevents is the one D6 names: an unrouted path on an older
// server answers a bare 404, and a 404 on this surface reads as "no such
// issue". A write that skipped the preflight would tell a user their issue does
// not exist when the truth is that their server cannot close issues.

// writeOp is one operation method as the roles reach it. The call drives the
// real method with the smallest arguments it accepts; nothing here inspects the
// result, because the question is only which requests reached the server.
type writeOp struct {
	name string
	call func(context.Context, *Client) error
}

func writeOps() []writeOp {
	return []writeOp{
		{OpClaimIssue, func(ctx context.Context, c *Client) error {
			_, err := c.ClaimIssue(ctx, "bd-1", apigen.ClaimRequest{Actor: "w"})
			return err
		}},
		// The only write here that carries a QUERY, and the only one that names
		// no resource: the filter that chooses the row is the ready listing's
		// vocabulary spelled as a query string.
		{OpClaimNextIssue, func(ctx context.Context, c *Client) error {
			_, err := c.ClaimNextIssue(ctx, url.Values{"sort": []string{"hybrid"}}, apigen.ClaimNextRequest{Actor: "w"})
			return err
		}},
		{OpCloseIssue, func(ctx context.Context, c *Client) error {
			_, err := c.CloseIssue(ctx, "bd-1", apigen.CloseIssueRequest{Actor: "w"})
			return err
		}},
		{OpReopenIssue, func(ctx context.Context, c *Client) error {
			_, err := c.ReopenIssue(ctx, "bd-1", apigen.ReopenIssueRequest{Actor: "w"})
			return err
		}},
		{OpReleaseIssue, func(ctx context.Context, c *Client) error {
			_, err := c.ReleaseIssue(ctx, "bd-1", apigen.ReleaseIssueRequest{Actor: "w"})
			return err
		}},
		{OpAddComment, func(ctx context.Context, c *Client) error {
			_, err := c.AddComment(ctx, "bd-1", apigen.AddCommentRequest{Author: "w", Text: "t"})
			return err
		}},
		{OpUpdateIssue, func(ctx context.Context, c *Client) error {
			_, err := c.UpdateIssue(ctx, "bd-1", "w", map[string]any{"title": "t"}, UpdateGuards{})
			return err
		}},
		{OpCreateIssue, func(ctx context.Context, c *Client) error {
			_, err := c.CreateIssue(ctx, apigen.CreateIssueRequest{Actor: "w", Title: "t"})
			return err
		}},
		{OpCompareAndSetMetadata, func(ctx context.Context, c *Client) error {
			_, err := c.CompareAndSetMetadata(ctx, "bd-1", apigen.CompareAndSetMetadataRequest{Actor: "w", Key: "k"})
			return err
		}},
		{OpAddDependencies, func(ctx context.Context, c *Client) error {
			_, err := c.AddDependencies(ctx, apigen.AddDependenciesRequest{
				Actor: "w",
				Edges: []apigen.DependencyEdge{{IssueId: "bd-1", DependsOnId: "bd-2", Type: "blocks"}},
			})
			return err
		}},
		{OpRemoveDependency, func(ctx context.Context, c *Client) error {
			_, err := c.RemoveDependency(ctx, apigen.RemoveDependencyRequest{
				Actor: "w", IssueId: "bd-1", DependsOnId: "bd-2",
			})
			return err
		}},
		{OpSweepIssues, func(ctx context.Context, c *Client) error {
			_, err := c.SweepIssues(ctx, apigen.SweepRequest{Tier: apigen.Ephemeral})
			return err
		}},
		{OpDeleteIssues, func(ctx context.Context, c *Client) error {
			_, err := c.DeleteIssues(ctx, apigen.DeleteIssuesRequest{Ids: []string{"bd-1"}})
			return err
		}},
		{OpBatchCreateIssues, func(ctx context.Context, c *Client) error {
			_, err := c.BatchCreateIssues(ctx, apigen.BatchCreateRequest{
				Actor: "w", Items: []apigen.BatchCreateItem{{Title: "t"}},
			})
			return err
		}},
		{OpBatchCloseIssues, func(ctx context.Context, c *Client) error {
			_, err := c.BatchCloseIssues(ctx, apigen.BatchCloseRequest{
				Actor: "w", Items: []apigen.BatchCloseItem{{Id: "bd-1"}},
			})
			return err
		}},
		{OpApplyBatch, func(ctx context.Context, c *Client) error {
			_, err := c.ApplyBatch(ctx, ApplyBatchRequest{
				Actor: "w",
				Items: []ApplyItem{{
					Kind:   string(apigen.ApplyItemKindCreate),
					Create: &apigen.ApplyCreateItem{Title: "t"},
				}},
			})
			return err
		}},
		{OpRememberMemory, func(ctx context.Context, c *Client) error {
			_, err := c.RememberMemory(ctx, apigen.RememberRequest{})
			return err
		}},
		{OpGetMemory, func(ctx context.Context, c *Client) error {
			_, err := c.RecallMemory(ctx, "k")
			return err
		}},
		{OpForgetMemory, func(ctx context.Context, c *Client) error {
			_, err := c.ForgetMemory(ctx, "k")
			return err
		}},
		{OpListMemories, func(ctx context.Context, c *Client) error {
			_, err := c.ListMemories(ctx, "")
			return err
		}},
		{OpListReadyWork, func(ctx context.Context, c *Client) error {
			_, err := c.ListReadyWork(ctx, url.Values{})
			return err
		}},
	}
}

func (op writeOp) run(t *testing.T, c *Client) error {
	t.Helper()
	return op.call(ctx(t), c)
}

func TestEveryWriteOperationPreflightsBeforeItDials(t *testing.T) {
	ops := writeOps()
	if len(ops) == 0 {
		t.Fatal("no write operations are classified; there is nothing for this gate to check")
	}

	every := allTokens()
	for _, op := range ops {
		token, known := CapabilityFor(op.name)
		if !known {
			t.Errorf("%s is not on the capability map", op.name)
			continue
		}
		if IsBaseline(op.name) {
			// Not a skipped case: the baseline half has its own gate below, and
			// a subtest that only ever skips is the shape this whole wiring
			// exists to eliminate.
			continue
		}
		t.Run(op.name, func(t *testing.T) {
			// A server that advertises the whole vocabulary EXCEPT this
			// operation's token. The holdout matters: a "server has nothing"
			// fixture passes even for a method wired to a neighbour's token.
			held := make([]string, 0, len(every))
			for _, candidate := range every {
				if candidate != token {
					held = append(held, candidate)
				}
			}
			c, rec := newTestClient(t, Options{}, nil, serveEverything(contextBody("v0", "proj-1", held...)))

			err := op.run(t, c)
			var absent *CapabilityError
			if !errors.As(err, &absent) {
				t.Fatalf("%s against a server that does not advertise %q = %v (%T), want a *CapabilityError",
					op.name, token, err, err)
			}
			if absent.Capability != token {
				t.Errorf("%s refused on capability %q, want %q", op.name, absent.Capability, token)
			}

			// The half that is the whole point: the operation's own request was
			// never made. Only the handshake reached the server.
			if rec.count() != 1 {
				t.Fatalf("%s made %d requests, want only the handshake", op.name, rec.count())
			}
			if got := rec.at(t, 0).path; got != PathContext {
				t.Errorf("%s dialed %q before refusing; an unrouted path on an older server answers a bare 404, "+
					"which is indistinguishable from an entity's not_found (D6)", op.name, got)
			}
		})
	}
}

// TestEveryBaselineWriteOperationDialsWithoutConsultingTheList is the other
// polarity, and it is not symmetric decoration: the two-speed policy exists to
// keep the hot work-distribution path off a second round trip, so a baseline
// operation that started preflighting would be a real regression that the test
// above cannot see.
func TestEveryBaselineWriteOperationDialsWithoutConsultingTheList(t *testing.T) {
	var baseline int
	for _, op := range writeOps() {
		if !IsBaseline(op.name) {
			continue
		}
		baseline++
		t.Run(op.name, func(t *testing.T) {
			// A server advertising NOTHING. A baseline operation still dials.
			c, rec := newTestClient(t, Options{}, nil, serveEverything(contextBody("v0", "proj-1")))
			if err := op.run(t, c); err != nil {
				t.Fatalf("%s against a server advertising nothing: %v", op.name, err)
			}
			if rec.count() != 1 {
				t.Fatalf("%s made %d requests, want exactly its own", op.name, rec.count())
			}
			if got := rec.at(t, 0).path; got == PathContext {
				t.Errorf("%s fetched the context first; a baseline operation must not", op.name)
			}
		})
	}
	// listReadyWork is the ONE baseline operation the write door reaches (the
	// composed ClaimNext is its only caller here). claimIssue used to be the
	// second, but ga-b8ddd.11 pulled it out of baselineOps: a claim is a write,
	// and the handshake it now forces is what runs the project-identity gate
	// before it. So claimIssue is exercised by
	// TestEveryWriteOperationPreflightsBeforeItDials instead, which stays green
	// because that PR keeps ClaimIssue routing through c.dispatch. The baseline
	// set is now five — the four reads plus getContext/health liveness — and
	// none of them is a write.
	if baseline != 1 {
		t.Errorf("%d baseline write operations, want 1; the baseline set moved and this gate's premise with it", baseline)
	}
}

// TestEveryOperationMethodRoutesThroughTheSharedDispatch is the structural half.
//
// writes.go's own comment states the rule — "each one preflights ITSELF... the
// capability check is not a policy the CALLER should be able to forget" — and
// the way it is kept is that every method calls c.dispatch, which is
// preflight-then-Do and nothing else. A method reaching c.Do directly would skip
// the preflight silently; only the behavioral half above would catch it, and
// only for the operations someone remembered to table.
//
// SCOPE CAVEAT: this reads writes.go alone, by design — every operation method
// lives there today, and confining the scan is what lets it assert "exactly one
// dispatch, no bare Do" per method without tripping over the handshake's own
// Do calls in handshake.go. The blind spot is the price: an operation method
// added to a DIFFERENT file escapes this gate entirely. The behavioral half is
// the backstop — a method that skipped the preflight would dial an unadvertised
// path in TestEveryWriteOperationPreflightsBeforeItDials — but only if it was
// also added to writeOps(), which TestEveryOperationMethodIsCoveredByTheTwoSpeedTable
// enforces for the methods in this file and cannot for one outside it. A new
// operation belongs in writes.go; put it elsewhere and both gates need widening.
func TestEveryOperationMethodRoutesThroughTheSharedDispatch(t *testing.T) {
	file := parseWritesFile(t)

	var checked int
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || !fn.Name.IsExported() {
			continue
		}
		checked++
		var dispatches, dials int
		ast.Inspect(fn, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "dispatch":
				dispatches++
			case "Do":
				dials++
			}
			return true
		})
		if dials > 0 {
			t.Errorf("%s calls Do directly; it must go through dispatch, which is the preflight-then-Do pairing "+
				"none of these methods may skip", fn.Name.Name)
		}
		if dispatches != 1 {
			t.Errorf("%s makes %d dispatch calls, want exactly 1", fn.Name.Name, dispatches)
		}
	}
	if checked == 0 {
		t.Fatal("found no exported operation methods in writes.go; the file moved and this gate went vacuous")
	}
}

// TestEveryOperationMethodIsCoveredByTheTwoSpeedTable keeps the table above from
// going stale by omission. A method added to writes.go tomorrow with no entry
// here would be a write door nothing checks, and the structural gate alone
// cannot say whether its token is the right one.
func TestEveryOperationMethodIsCoveredByTheTwoSpeedTable(t *testing.T) {
	var tabled []string
	for _, op := range writeOps() {
		tabled = append(tabled, op.name)
	}

	file := parseWritesFile(t)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || !fn.Name.IsExported() {
			continue
		}
		op := operationDispatched(fn)
		if op == "" {
			t.Errorf("%s dispatches no recognizable Op constant; the table cannot be checked against it", fn.Name.Name)
			continue
		}
		if !slices.Contains(tabled, op) {
			t.Errorf("writes.go's %s dispatches %s, which no writeOps() entry drives", fn.Name.Name, op)
		}
	}
}

// operationDispatched reads the Op the method's Request literal names, as the
// identifier it is spelled with (`OpCloseIssue`), resolved through the package's
// own constants.
func operationDispatched(fn *ast.FuncDecl) string {
	var found string
	ast.Inspect(fn, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Op" {
			return true
		}
		if ident, ok := kv.Value.(*ast.Ident); ok {
			found = opConstants()[ident.Name]
		}
		return false
	})
	return found
}

func parseWritesFile(t *testing.T) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "writes.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse writes.go: %v", err)
	}
	return file
}

// serveEverything answers the context with body and every other path with an
// empty JSON object, which decodes into any of this client's response types.
func serveEverything(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == PathContext {
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}
}

// opConstants maps the Go identifier of each operation constant to its value, so
// the AST scan above can turn `OpCloseIssue` into "closeIssue" without a second
// hand-kept copy of the pairing.
func opConstants() map[string]string {
	out := map[string]string{}
	for op := range opCapability {
		// The identifier is Op + the operationId with an upper-case initial.
		out["Op"+strings.ToUpper(op[:1])+op[1:]] = op
	}
	// The two identity operations are on the map with an empty token, so the
	// loop above already covered them.
	return out
}
