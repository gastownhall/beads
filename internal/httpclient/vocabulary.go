// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/vocabulary.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The off-role config, statistics and vocabulary reads: the probes cmd/bd still
// makes directly, each riding the operation its role rides (design D4).
//
// They live off the unsupported allowlist because a v0 operation answers each of
// them, and they are served DEGRADED where the wire's source is narrower than
// the store's — which is ledger row L7 and is the reason every one of these
// methods is documented as "must never hard-fail". LoadStoreListConfig wraps the
// four vocabulary reads in a failure that would kill every `bd list`, so a
// refusal here is not a refusal of a feature, it is a refusal of the command.

// GetAllConfig serves listSettings. Routing reads it; the server omits redacted
// values, and routing keys are not credential-bearing (D4).
func (s *Store) GetAllConfig(ctx context.Context) (map[string]string, error) {
	cfg, err := s.WorkspaceConfig()
	if err != nil {
		return nil, err
	}
	res, err := cfg.ListSettings(ctx, issueops.ListSettingsRequest{})
	if err != nil {
		return nil, err
	}
	return res.Settings, nil
}

// GetStatistics serves getStats. It answers the auto-import gate, the routing
// notice and `bd ready`'s empty-state probe (D4) — the last of which is why this
// is served rather than stubbed: an empty-state probe that failed would print
// "No open issues" against a workspace full of them.
func (s *Store) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	reporter, err := s.StatsReporter()
	if err != nil {
		return nil, err
	}
	res, err := reporter.Stats(ctx, issueops.StatsRequest{})
	if err != nil {
		return nil, err
	}
	summary := res.Summary
	return &summary, nil
}

// The status and type vocabulary, served from the pre-migration config keys that
// are its only wire-visible source (L7).
//
// EVERY ONE OF THESE SWALLOWS ITS TRANSPORT ERROR into the empty vocabulary, and
// that is a decision rather than sloppiness. LoadStoreListConfig turns any error
// here into a hard failure of `bd list` (internal/workapi/list.go), so a
// momentarily busy server would take down the listing rather than degrade it —
// and the vocabulary is already documented as degraded on this backend. What is
// NOT swallowed is a refusal to serve the listing itself: the reads that decide
// the answer go through the role and report their errors.

// GetCustomStatuses serves the custom status names.
func (s *Store) GetCustomStatuses(ctx context.Context) ([]string, error) {
	detailed, err := s.GetCustomStatusesDetailed(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(detailed))
	for _, cs := range detailed {
		names = append(names, cs.Name)
	}
	return names, nil
}

// GetCustomStatusesDetailed serves the custom statuses with their categories,
// from the `status.custom` key.
//
// The custom_statuses TABLE is the authoritative source on a migrated workspace
// and no v0 operation exposes it, so a table-backed vocabulary is invisible from
// here. That is L7, and it is survivable for one reason: server-side role
// validation still uses the true vocabulary, so the answers stay correct even
// where this client's copy of the names is short.
//
// THE ANSWER IS ORDERED BY NAME, which is not the order the config string lists
// them in. Every other leg reads the projected table with an explicit ORDER BY
// name, so name order is what this role answers rather than a property of any
// one storage layout — and a client that handed back the string's own order
// would be the only backend whose vocabulary changed shape when an operator
// rewrote the same set in a different sequence.
func (s *Store) GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error) {
	value, err := s.settingOrEmpty(ctx, issueops.SettingKeyStatusCustom)
	if err != nil {
		return nil, err
	}
	if value != "" {
		// A value that does not parse is answered as no custom statuses, which
		// is what the embedded resolver does with the same string: the workspace
		// is misconfigured, and failing the listing over it would be a harder
		// answer than the local store gives.
		if parsed, parseErr := types.ParseCustomStatusConfig(value); parseErr == nil {
			return sortStatusesByName(parsed), nil
		}
		return nil, nil
	}
	if yaml := config.GetCustomStatusesFromYAML(); len(yaml) > 0 {
		return sortStatusesByName(parseStatusFallback(yaml)), nil
	}
	return nil, nil
}

// GetCustomTypes serves the custom issue-type names from the `types.custom` key.
//
// THE VALUE HAS TWO SPELLINGS and the shared parser knows both: the JSON array
// `bd config set` and `bd pour` write, and the legacy comma-separated form.
// storageops.ParseTypesConfigValue IS that parser rather than a copy of it —
// this client reaching for the same function is what keeps a workspace
// configured through the ordinary front door from reading as ONE custom type
// named `["a","b"]` here and two everywhere else.
//
// Ordered by name for GetCustomStatusesDetailed's reason, and the same L7
// caveat applies: the custom_types table is invisible over v0, so the config
// string is the whole source.
func (s *Store) GetCustomTypes(ctx context.Context) ([]string, error) {
	value, err := s.settingOrEmpty(ctx, issueops.SettingKeyTypesCustom)
	if err != nil {
		return nil, err
	}
	if value != "" {
		names := storageops.ParseTypesConfigValue(value)
		slices.Sort(names)
		return names, nil
	}
	return config.GetCustomTypesFromYAML(), nil
}

// sortStatusesByName orders a parsed status vocabulary the way every leg's table
// read orders it. It sorts a copy's backing array in place, which is safe
// because both callers own the slice they pass.
func sortStatusesByName(statuses []types.CustomStatus) []types.CustomStatus {
	slices.SortFunc(statuses, func(a, b types.CustomStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	return statuses
}

// GetInfraTypes and IsInfraTypeCtx have NO error return, so they structurally
// cannot report a transport failure and cannot sit on the unsupported allowlist
// — which is why they were hand-written and refusing from the first commit.
// Now that `types.infra` is readable they answer from it, falling back to the
// same defaults the embedded resolver falls back to.
func (s *Store) GetInfraTypes(ctx context.Context) map[string]bool {
	value, err := s.settingOrEmpty(ctx, infraTypesKey)
	if err != nil {
		// Nowhere to report it, so the honest answer is the one the resolver
		// gives an unconfigured workspace rather than an empty map, which would
		// say "nothing is infrastructure" and admit agent and message rows into
		// every default listing.
		value = ""
	}
	names := splitList(value)
	if len(names) == 0 {
		names = config.GetInfraTypesFromYAML()
	}
	if len(names) == 0 {
		names = defaultInfraTypes()
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

// IsInfraTypeCtx reports whether t is one of the workspace's infra types.
func (s *Store) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	return s.GetInfraTypes(ctx)[string(t)]
}

// infraTypesKey is the flat config key the infra vocabulary lives under. It is
// spelled here rather than imported because the constant that holds it lives in
// the embedded storage package, which this client must not depend on.
const infraTypesKey = "types.infra"

// settingOrEmpty reads one setting, treating an unset key as the empty string —
// which is what the role already promises — and reporting only a real failure.
func (s *Store) settingOrEmpty(ctx context.Context, key string) (string, error) {
	value, err := s.GetConfig(ctx, key)
	if errors.Is(err, ErrSettingRedacted) {
		// A vocabulary key an operator marked credential-bearing is a
		// misconfiguration, not a secret worth failing over: these three name
		// statuses and issue types. GetConfig is right to report the withholding
		// to a caller that asked for the VALUE, and this caller is asking what
		// the workspace's vocabulary is — so it reads as unconfigured and the
		// listing degrades per L7 instead of dying.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// splitList parses the comma-separated form the flat vocabulary keys use.
func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseStatusFallback reads the bare-name YAML spelling, which carries no
// category. Unspecified is the right reading: the exclusion rule only fires on
// the done and frozen categories, so a name with no category is a status the
// default listing SHOWS — which is the conservative direction.
func parseStatusFallback(names []string) []types.CustomStatus {
	out := make([]types.CustomStatus, 0, len(names))
	for _, name := range names {
		out = append(out, types.CustomStatus{Name: name, Category: types.CategoryUnspecified})
	}
	return out
}

// defaultInfraTypes mirrors the domain default. It is three literals rather than
// an import because internal/storage/domain is the embedded engine's own
// package; a drift here is caught by the parent-walk round trip, which fails the
// moment the client's derived exclusions stop matching the server's.
func defaultInfraTypes() []string { return []string{"agent", "role", "message"} }

// listConfig is the client-side vocabulary the parent-walk inversion recognizes
// with, cached for the life of the store.
//
// CACHED because the walk asks for it once per level and the answer cannot
// change within one command: `bd list` already loaded exactly this value to
// BUILD the filter being inverted, so re-reading it per level would be three
// wire calls to re-learn something the process has held since before the walk
// started.
//
// It is assembled from the same three reads workapi's own loader makes, but it
// is assembled through the bridge subpackage rather than by importing that
// loader: the store is on the wrong side of the client boundary for a workapi
// dependency, and the bridge is the allowlisted side.
//
// A read that fails leaves the vocabulary EMPTY rather than failing the walk.
// That is the conservative direction, not a shrug: an empty vocabulary still
// recognizes the built-in defaults, and a filter carrying a custom exclusion
// this client could not read then refuses loudly instead of encoding a narrower
// question.
func (s *Store) listConfig(ctx context.Context) encode.ListConfig {
	s.vocabOnce.Do(func() {
		statuses, err := s.GetCustomStatusesDetailed(ctx)
		if err != nil {
			return
		}
		customTypes, err := s.GetCustomTypes(ctx)
		if err != nil {
			return
		}
		s.vocab = encode.NewListConfig(statuses, customTypes, s.GetInfraTypes(ctx))
	})
	return s.vocab
}

// vocabularyCache is the store's memoized ListConfig. It is a separate type so
// the zero Store stays usable by the refusal contract, which constructs one with
// no fields set.
type vocabularyCache struct {
	vocabOnce sync.Once
	vocab     encode.ListConfig
}

// isNotFound reports the wire's not_found, which several roles turn back into a
// result rather than an error.
func isNotFound(err error) bool { return errors.Is(err, issueops.ErrNotFound) }
