package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// serveRolesTestConfig is the store arm's httpapi.Config as runServe spells
// it, for tests: every role plus the advertisement, both off one serveRoles.
// TestServeWiresTheAdvertisementFromWhatItComposed pins that runServe's own
// literal takes ExternalDependencyPolicy from the same place.
func serveRolesTestConfig(roles serveRoles) httpapi.Config {
	return httpapi.Config{
		Reader: roles.reader, Claimer: roles.claimer, BatchCloser: roles.batchCloser,
		ReadyClaimer: roles.readyClaimer, Releaser: roles.releaser, Lifecycle: roles.lifecycle,
		Settings: roles.settings, Stats: roles.stats, CycleDetector: roles.cycles,
		EdgeReader: roles.edges, GraphCounter: roles.edgeCounter, Relations: roles.relations,
		Commenter: roles.commenter, BlockingAnnotator: roles.blocking, TreeWalker: roles.tree,
		ReadyCounter: roles.readyCounter, Counter: roles.counter, Querier: roles.querier,
		Sweeper: roles.sweeper, Deleter: roles.deleter, BatchCreator: roles.batchCreator,
		DependencyEditor: roles.dependencyEditor, MetadataCAS: roles.metadataCAS,
		BatchApplier: roles.batchApplier, Memories: roles.memories,
		EventsJournal:            roles.eventsJournal,
		ExternalDependencyPolicy: roles.externalDependencyPolicy,
	}
}

// listenForTest binds cfg on an ephemeral loopback port for the life of the
// test and returns its address.
func listenForTest(t *testing.T, cfg httpapi.Config) string {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	cfg.Stdout, cfg.Stderr = io.Discard, io.Discard
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		t.Fatalf("httpapi.Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	return srv.Addr()
}

// serveContextCapabilities binds cfg on an ephemeral loopback port, asks
// GET /v0/beads/context, and returns the advertised capabilities.
func serveContextCapabilities(t *testing.T, cfg httpapi.Config) []string {
	t.Helper()
	addr := listenForTest(t, cfg)
	resp, err := http.Get("http://" + addr + "/v0/beads/context")
	if err != nil {
		t.Fatalf("GET /v0/beads/context: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v0/beads/context: status %d", resp.StatusCode)
	}
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	return body.Capabilities
}

// TestServeAdvertisesTheExternalDependencyPolicyItComposed pins U7 on both of
// serve's arms: the policy.external_dependencies capability is advertised
// exactly when the roles serve answers from were built by the policy layer —
// and on the store arm it checks that those very roles apply the policy — while
// a store the policy was never put on (a client of a policy-enforcing server)
// and a bare provider advertise nothing.
func TestServeAdvertisesTheExternalDependencyPolicyItComposed(t *testing.T) {
	const token = httpapi.CapExternalDependencies

	t.Run("store arm composes and advertises", func(t *testing.T) {
		clearTelemetryEnv(t)
		stub := newPolicyChainStub(t)
		stub.edges = map[string][]*types.Dependency{
			"bd-held": {{IssueID: "bd-held", DependsOnID: "external:nowhere:thing", Type: types.DepBlocks}},
		}
		chain := wireStorageDecorators(stub, hooks.NewRunner(t.TempDir()), false)
		roles, err := serveIssueRoles(chain, false)
		if err != nil {
			t.Fatalf("serveIssueRoles: %v", err)
		}
		cfg := serveRolesTestConfig(roles)
		if !cfg.ExternalDependencyPolicy {
			t.Fatal("serve composed its roles through the policy layer but did not say so")
		}
		// The roles in the config carry the policy: a claim reaches the
		// backend with the externally blocked row excluded.
		if _, err := cfg.ReadyClaimer.ClaimNext(t.Context(), issueops.ClaimNextRequest{Actor: "w", Filter: issueops.ReadyRequest{Sort: "priority"}}); err != nil {
			t.Fatalf("ClaimNext: %v", err)
		}
		if len(stub.claims) != 1 || !slices.Equal(stub.claims[0].Filter.ExcludeIDs, []string{"bd-held"}) {
			t.Fatalf("served claim reached the backend as %+v, want ExcludeIDs [bd-held]", stub.claims)
		}
		// The policy stub leaves roles this case never calls nil, which Listen
		// refuses, so the store arm reads the list the context handler serves
		// (httpapi.AdvertisedCapabilities, pinned on the wire by httpapi's
		// TestExternalDependencyCapabilityFollowsTheConfig); the provider arm
		// below goes over the wire.
		if caps := httpapi.AdvertisedCapabilities(cfg); !slices.Contains(caps, token) {
			t.Errorf("capabilities %v do not advertise %q", caps, token)
		}
	})

	t.Run("store arm over a server-enforced store advertises nothing", func(t *testing.T) {
		clearTelemetryEnv(t)
		stub := newPolicyChainStub(t)
		stub.forbidEdges = true
		chain := wireStorageDecorators(serverEnforcedChainStub{stub}, hooks.NewRunner(t.TempDir()), false)
		roles, err := serveIssueRoles(chain, false)
		if err != nil {
			t.Fatalf("serveIssueRoles: %v", err)
		}
		cfg := serveRolesTestConfig(roles)
		if cfg.ExternalDependencyPolicy {
			t.Fatal("serve advertised a policy it never put on its roles")
		}
		if caps := httpapi.AdvertisedCapabilities(cfg); slices.Contains(caps, token) {
			t.Errorf("capabilities %v advertise %q for roles without the policy", caps, token)
		}
	})

	var listed []issueops.ReadyListRequest
	bare := listerOnlyProvider{t: t, listed: &listed, allowUW: true}

	t.Run("provider arm composes and advertises", func(t *testing.T) {
		provider := wireExternalDependencyUOWProvider(bare)
		cfg := httpapi.Config{Provider: provider, ExternalDependencyPolicy: externaldeps.Composed(provider)}
		if !cfg.ExternalDependencyPolicy {
			t.Fatal("serve wrapped its provider in the policy but did not say so")
		}
		if caps := serveContextCapabilities(t, cfg); !slices.Contains(caps, token) {
			t.Errorf("capabilities %v do not advertise %q", caps, token)
		}
	})

	t.Run("provider arm without the policy advertises nothing", func(t *testing.T) {
		cfg := httpapi.Config{Provider: bare, ExternalDependencyPolicy: externaldeps.Composed(bare)}
		if cfg.ExternalDependencyPolicy {
			t.Fatal("a bare provider was reported as carrying the policy")
		}
		caps := serveContextCapabilities(t, cfg)
		if slices.Contains(caps, token) {
			t.Errorf("capabilities %v advertise %q for a provider without the policy", caps, token)
		}
		if !slices.Equal(caps, httpapi.Capabilities()) {
			t.Errorf("capabilities = %v, want exactly the build-level %v", caps, httpapi.Capabilities())
		}
	})
}

// TestServeWiresTheAdvertisementFromWhatItComposed is the source-level half of
// U7, in the style of TestServeNamesOneDatabaseSourcePerServerItBuilds: every
// httpapi.Config literal cmd/bd builds sets ExternalDependencyPolicy, and sets
// it from what was composed — serveRoles.externalDependencyPolicy on the store
// arm, externaldeps.Composed on the provider arm — never from a constant or a
// flag. Without this, the behavioral test above would stay green against a
// runServe that simply forgot the field (advertising nothing) or hard-coded
// true.
func TestServeWiresTheAdvertisementFromWhatItComposed(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(packageDir(t), "serve.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse serve.go: %v", err)
	}
	lits := httpapiConfigLiterals(file)
	if len(lits) < 2 {
		t.Fatalf("serve.go builds %d httpapi.Config literals, want both arms", len(lits))
	}
	allowed := map[string]bool{
		"roles.externalDependencyPolicy":  true,
		"externaldeps.Composed(provider)": true,
	}
	seen := map[string]bool{}
	for _, lit := range lits {
		var value ast.Expr
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "ExternalDependencyPolicy" {
					value = kv.Value
				}
			}
		}
		if value == nil {
			t.Errorf("%s: this httpapi.Config does not set ExternalDependencyPolicy, so the server it builds never advertises the policy its roles carry",
				fset.Position(lit.Pos()))
			continue
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, value); err != nil {
			t.Fatal(err)
		}
		if !allowed[buf.String()] {
			t.Errorf("%s: ExternalDependencyPolicy is set from %q; it must be read off the composed value (%v)",
				fset.Position(lit.Pos()), buf.String(), allowed)
		}
		seen[buf.String()] = true
	}
	for want := range allowed {
		if !seen[want] {
			t.Errorf("no httpapi.Config in serve.go sets ExternalDependencyPolicy from %s; an arm lost its advertisement", want)
		}
	}
}
