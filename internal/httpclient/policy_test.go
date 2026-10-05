// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/policy_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The external-dependency policy marker (policy.go). The served tier proves the
// policy end to end; these pin the decision itself — which handshake answers
// true, that it is settled once, and what the client-side policy's dependency
// reads say when the server cannot help.

func policyContext(caps ...string) *apigen.ContextResponse {
	return &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: caps}
}

// TestExternalDependencyCapabilityMatchesTheServer holds the client's spelling
// of the token to the server's. A drift would make every client answer false
// against a server that does enforce the policy — safe, but it would fail every
// `bd ready` on a store the client cannot read dependency records from.
func TestExternalDependencyCapabilityMatchesTheServer(t *testing.T) {
	if wire.CapExternalDependencies != httpapi.CapExternalDependencies {
		t.Fatalf("wire.CapExternalDependencies = %q, httpapi.CapExternalDependencies = %q",
			wire.CapExternalDependencies, httpapi.CapExternalDependencies)
	}
}

func TestPolicyEnforcedByServerAnswersOnlyTheAdvertisedCapability(t *testing.T) {
	cases := []struct {
		name string
		wire *fakeWire
		want bool
	}{
		{"advertised", &fakeWire{res: policyContext("ready.list", wire.CapExternalDependencies)}, true},
		{"absent", &fakeWire{res: policyContext("ready.list", wire.CapProjectEnforce)}, false},
		{"empty capability list", &fakeWire{res: policyContext()}, false},
		// A capability name that only CONTAINS the token is not the token.
		{"lookalike", &fakeWire{res: policyContext(wire.CapExternalDependencies + ".v2")}, false},
		{"handshake failed", &fakeWire{err: errors.New("dial tcp: connection refused")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(testTarget(t), tc.wire, nil)
			if got := s.PolicyEnforcedByServer(); got != tc.want {
				t.Fatalf("PolicyEnforcedByServer() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("no transport", func(t *testing.T) {
		if New(testTarget(t), nil, nil).PolicyEnforcedByServer() {
			t.Fatal("a store with no transport and no snapshot claimed the server enforces the policy")
		}
	})
	t.Run("nil store", func(t *testing.T) {
		var s *Store
		if s.PolicyEnforcedByServer() {
			t.Fatal("a nil store claimed the server enforces the policy")
		}
	})
	t.Run("pre-seeded snapshot", func(t *testing.T) {
		s := New(testTarget(t), nil, policyContext(wire.CapExternalDependencies))
		if !s.PolicyEnforcedByServer() {
			t.Fatal("a store seeded with an advertising snapshot answered false")
		}
	})
}

// TestPolicyEnforcedByServerIsSettledOnce pins the caching: the composition
// decision and the refusals it leads to must describe ONE handshake, and a
// command pays for at most one.
func TestPolicyEnforcedByServerIsSettledOnce(t *testing.T) {
	t.Run("a success is shared with the store's handshake cache", func(t *testing.T) {
		fw := &fakeWire{res: policyContext(wire.CapExternalDependencies)}
		s := New(testTarget(t), fw, nil)
		for range 3 {
			if !s.PolicyEnforcedByServer() {
				t.Fatal("PolicyEnforcedByServer() = false")
			}
		}
		if _, err := s.snapshot(context.Background()); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if fw.hits != 1 {
			t.Fatalf("handshake fetched %d times, want 1", fw.hits)
		}
	})
	t.Run("a failure is not retried under the same decision", func(t *testing.T) {
		fw := &fakeWire{err: errors.New("dial tcp: connection refused")}
		s := New(testTarget(t), fw, nil)
		if s.PolicyEnforcedByServer() {
			t.Fatal("PolicyEnforcedByServer() = true after a failed handshake")
		}
		// The server comes back: the decision already made stands, and the
		// policy's refusal reports the failure that decided it.
		fw.err = nil
		fw.res = policyContext(wire.CapExternalDependencies)
		if s.PolicyEnforcedByServer() {
			t.Fatal("the settled answer changed within one store")
		}
		_, err := s.GetExternalBlockingDependencyRecords(context.Background())
		if err == nil || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("GetExternalBlockingDependencyRecords() = %v, want the handshake failure", err)
		}
		if fw.hits != 1 {
			t.Fatalf("handshake fetched %d times, want 1", fw.hits)
		}
	})
}

// TestPolicyDependencyReadsNameTheCapability pins the old-server refusal: the
// client-side policy's two dependency reads fail with a *wire.CapabilityError
// naming policy.external_dependencies, never with a generic "not supported" and
// never with a nil that would let the policy decide on no edges.
func TestPolicyDependencyReadsNameTheCapability(t *testing.T) {
	reads := map[string]func(*Store) error{
		"GetExternalBlockingDependencyRecords": func(s *Store) error {
			got, err := s.GetExternalBlockingDependencyRecords(context.Background())
			if got != nil {
				t.Errorf("records = %v alongside the refusal, want nil", got)
			}
			return err
		},
		"GetDependencyRecordsForIssues": func(s *Store) error {
			got, err := s.GetDependencyRecordsForIssues(context.Background(), []string{"bd-1"})
			if got != nil {
				t.Errorf("records = %v alongside the refusal, want nil", got)
			}
			return err
		},
	}
	for name, read := range reads {
		t.Run(name+"/capability absent", func(t *testing.T) {
			s := New(testTarget(t), &fakeWire{res: policyContext("ready.list")}, nil)
			err := read(s)
			var capErr *wire.CapabilityError
			if !errors.As(err, &capErr) {
				t.Fatalf("err = %v (%T), want *wire.CapabilityError", err, err)
			}
			if capErr.Capability != wire.CapExternalDependencies {
				t.Errorf("Capability = %q, want %q", capErr.Capability, wire.CapExternalDependencies)
			}
			if capErr.ServerURL != "http://127.0.0.1:7777" || capErr.BdVersion != "1.2.3" {
				t.Errorf("refusal does not name the server: %+v", capErr)
			}
			if !errors.Is(err, wire.ErrCapabilityAbsent) {
				t.Error("refusal does not match wire.ErrCapabilityAbsent")
			}
			if !strings.Contains(err.Error(), wire.CapExternalDependencies) {
				t.Errorf("refusal text does not name the capability: %v", err)
			}
		})
		t.Run(name+"/handshake failed", func(t *testing.T) {
			down := errors.New("cannot reach bd serve at http://127.0.0.1:7777")
			s := New(testTarget(t), &fakeWire{err: down}, nil)
			if err := read(s); !errors.Is(err, down) {
				t.Fatalf("err = %v, want the handshake failure itself", err)
			}
		})
		t.Run(name+"/capability advertised", func(t *testing.T) {
			// The policy is not on the chain then; an unrelated caller gets the
			// ordinary typed refusal it always got.
			s := New(testTarget(t), &fakeWire{res: policyContext(wire.CapExternalDependencies)}, nil)
			err := read(s)
			var unsup *ErrHTTPUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("err = %v (%T), want *ErrHTTPUnsupported", err, err)
			}
			var generic *storage.ErrUnsupported
			if !errors.As(err, &generic) || generic.Op != name {
				t.Errorf("err does not unwrap to storage.ErrUnsupported{Op: %q}: %v", name, err)
			}
		})
	}
}

// TestExternalDepsWrapHonorsTheMarker drives the composition cmd/bd uses: the
// policy is left off exactly when the server advertises it, and put on — never
// skipped — otherwise.
func TestExternalDepsWrapHonorsTheMarker(t *testing.T) {
	wrap := func(s *Store) storage.DoltStorage {
		return externaldeps.Wrap(s,
			func(externaldeps.ProjectName) (string, bool) { return "", false },
			func(context.Context, string) (storage.DoltStorage, error) { return nil, errors.New("unused") })
	}

	enforcing := New(testTarget(t), &fakeWire{res: policyContext(wire.CapExternalDependencies)}, nil)
	if got := wrap(enforcing); got != storage.DoltStorage(enforcing) {
		t.Errorf("Wrap(%T advertising the capability) = %T, want the store unchanged", enforcing, got)
	}

	for name, fw := range map[string]*fakeWire{
		"capability absent": {res: policyContext("ready.list")},
		"handshake failed":  {err: errors.New("connection refused")},
	} {
		s := New(testTarget(t), fw, nil)
		got := wrap(s)
		if !externaldeps.Composed(got) {
			t.Errorf("%s: Wrap returned %T, want the client-side policy layer", name, got)
		}
	}
}

// TestReadyListerSurvivesThePolicyAgainstAnOldServer is the whole old-server
// path at the role seam: the client-side policy over this store refuses the
// listing with the capability's name instead of listing externally blocked work.
func TestReadyListerSurvivesThePolicyAgainstAnOldServer(t *testing.T) {
	fw := &fakeWire{
		res:       policyContext("ready.list", "ready.count"),
		preflight: func(context.Context, string) error { return nil },
		do: func(_ context.Context, req wire.Request, _ any) error {
			t.Errorf("the policy dispatched %s before it could exclude anything", req.Op)
			return nil
		},
	}
	s := New(testTarget(t), fw, nil)
	chain := externaldeps.Wrap(s,
		func(externaldeps.ProjectName) (string, bool) { return "", false },
		func(context.Context, string) (storage.DoltStorage, error) { return nil, errors.New("unused") })
	lister, err := chain.ReadyLister()
	if err != nil {
		t.Fatalf("ReadyLister(): %v", err)
	}
	_, err = lister.ListReady(context.Background(), issueops.ReadyListRequest{})
	if !errors.Is(err, wire.ErrCapabilityAbsent) {
		t.Fatalf("ListReady through the client-side policy = %v, want the capability refusal", err)
	}
}

// TestHTTPReadyListerTotal pins how the listing sizes its set: from the page
// when the server says the page is the tail, from one countReadyWork round trip
// otherwise, and never below the rows the page returned.
func TestHTTPReadyListerTotal(t *testing.T) {
	limit := func(n int) *int { return &n }
	cases := []struct {
		name      string
		req       issueops.ReadyListRequest
		items     int
		hasMore   bool
		count     int64
		wantTotal int64
		wantCount bool
	}{
		{"whole set in the page", issueops.ReadyListRequest{ReadyRequest: issueops.ReadyRequest{Limit: limit(10)}}, 3, false, 0, 3, false},
		{"truncated page", issueops.ReadyListRequest{ReadyRequest: issueops.ReadyRequest{Limit: limit(2)}}, 2, true, 7, 7, true},
		{"count lower than the page", issueops.ReadyListRequest{ReadyRequest: issueops.ReadyRequest{Limit: limit(2)}}, 2, true, 1, 2, true},
		{"empty set", issueops.ReadyListRequest{}, 0, false, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ops []string
			var countQuery url.Values
			fw := &fakeWire{
				res:       policyContext(),
				preflight: func(context.Context, string) error { return nil },
				do: func(_ context.Context, req wire.Request, out any) error {
					ops = append(ops, req.Op)
					switch body := out.(type) {
					case *apigen.ReadyPage:
						body.Items = make([]apigen.IssueWithCounts, tc.items)
						for i := range body.Items {
							body.Items[i].Issue = &types.Issue{ID: "bd-" + string(rune('a'+i))}
						}
						body.HasMore = tc.hasMore
					case *apigen.ReadyCount:
						countQuery = req.Query
						body.Total = tc.count
					}
					return nil
				},
			}
			lister, err := New(testTarget(t), fw, nil).ReadyLister()
			if err != nil {
				t.Fatalf("ReadyLister(): %v", err)
			}
			got, err := lister.ListReady(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("ListReady: %v", err)
			}
			if len(got.Items) != tc.items || got.HasMore != tc.hasMore || got.Total != tc.wantTotal {
				t.Errorf("listing = %d items, has_more %v, total %d; want %d, %v, %d",
					len(got.Items), got.HasMore, got.Total, tc.items, tc.hasMore, tc.wantTotal)
			}
			counted := len(ops) == 2 && ops[1] == wire.OpCountReadyWork
			if counted != tc.wantCount || ops[0] != wire.OpListReadyWork {
				t.Errorf("dispatched %v, want the listing%s", ops, map[bool]string{true: " then the count", false: " alone"}[tc.wantCount])
			}
			if counted && (countQuery.Has("limit") || countQuery.Has("offset")) {
				t.Errorf("the count carried the page: %v", countQuery)
			}
		})
	}
}
