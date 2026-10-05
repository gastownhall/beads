// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/policy.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"slices"
	"sync"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// The external-dependency policy on this backend.
//
// bd's ready/claim/close policy for `external:<project>:<capability>` blockers
// needs the workspace's dependency records, which a client of bd serve cannot
// read: the served dependency surface is anchored (listDependencies takes at
// least one issue id), and the policy asks for every external edge in the
// workspace. So the policy runs where the records are. A server that composed
// its roles through the policy layer advertises policy.external_dependencies,
// and this store then answers storage.ServerEnforcedPolicy true: cmd/bd leaves
// its own copy of the policy off the chain and forwards `bd ready`, `--claim`,
// `bd close` and `--claim-next` as asked.
//
// A server that does NOT advertise the capability lists and claims past
// `external:` edges, so the marker answers false and cmd/bd layers the policy
// on the client, exactly as it does for a local store. That layer then asks
// this store for the dependency records it cannot serve, and the two reads the
// policy makes — the workspace's external blocking edges and a named issue's own
// edges — refuse with the capability's name (policyRecordsUnavailable) rather
// than with a generic "not supported": the operator's fix is upgrading the
// server, and the refusal says so. The policy is never skipped silently.

var _ storage.ServerEnforcedPolicy = (*Store)(nil)
var _ storage.ExternalDependencyQueryStore = (*Store)(nil)

// PolicyEnforcedByServer implements storage.ServerEnforcedPolicy: true only
// when the connected server advertised policy.external_dependencies in its
// handshake. Every other outcome — the capability missing, a handshake that
// failed (unreachable server, wrong workspace, api_version skew), no transport
// at all — answers false, which keeps the client-side policy.
//
// The answer is settled ONCE per store, which is one connection for one
// command: cmd/bd composes its storage chain from it at open, and the refusals
// the client-side policy later earns (policyRecordsUnavailable) must describe
// the same decision rather than a second handshake that might disagree with it.
// The probe shares the store's handshake cache, so a command that later needs
// the snapshot for a post-baseline dispatch or a refusal pays nothing more.
func (s *Store) PolicyEnforcedByServer() bool {
	enforced, _ := s.probeServerPolicy()
	return enforced
}

// probeServerPolicy runs (once) and reports the handshake behind
// PolicyEnforcedByServer. The error is the handshake's own, kept so a refusal
// can name the real cause — a server that is down — rather than a capability
// the client never got to read.
//
// The marker's signature carries no context, so the probe runs under a fresh
// one; the transport's own per-request ceiling bounds it.
func (s *Store) probeServerPolicy() (bool, error) {
	if s == nil {
		return false, nil
	}
	s.policy.once.Do(func() {
		snap, err := s.snapshot(context.Background())
		if err != nil {
			s.policy.err = err
			return
		}
		s.policy.enforced = snap != nil && slices.Contains(snap.Capabilities, wire.CapExternalDependencies)
	})
	return s.policy.enforced, s.policy.err
}

// policyProbe is the settled answer PolicyEnforcedByServer gives.
type policyProbe struct {
	once     sync.Once
	enforced bool
	err      error
}

// policyRecordsUnavailable is the refusal for a dependency-record read the
// client-side external-dependency policy makes.
//
//   - The handshake failed: that error, unchanged. It is the actionable one (a
//     down server is "cannot reach bd serve at ..."), and a capability refusal
//     in its place would send the operator to upgrade a server they cannot
//     reach.
//   - The server does not advertise policy.external_dependencies: a
//     *wire.CapabilityError naming it, which cmd/bd renders as "bd <command>
//     requires server capability ... upgrade the server".
//   - The server does advertise it: the ordinary store-bound refusal. The
//     policy is not on the chain then, so only a caller unrelated to it gets
//     here, and it gets what it always got.
func (s *Store) policyRecordsUnavailable(op string) error {
	enforced, err := s.probeServerPolicy()
	if err != nil {
		return err
	}
	if enforced {
		return s.unsupported(op)
	}
	return wire.NewCapabilityError("the external-dependency policy", wire.CapExternalDependencies,
		s.target.String(), s.cachedSnapshot())
}

// GetExternalBlockingDependencyRecords implements
// storage.ExternalDependencyQueryStore for the policy's ready narrowing, which
// needs every external blocking edge in the workspace. No v0 operation lists
// them, so it refuses; see policyRecordsUnavailable.
func (s *Store) GetExternalBlockingDependencyRecords(_ context.Context) (map[string][]*types.Dependency, error) {
	return nil, s.policyRecordsUnavailable("GetExternalBlockingDependencyRecords")
}

// GetDependencyRecordsForIssues is the unanchored multi-id edge read. It stays
// unserved (ledger L13: `bd ready`'s pretty parent-epic probe swallows the
// refusal), and it is hand-written rather than generated only so its refusal
// can say why when the client-side policy is the caller: the policy's claim and
// close guards read a named issue's own edges through it.
func (s *Store) GetDependencyRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return nil, s.policyRecordsUnavailable("GetDependencyRecordsForIssues")
}
