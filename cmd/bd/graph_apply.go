package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

// GraphApplyPlan describes a symbolic bead graph to create atomically.
type GraphApplyPlan struct {
	CommitMessage string           `json:"commit_message,omitempty"`
	Nodes         []GraphApplyNode `json:"nodes"`
	Edges         []GraphApplyEdge `json:"edges,omitempty"`
}

// GraphApplyNode describes a single bead to create. Field names follow the
// types.Issue JSON tags (the names `bd show --json` emits), covering every
// field single-issue `bd create` can set plus initial status and pinned.
type GraphApplyNode struct {
	Key                string                     `json:"key"`
	ID                 string                     `json:"id,omitempty"` // explicit issue ID (default: generated)
	Title              string                     `json:"title"`
	Type               string                     `json:"type,omitempty"`
	Status             string                     `json:"status,omitempty"` // initial status (default: open, or deferred when defer_until is future)
	Description        string                     `json:"description,omitempty"`
	Design             string                     `json:"design,omitempty"`
	AcceptanceCriteria string                     `json:"acceptance_criteria,omitempty"`
	Notes              string                     `json:"notes,omitempty"`
	SpecID             string                     `json:"spec_id,omitempty"`
	ExternalRef        string                     `json:"external_ref,omitempty"`
	Assignee           string                     `json:"assignee,omitempty"`
	AssignAfterCreate  bool                       `json:"assign_after_create,omitempty"`
	Owner              string                     `json:"owner,omitempty"`
	Priority           *int                       `json:"priority,omitempty"`          // nil defaults to P2
	Estimate           *int                       `json:"estimate,omitempty"`          // minutes (alias for estimated_minutes)
	EstimatedMinutes   *int                       `json:"estimated_minutes,omitempty"` // minutes
	DueAt              *time.Time                 `json:"due_at,omitempty"`            // RFC3339
	DeferUntil         *time.Time                 `json:"defer_until,omitempty"`       // RFC3339
	Labels             []string                   `json:"labels,omitempty"`
	Metadata           map[string]json.RawMessage `json:"metadata,omitempty"`
	MetadataRefs       map[string]string          `json:"metadata_refs,omitempty"`
	Parent             string                     `json:"parent,omitempty"` // alias for parent_key
	ParentKey          string                     `json:"parent_key,omitempty"`
	ParentID           string                     `json:"parent_id,omitempty"`
	Deps               []GraphApplyNodeDep        `json:"deps,omitempty"`
	Ephemeral          *bool                      `json:"ephemeral,omitempty"`  // overrides --ephemeral for this node
	NoHistory          *bool                      `json:"no_history,omitempty"` // overrides --no-history for this node
	WispType           string                     `json:"wisp_type,omitempty"`
	MolType            string                     `json:"mol_type,omitempty"`
	Pinned             bool                       `json:"pinned,omitempty"`
	EventKind          string                     `json:"event_kind,omitempty"` // type=event only
	Actor              string                     `json:"actor,omitempty"`      // type=event only
	Target             string                     `json:"target,omitempty"`     // type=event only
	Payload            string                     `json:"payload,omitempty"`    // type=event only
}

// effectiveParentKey resolves the parent alias: parent_key wins, then parent.
// Every consumer of a node's plan-local parent (validation, dry-run, embedded
// apply, proxied apply) must go through this so the alias can't be dropped on
// one path.
func (n GraphApplyNode) effectiveParentKey() string {
	if n.ParentKey != "" {
		return n.ParentKey
	}
	return n.Parent
}

// GraphApplyEdge describes a dependency edge.
type GraphApplyEdge struct {
	FromKey string `json:"from_key,omitempty"`
	FromID  string `json:"from_id,omitempty"`
	ToKey   string `json:"to_key,omitempty"`
	ToID    string `json:"to_id,omitempty"`
	Type    string `json:"type,omitempty"`
	// Gate and spawner apply to waits-for edges only (fanout gates).
	Gate       string `json:"gate,omitempty"`        // all-children | any-children
	SpawnerKey string `json:"spawner_key,omitempty"` // plan-local key of the spawning node
	SpawnerID  string `json:"spawner_id,omitempty"`  // existing issue ID of the spawner
	ThreadID   string `json:"thread_id,omitempty"`   // conversation threading (replies-to)
}

// GraphApplyNodeDep describes an inline dependency on a single graph node.
// Target is resolved as a plan key first, then treated as a literal issue ID.
type GraphApplyNodeDep struct {
	Type   string `json:"type,omitempty"`
	Target string `json:"target"`
}

// GraphApplyResult returns the concrete bead IDs assigned to each symbolic key.
type GraphApplyResult struct {
	IDs map[string]string `json:"ids"`
}

// GraphApplyOptions carries CLI-level storage options that apply to every node
// in the graph.
type GraphApplyOptions struct {
	Ephemeral bool
	NoHistory bool
	Force     bool // --force: allow explicit IDs with foreign prefixes
}

func (opts GraphApplyOptions) Validate() error {
	if opts.Ephemeral && opts.NoHistory {
		return fmt.Errorf("ephemeral and no_history are mutually exclusive")
	}
	return nil
}

// GraphApplyDryRun describes the actions that would be taken by a graph plan,
// without performing any writes. Emitted by `bd create --graph --dry-run`.
type GraphApplyDryRun struct {
	DryRun          bool                  `json:"dry_run"`
	NodeCount       int                   `json:"node_count"`
	EdgeCount       int                   `json:"edge_count"`
	ParentDeps      int                   `json:"parent_deps"`
	ValidationNotes []string              `json:"validation_notes,omitempty"`
	Nodes           []GraphApplyDryRunRow `json:"nodes"`
}

// GraphApplyDryRunRow describes a single planned node in the dry-run preview.
type GraphApplyDryRunRow struct {
	Key       string `json:"key"`
	ID        string `json:"id,omitempty"` // explicit ID, when the plan sets one
	Title     string `json:"title"`
	Type      string `json:"type"`
	Status    string `json:"status,omitempty"` // effective initial status (explicit, or deferred when defer_until is future)
	Priority  int    `json:"priority"`
	ParentKey string `json:"parent_key,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
}

const graphApplyDryRunTransactionValidationNote = "dry-run validates the graph structure only; live create may still reject parent-child blocking paths after resolving stored dependencies"

// Known-field sets list the JSON keys recognized on each plan struct; unknown
// keys warn about schema typos. Derived from json tags so they can't drift. (GH#3367)
var (
	knownGraphPlanFields = jsonTagSet(reflect.TypeOf(GraphApplyPlan{}))
	knownGraphNodeFields = jsonTagSet(reflect.TypeOf(GraphApplyNode{}))
	knownGraphEdgeFields = jsonTagSet(reflect.TypeOf(GraphApplyEdge{}))
)

// jsonTagSet returns the set of JSON field names declared on t's struct tags.
func jsonTagSet(t reflect.Type) map[string]struct{} {
	out := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			tag = tag[:comma]
		}
		out[tag] = struct{}{}
	}
	return out
}

// graphFieldHints maps unknown-field names to a corrective hint pointing at
// the recognized schema field. Used by warnUnknownGraphFields to suggest the
// intended schema when a plan uses a common-but-wrong name (e.g. nodes carry
// a "parent" string instead of "parent_key", or "blocks" arrays instead of
// the top-level edges array). (GH#3367)
var graphFieldHints = map[string]string{
	"blocks":         "use the top-level 'edges' array or per-node 'deps', e.g. {\"deps\": [{\"target\": \"key\", \"type\": \"blocks\"}]}",
	"depends":        "use the top-level 'edges' array or per-node 'deps' with type 'blocks'",
	"children":       "set 'parent_key' or 'parent' on each child instead of listing children on the parent",
	"acceptance":     "use 'acceptance_criteria' (matching the issue model's JSON field)",
	"due":            "use 'due_at' with an RFC3339 timestamp",
	"defer":          "use 'defer_until' with an RFC3339 timestamp",
	"event_category": "use 'event_kind' (matching the issue model's JSON field)",
	"event_actor":    "use 'actor' (matching the issue model's JSON field)",
	"event_target":   "use 'target' (matching the issue model's JSON field)",
	"event_payload":  "use 'payload' (matching the issue model's JSON field)",
}

// detectUnknownGraphFields scans the raw plan JSON and returns unknown field
// names grouped by their location in the plan. The returned map keys describe
// the location ("plan", "node[<key-or-index>]", "edge[<index>]") and values
// are sorted lists of unknown field names at that location. Returns an empty
// map when the plan is structurally invalid (callers should still attempt the
// strict parse so the operator gets a normal parse error rather than only the
// schema warning). (GH#3367)
func detectUnknownGraphFields(rawData []byte) map[string][]string {
	out := make(map[string][]string)

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &top); err != nil {
		return out
	}

	if planUnknown := unknownKeys(top, knownGraphPlanFields); len(planUnknown) > 0 {
		out["plan"] = planUnknown
	}

	if nodesRaw, ok := top["nodes"]; ok {
		var rawNodes []json.RawMessage
		if err := json.Unmarshal(nodesRaw, &rawNodes); err == nil {
			for i, nodeRaw := range rawNodes {
				var nodeMap map[string]json.RawMessage
				if err := json.Unmarshal(nodeRaw, &nodeMap); err != nil {
					continue
				}
				if unknown := unknownKeys(nodeMap, knownGraphNodeFields); len(unknown) > 0 {
					label := fmt.Sprintf("node[%d]", i)
					if keyRaw, ok := nodeMap["key"]; ok {
						var keyStr string
						if err := json.Unmarshal(keyRaw, &keyStr); err == nil && keyStr != "" {
							label = fmt.Sprintf("node[%q]", keyStr)
						}
					}
					out[label] = unknown
				}
			}
		}
	}

	if edgesRaw, ok := top["edges"]; ok {
		var rawEdges []json.RawMessage
		if err := json.Unmarshal(edgesRaw, &rawEdges); err == nil {
			for i, edgeRaw := range rawEdges {
				var edgeMap map[string]json.RawMessage
				if err := json.Unmarshal(edgeRaw, &edgeMap); err != nil {
					continue
				}
				if unknown := unknownKeys(edgeMap, knownGraphEdgeFields); len(unknown) > 0 {
					out[fmt.Sprintf("edge[%d]", i)] = unknown
				}
			}
		}
	}

	return out
}

// unknownKeys returns the keys present in have that are not in known, sorted
// alphabetically for deterministic output. Matching is case-insensitive because
// encoding/json binds case-variant keys (e.g. "Pinned") to the lowercase field.
func unknownKeys(have map[string]json.RawMessage, known map[string]struct{}) []string {
	var unknown []string
	for k := range have {
		if _, ok := known[strings.ToLower(k)]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// warnUnknownGraphFields prints a single warning line per location in the
// plan with one or more unknown fields, plus a per-field hint when one is
// available. Output goes to w (typically os.Stderr). Returns the sorted
// list of distinct unknown field names for test assertion; production
// callers may safely ignore the result. (GH#3367)
//
//nolint:unparam // return value used by tests for assertion; production callers ignore
func warnUnknownGraphFields(w io.Writer, unknown map[string][]string) []string {
	if len(unknown) == 0 {
		return nil
	}

	locations := make([]string, 0, len(unknown))
	for loc := range unknown {
		locations = append(locations, loc)
	}
	sort.Strings(locations)

	distinct := make(map[string]struct{})
	for _, loc := range locations {
		fields := append([]string(nil), unknown[loc]...)
		sort.Strings(fields)
		fmt.Fprintf(w, "warning: graph plan %s has unknown field(s): %v (silently dropped — see 'bd create --graph' schema)\n", loc, fields)
		for _, f := range fields {
			distinct[f] = struct{}{}
		}
	}

	hintFields := make([]string, 0, len(distinct))
	for f := range distinct {
		hintFields = append(hintFields, f)
	}
	sort.Strings(hintFields)
	for _, f := range hintFields {
		// Lowercase to match unknownKeys, which reports the original-case
		// spelling against the all-lowercase hint map.
		if hint, ok := graphFieldHints[strings.ToLower(f)]; ok {
			fmt.Fprintf(w, "  hint: %q is not part of the schema; %s\n", f, hint)
		}
	}

	return hintFields
}

func loadEmbeddedCustomTypes() []string {
	if store != nil {
		if ct, err := store.GetCustomTypes(rootCtx); err == nil && len(ct) > 0 {
			return ct
		}
	}
	return config.GetCustomTypesFromYAML()
}

// loadEmbeddedCustomStatuses reads custom statuses from the store only — no
// YAML fallback, matching single-issue create and loadListFilterConfig
// (custom types fall back to YAML; custom statuses deliberately do not).
func loadEmbeddedCustomStatuses() []string {
	if store == nil {
		return nil
	}
	cs, err := store.GetCustomStatuses(rootCtx)
	if err != nil {
		return nil
	}
	return cs
}

// createIssuesFromGraph handles `bd create --graph <plan-file>`.
// When dryRun is true, the plan is parsed and validated but no writes occur;
// a preview is emitted to stdout (JSON when jsonOutput is set, otherwise
// human-readable). Unknown plan/node/edge fields are reported to stderr in
// both modes so schema gaps are visible before any writes happen. (GH#3367)
func createIssuesFromGraph(planFile string, dryRun bool, opts GraphApplyOptions) error {
	data, err := os.ReadFile(planFile) // #nosec G304 -- user-provided path is intentional
	if err != nil {
		return HandleErrorRespectJSON("reading graph plan: %v", err)
	}

	if unknown := detectUnknownGraphFields(data); len(unknown) > 0 {
		warnUnknownGraphFields(os.Stderr, unknown)
	}

	var plan GraphApplyPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return HandleErrorRespectJSON("parsing graph plan: %v", err)
	}

	dbPrefix, allowedPrefixes := loadEmbeddedIDPrefixes()
	cfg := graphPlanConfig{
		customTypes:     loadEmbeddedCustomTypes(),
		customStatuses:  loadEmbeddedCustomStatuses(),
		dbPrefix:        dbPrefix,
		allowedPrefixes: allowedPrefixes,
	}
	if store != nil {
		cfg.issueExists = func(id string) (bool, error) {
			if _, err := store.GetIssue(rootCtx, id); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}
	}
	if _, err := validateFullGraphPlan(&plan, cfg, opts, false); err != nil {
		return HandleErrorRespectJSON("invalid graph plan: %v", err)
	}

	if dryRun {
		return emitGraphApplyDryRun(&plan, opts)
	}

	result, err := executeGraphApply(rootCtx, &plan, opts)
	if err != nil {
		return HandleErrorRespectJSON("graph create: %v", err)
	}

	if jsonOutput {
		return outputJSON(result)
	}
	fmt.Printf("Created %d issues\n", len(result.IDs))
	keys := make([]string, 0, len(result.IDs))
	for key := range result.IDs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("  %s -> %s\n", key, result.IDs[key])
	}
	return nil
}

// emitGraphApplyDryRun prints what `bd create --graph` would do without
// performing any writes. Mirrors the JSON-vs-human split of the live path.
// (GH#3367)
func emitGraphApplyDryRun(plan *GraphApplyPlan, opts GraphApplyOptions) error {
	parentDeps := 0
	rows := make([]GraphApplyDryRunRow, 0, len(plan.Nodes))
	for _, node := range plan.Nodes {
		// Preview via the same materialization the apply path uses so
		// type/priority/status defaults can't drift. (A defer_until passing
		// between dry-run and apply still shifts the real status to open.)
		issue, err := graphApplyNodeIssue(node, opts, "", "")
		if err != nil {
			return HandleErrorRespectJSON("invalid graph plan: %v", err)
		}
		effectiveParentKey := node.effectiveParentKey()
		status := string(issue.Status)
		if node.Status == "" && issue.Status == types.StatusOpen {
			status = "" // default status: keep the preview row terse
		}
		if effectiveParentKey != "" || node.ParentID != "" {
			parentDeps++
		}
		rows = append(rows, GraphApplyDryRunRow{
			Key:       node.Key,
			ID:        node.ID,
			Title:     node.Title,
			Type:      string(issue.IssueType),
			Status:    status,
			Priority:  issue.Priority,
			ParentKey: effectiveParentKey,
			ParentID:  node.ParentID,
		})
	}

	preview := GraphApplyDryRun{
		DryRun:          true,
		NodeCount:       len(plan.Nodes),
		EdgeCount:       len(plan.Edges),
		ParentDeps:      parentDeps,
		ValidationNotes: []string{graphApplyDryRunTransactionValidationNote},
		Nodes:           rows,
	}

	if jsonOutput {
		return outputJSON(preview)
	}

	fmt.Printf("Dry run: would create %d issue(s) and %d edge(s) (%d parent-child link(s))\n",
		preview.NodeCount, preview.EdgeCount, preview.ParentDeps)
	fmt.Printf("Note: %s.\n", graphApplyDryRunTransactionValidationNote)
	for _, row := range rows {
		extras := ""
		if row.ID != "" {
			extras += fmt.Sprintf(" id=%s", row.ID)
		}
		if row.Status != "" {
			extras += fmt.Sprintf(" status=%s", row.Status)
		}
		switch {
		case row.ParentKey != "":
			extras += fmt.Sprintf(" parent_key=%s", row.ParentKey)
		case row.ParentID != "":
			extras += fmt.Sprintf(" parent_id=%s", row.ParentID)
		}
		fmt.Printf("  %s [%s] P%d %q%s\n", row.Key, row.Type, row.Priority, row.Title, extras)
	}
	return nil
}

// graphPlanConfig bundles the config inputs full-plan validation needs; the
// embedded and proxied paths populate it from their respective config sources.
type graphPlanConfig struct {
	customTypes     []string
	customStatuses  []string
	dbPrefix        string
	allowedPrefixes string
	// issueExists probes whether an ID is already taken by a stored issue (or
	// wisp, where the underlying lookup spans both tables). nil skips the
	// explicit-ID collision preflight (no store access in the calling context).
	issueExists func(id string) (bool, error)
}

// validateFullGraphPlan runs every plan-level check in one place so the
// embedded and proxied paths cannot skip a step independently: shared plan
// checks, storage-class resolution (uniform when requireUniform), and
// explicit-ID prefix checks. The returned useWisp is node 0's storage class —
// under requireUniform, the routing decision for the whole plan.
func validateFullGraphPlan(plan *GraphApplyPlan, cfg graphPlanConfig, opts GraphApplyOptions, requireUniform bool) (useWisp bool, err error) {
	if err := validateGraphApplyPlan(plan, cfg.customTypes, cfg.customStatuses, opts); err != nil {
		return false, err
	}
	useWisp, err = validateGraphApplyStorageClasses(plan, opts, requireUniform)
	if err != nil {
		return false, err
	}
	if err := validateGraphApplyExplicitIDPrefixes(plan, cfg.dbPrefix, cfg.allowedPrefixes, opts.Force); err != nil {
		return false, err
	}
	return useWisp, validateGraphApplyExplicitIDCollisions(plan, cfg.issueExists)
}

// validateGraphApplyExplicitIDCollisions rejects plan nodes whose explicit id
// already belongs to a stored issue or wisp. The insert path upserts on a
// duplicate ID (matching `bd create --id`), which for an atomic graph create
// would silently rewrite the existing issue while reporting creation — a plan
// must fail fast instead. Deliberately not overridable by --force: that flag
// vouches for a foreign prefix, not for destroying existing data. Best-effort
// by transport: exists is nil where the calling context has no store access.
func validateGraphApplyExplicitIDCollisions(plan *GraphApplyPlan, exists func(id string) (bool, error)) error {
	if exists == nil {
		return nil
	}
	for _, node := range plan.Nodes {
		if node.ID == "" {
			continue
		}
		taken, err := exists(node.ID)
		if err != nil {
			return fmt.Errorf("checking explicit id %q (node %q): %w", node.ID, node.Key, err)
		}
		if taken {
			return fmt.Errorf("explicit id %q (node %q) already exists; graph create will not overwrite an existing issue — drop the id to mint a new one, or update the existing issue instead", node.ID, node.Key)
		}
	}
	return nil
}

func validateGraphApplyPlan(plan *GraphApplyPlan, customTypes, customStatuses []string, opts GraphApplyOptions) error {
	if len(plan.Nodes) == 0 {
		return fmt.Errorf("plan has no nodes")
	}

	seenKeys := make(map[string]bool, len(plan.Nodes))
	seenIDs := make(map[string]bool)
	for i, node := range plan.Nodes {
		if node.Key == "" {
			return fmt.Errorf("node %d has empty key", i)
		}
		if seenKeys[node.Key] {
			return fmt.Errorf("duplicate node key %q", node.Key)
		}
		seenKeys[node.Key] = true
		if node.Title == "" {
			return fmt.Errorf("node %q has empty title", node.Key)
		}
		if err := validateGraphApplyNodeFields(node, customTypes, customStatuses, opts); err != nil {
			return err
		}
		if node.ID != "" {
			if seenIDs[node.ID] {
				return fmt.Errorf("duplicate explicit id %q (node %q)", node.ID, node.Key)
			}
			seenIDs[node.ID] = true
		}
		// Validate MetadataRefs point to known keys.
		for metaKey, refKey := range node.MetadataRefs {
			if !seenKeys[refKey] {
				found := false
				for _, other := range plan.Nodes {
					if other.Key == refKey {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("node %q: metadata ref %q references unknown key %q", node.Key, metaKey, refKey)
				}
			}
		}
		parentKey := node.effectiveParentKey()
		if parentKey != "" && !seenKeys[parentKey] {
			found := false
			for _, other := range plan.Nodes {
				if other.Key == parentKey {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("node %q: parent key %q not found in plan", node.Key, parentKey)
			}
		}
		for j, dep := range node.Deps {
			if dep.Target == "" {
				return fmt.Errorf("node %q: dep %d has empty target", node.Key, j)
			}
			if dep.Type != "" {
				dt := types.DependencyType(dep.Type)
				if !dt.IsValid() {
					return fmt.Errorf("node %q: dep %d: invalid dependency type %q", node.Key, j, dep.Type)
				}
			}
		}
	}

	for i, edge := range plan.Edges {
		if edge.FromKey != "" && !seenKeys[edge.FromKey] {
			return fmt.Errorf("edge %d: from key %q not found in plan", i, edge.FromKey)
		}
		if edge.ToKey != "" && !seenKeys[edge.ToKey] {
			return fmt.Errorf("edge %d: to key %q not found in plan", i, edge.ToKey)
		}
		if edge.FromKey == "" && edge.FromID == "" {
			return fmt.Errorf("edge %d: must specify from_key or from_id", i)
		}
		if edge.ToKey == "" && edge.ToID == "" {
			return fmt.Errorf("edge %d: must specify to_key or to_id", i)
		}
		if edge.Type != "" {
			dt := types.DependencyType(edge.Type)
			if !dt.IsValid() {
				return fmt.Errorf("edge %d: invalid dependency type %q", i, edge.Type)
			}
		}
		if edge.Gate != "" || edge.SpawnerKey != "" || edge.SpawnerID != "" {
			if graphApplyDependencyType(edge.Type) != types.DepWaitsFor {
				return fmt.Errorf("edge %d: gate/spawner fields require type %q", i, types.DepWaitsFor)
			}
			if edge.Gate != "" && !types.IsValidWaitsForGate(edge.Gate) {
				return fmt.Errorf("edge %d: invalid gate %q (valid: %s, %s)", i, edge.Gate, types.WaitsForAllChildren, types.WaitsForAnyChildren)
			}
			if edge.SpawnerKey != "" && edge.SpawnerID != "" {
				return fmt.Errorf("edge %d: cannot specify both spawner_key and spawner_id", i)
			}
			if edge.SpawnerKey != "" && !seenKeys[edge.SpawnerKey] {
				return fmt.Errorf("edge %d: spawner key %q not found in plan", i, edge.SpawnerKey)
			}
			// Gate evaluation reads the spawner from the dependency target
			// (depends_on_id), not metadata, so the spawner must equal the to
			// endpoint. Since to_id overrides to_key at apply time
			// (resolveEdgeRef), a key-named spawner can't be combined with to_id.
			if edge.SpawnerKey != "" && edge.ToID != "" {
				return fmt.Errorf("edge %d: spawner_key %q cannot be combined with to_id %q (to_id overrides to_key as the waits-for target; use spawner_id)", i, edge.SpawnerKey, edge.ToID)
			}
			if edge.SpawnerKey != "" && edge.SpawnerKey != edge.ToKey {
				return fmt.Errorf("edge %d: spawner_key %q must match to_key %q (the waits-for target is the spawner)", i, edge.SpawnerKey, edge.ToKey)
			}
			if edge.SpawnerID != "" && edge.SpawnerID != edge.ToID {
				return fmt.Errorf("edge %d: spawner_id %q must match to_id %q (the waits-for target is the spawner)", i, edge.SpawnerID, edge.ToID)
			}
		}
	}

	if err := validateGraphApplyLocalCycles(plan, seenKeys); err != nil {
		return err
	}

	return nil
}

// validateGraphApplyNodeFields checks the single-node fields added for
// bd-create parity, mirroring the flag-shape checks `bd create` applies
// (config-gated template linting is not run on graph plans).
func validateGraphApplyNodeFields(node GraphApplyNode, customTypes, customStatuses []string, opts GraphApplyOptions) error {
	if node.ID != "" {
		if _, err := validation.ValidateIDFormat(node.ID); err != nil {
			return fmt.Errorf("node %q: %w", node.Key, err)
		}
	}
	// Friendlier status message than the issue-model validator's below.
	if node.Status != "" && !types.Status(node.Status).IsValidWithCustom(customStatuses) {
		return fmt.Errorf("node %q: invalid status %q (valid: %s; configure custom statuses via 'bd config set status.custom')", node.Key, node.Status, validStatusList(customStatuses))
	}
	if node.WispType != "" && !types.WispType(node.WispType).IsValid() {
		return fmt.Errorf("node %q: invalid wisp_type %q (must be %s)", node.Key, node.WispType, types.ValidWispTypeNames())
	}
	if node.MolType != "" && !types.MolType(node.MolType).IsValid() {
		return fmt.Errorf("node %q: invalid mol_type %q (must be %s)", node.Key, node.MolType, types.ValidMolTypeNames())
	}
	if (node.EventKind != "" || node.Actor != "" || node.Target != "" || node.Payload != "") && node.Type != string(types.TypeEvent) {
		return fmt.Errorf("node %q: event_kind, actor, target, and payload require type %q", node.Key, types.TypeEvent)
	}
	// Issue-model rules (type validity, priority range, estimate sign,
	// metadata JSON, ...) run against the same materialized issue the apply
	// path stores, so plan-time and insert-time validation can't drift.
	issue, err := graphApplyNodeIssue(node, opts, "", "")
	if err != nil {
		return err
	}
	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("node %q: %w", node.Key, err)
	}
	return nil
}

// validateGraphApplyStorageClasses resolves each node's effective storage class
// (per-node overrides combined with the CLI flags) so conflicts surface at
// validation time instead of mid-apply. When requireUniform is set (proxied
// mode, which routes the whole plan to one table), mixed durable/wisp plans are
// also rejected. The returned useWisp is node 0's class — under requireUniform,
// the routing decision for the whole plan.
func validateGraphApplyStorageClasses(plan *GraphApplyPlan, opts GraphApplyOptions, requireUniform bool) (useWisp bool, err error) {
	for i, node := range plan.Nodes {
		ephemeral, noHistory, err := graphApplyNodeStorageClass(node, opts)
		if err != nil {
			return false, err
		}
		nodeWisp := ephemeral || noHistory
		if i == 0 {
			useWisp = nodeWisp
		} else if requireUniform && nodeWisp != useWisp {
			return false, fmt.Errorf("plan mixes durable and wisp storage classes (node %q vs node %q): effective ephemeral/no_history must be uniform across the plan in proxied-server mode", plan.Nodes[0].Key, node.Key)
		}
	}
	return useWisp, nil
}

// validateGraphApplyExplicitIDPrefixes mirrors the `bd create --id` prefix
// check for every plan node that pins an explicit ID: the ID must start with
// the database prefix (or one of allowed_prefixes) unless force is set.
func validateGraphApplyExplicitIDPrefixes(plan *GraphApplyPlan, dbPrefix, allowedPrefixes string, force bool) error {
	for _, node := range plan.Nodes {
		if node.ID == "" {
			continue
		}
		if err := validation.ValidateIDPrefixAllowed(node.ID, dbPrefix, allowedPrefixes, force); err != nil {
			return fmt.Errorf("node %q: %w", node.Key, err)
		}
	}
	return nil
}

// loadEmbeddedIDPrefixes returns the database prefix and allowed_prefixes for
// explicit-ID validation. YAML config takes precedence over DB (via
// overlayYAMLPrefix) — in shared-server mode the DB may belong to a different
// project (GH#2469) — except under --global, where the shared database's own
// stored prefix wins (GH#4957).
func loadEmbeddedIDPrefixes() (dbPrefix, allowedPrefixes string) {
	var storePrefix string
	if store != nil {
		storePrefix, _ = store.GetConfig(rootCtx, "issue_prefix")         // Best effort: empty prefix is a valid fallback
		allowedPrefixes, _ = store.GetConfig(rootCtx, "allowed_prefixes") // Best effort: empty means no prefix restriction
	}
	// Under --global the shared database's stored prefix wins over the
	// project YAML overlay (GH#4957); selectCreateIDPrefix owns that rule.
	dbPrefix = selectCreateIDPrefix(globalFlag, overlayYAMLPrefix(""), storePrefix)
	return dbPrefix, allowedPrefixes
}

func validateGraphApplyLocalCycles(plan *GraphApplyPlan, knownKeys map[string]bool) error {
	adj := make(map[string][]string)
	for _, node := range plan.Nodes {
		parentKey := node.effectiveParentKey()
		if parentKey != "" && knownKeys[node.Key] && knownKeys[parentKey] {
			// The parent key is guaranteed local by validateGraphApplyPlan, so it
			// is safe to model the implicit parent-child dependency by key here.
			adj[node.Key] = append(adj[node.Key], parentKey)
		}
	}
	for _, edge := range plan.Edges {
		depType := graphApplyDependencyType(edge.Type)
		if !graphApplyEdgeIsLocalCycleRelevant(edge, depType) {
			continue
		}
		if !knownKeys[edge.FromKey] || !knownKeys[edge.ToKey] {
			continue
		}
		adj[edge.FromKey] = append(adj[edge.FromKey], edge.ToKey)
	}

	visiting := make(map[string]bool, len(knownKeys))
	visited := make(map[string]bool, len(knownKeys))
	var visit func(string) (string, bool)
	visit = func(key string) (string, bool) {
		if visiting[key] {
			return key, true
		}
		if visited[key] {
			return "", false
		}
		visiting[key] = true
		for _, next := range adj[key] {
			if cycleKey, ok := visit(next); ok {
				return cycleKey, true
			}
		}
		visiting[key] = false
		visited[key] = true
		return "", false
	}

	for _, key := range graphApplySortedKeys(knownKeys) {
		if cycleKey, ok := visit(key); ok {
			return fmt.Errorf("graph contains a blocking dependency cycle involving node %q", cycleKey)
		}
	}
	return nil
}

// graphApplyNodeIssue materializes a plan node into an issue via the same
// createIssueParams path used by single-issue `bd create`, so every field the
// CLI can set stays addressable from graph plans. Assignee handling is left
// to the caller (embedded and proxied paths defer assignment differently).
func graphApplyNodeIssue(node GraphApplyNode, opts GraphApplyOptions, createdBy, owner string) (*types.Issue, error) {
	issueType := types.IssueType(node.Type)
	if issueType == "" {
		issueType = types.TypeTask
	}

	var metadataJSON json.RawMessage
	if len(node.Metadata) > 0 {
		raw, err := json.Marshal(node.Metadata)
		if err != nil {
			return nil, fmt.Errorf("node %q: marshaling metadata: %w", node.Key, err)
		}
		metadataJSON = raw
	}

	priority := 2
	if node.Priority != nil {
		priority = *node.Priority
	}

	ephemeral, noHistory, err := graphApplyNodeStorageClass(node, opts)
	if err != nil {
		return nil, err
	}

	if node.Owner != "" {
		owner = node.Owner
	}

	// Resolve the estimate alias here so every materialization path (embedded,
	// proxied, dry-run, validation) sees one canonical field. The canonical
	// estimated_minutes wins when both are set, matching the parent alias —
	// but a negative alias value is still rejected rather than silently
	// discarded by the precedence (GH#4064).
	if node.Estimate != nil && *node.Estimate < 0 {
		return nil, fmt.Errorf("node %q: estimate cannot be negative", node.Key)
	}
	estimatedMinutes := node.EstimatedMinutes
	if estimatedMinutes == nil {
		estimatedMinutes = node.Estimate
	}

	issue := buildCreateIssue(createIssueParams{
		ID:                 node.ID,
		Title:              node.Title,
		Description:        node.Description,
		Design:             node.Design,
		AcceptanceCriteria: node.AcceptanceCriteria,
		Notes:              node.Notes,
		SpecID:             node.SpecID,
		Priority:           priority,
		IssueType:          issueType.Normalize(),
		ExternalRef:        node.ExternalRef,
		EstimatedMinutes:   estimatedMinutes,
		Ephemeral:          ephemeral,
		NoHistory:          noHistory,
		CreatedBy:          createdBy,
		Owner:              owner,
		Labels:             node.Labels,
		MolType:            types.MolType(node.MolType),
		WispType:           types.WispType(node.WispType),
		EventKind:          node.EventKind,
		Actor:              node.Actor,
		Target:             node.Target,
		Payload:            node.Payload,
		InitialStatus:      node.Status,
		DueAt:              node.DueAt,
		DeferUntil:         node.DeferUntil,
		Metadata:           metadataJSON,
	})
	// Backfill status-coupled timestamps: the proxied domain insert does no
	// backfill, and neither path stamps started_at for issues born in_progress.
	now := time.Now().UTC()
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		issue.ClosedAt = &now
	}
	if issue.Status == types.StatusInProgress && issue.StartedAt == nil {
		issue.StartedAt = &now
	}
	issue.Pinned = node.Pinned
	return issue, nil
}

// graphApplyNodeStorageClass resolves a node's effective storage class from
// its per-node overrides and the plan-wide CLI flags.
func graphApplyNodeStorageClass(node GraphApplyNode, opts GraphApplyOptions) (ephemeral, noHistory bool, err error) {
	ephemeral = opts.Ephemeral
	if node.Ephemeral != nil {
		ephemeral = *node.Ephemeral
	}
	noHistory = opts.NoHistory
	if node.NoHistory != nil {
		noHistory = *node.NoHistory
	}
	if ephemeral && noHistory {
		return false, false, fmt.Errorf("node %q: ephemeral and no_history are mutually exclusive", node.Key)
	}
	return ephemeral, noHistory, nil
}

func executeGraphApply(ctx context.Context, plan *GraphApplyPlan, opts GraphApplyOptions) (*GraphApplyResult, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	keyToID := make(map[string]string, len(plan.Nodes))
	owner := getOwner()

	commitMsg := plan.CommitMessage
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("bd: graph-apply %d nodes", len(plan.Nodes))
	}

	if err := store.RunInTransaction(ctx, commitMsg, func(tx storage.Transaction) error {
		issues := make([]*types.Issue, 0, len(plan.Nodes))
		pendingAssignees := make(map[int]string)

		for i, node := range plan.Nodes {
			issue, err := graphApplyNodeIssue(node, opts, actor, owner)
			if err != nil {
				return err
			}
			if node.Assignee != "" {
				if node.AssignAfterCreate {
					pendingAssignees[i] = node.Assignee
				} else {
					issue.Assignee = node.Assignee
				}
			}

			issues = append(issues, issue)
		}

		if err := tx.CreateIssues(ctx, issues, actor); err != nil {
			return fmt.Errorf("batch create: %w", err)
		}

		for i, node := range plan.Nodes {
			keyToID[node.Key] = issues[i].ID
		}

		// Resolve MetadataRefs now that all IDs are known.
		for i, node := range plan.Nodes {
			if len(node.MetadataRefs) == 0 {
				continue
			}
			metaJSON, err := types.MergeMetadataRefs(issues[i].Metadata, node.MetadataRefs, keyToID)
			if err != nil {
				return fmt.Errorf("node %q: %w", node.Key, err)
			}
			updates := map[string]interface{}{
				"metadata": metaJSON,
			}
			if err := tx.UpdateIssue(ctx, issues[i].ID, updates, actor); err != nil {
				return fmt.Errorf("node %q: updating metadata refs: %w", node.Key, err)
			}
		}

		parentDepPairs := graphApplyParentDepPairs(plan.Nodes, keyToID)
		newSchedulingEdges := make([][2]string, 0, len(plan.Nodes)+len(plan.Edges))
		if err := validateGraphApplyPlannedParentBlockingPaths(ctx, tx, plan, keyToID, parentDepPairs); err != nil {
			return err
		}
		if err := validateGraphApplyPlannedBlockingCycles(ctx, tx, plan, keyToID); err != nil {
			return err
		}
		for i, edge := range plan.Edges {
			fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
			toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
			depType := graphApplyDependencyType(edge.Type)
			if parentDepPairs[graphApplyDepPairKey(fromID, toID)] && depType != types.DepParentChild {
				return fmt.Errorf("edge %d %s->%s duplicates a parent-child relationship with dependency type %q", i, fromID, toID, depType)
			}
			if parentDepPairs[graphApplyDepPairKey(toID, fromID)] && graphApplyCycleRelevantDependencyType(depType) {
				return fmt.Errorf("edge %d %s->%s creates a blocking reverse of a parent-child relationship", i, fromID, toID)
			}
		}

		// Add node parent-child dependencies first. The explicit and inline
		// dependency sources below are also processed parent-first, so every
		// blocking edge sees the plan's full hierarchy in storage.
		for i, node := range plan.Nodes {
			parentKey := node.effectiveParentKey()
			parentID := node.ParentID
			if parentKey != "" {
				parentID = keyToID[parentKey]
			}
			if parentID != "" {
				dep := &types.Dependency{
					IssueID:     issues[i].ID,
					DependsOnID: parentID,
					Type:        types.DepParentChild,
				}
				if err := tx.AddDependency(ctx, dep, actor); err != nil {
					return fmt.Errorf("node %q: adding parent-child dep: %w", node.Key, err)
				}
				newSchedulingEdges = append(newSchedulingEdges, [2]string{dep.IssueID, dep.DependsOnID})
			}
		}

		for phase := 0; phase < 2; phase++ {
			parentPhase := phase == 0
			// Add explicit edges in stable order for this phase.
			for i, edge := range plan.Edges {
				fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
				toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
				depType := graphApplyDependencyType(edge.Type)
				if (depType == types.DepParentChild) != parentPhase {
					continue
				}
				if parentDepPairs[graphApplyDepPairKey(fromID, toID)] {
					if depType == types.DepParentChild {
						continue
					}
					return fmt.Errorf("edge %d %s->%s duplicates a parent-child relationship with dependency type %q", i, fromID, toID, depType)
				}
				if parentDepPairs[graphApplyDepPairKey(toID, fromID)] && graphApplyCycleRelevantDependencyType(depType) {
					return fmt.Errorf("edge %d %s->%s creates a blocking reverse of a parent-child relationship", i, fromID, toID)
				}
				dep, err := types.NewGraphEdgeDependency(fromID, toID, depType, edge.Gate, edge.SpawnerKey, edge.SpawnerID, edge.ThreadID, keyToID)
				if err != nil {
					return fmt.Errorf("edge %s->%s: %w", fromID, toID, err)
				}
				if err := tx.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{}); err != nil {
					return fmt.Errorf("adding edge %s->%s: %w", fromID, toID, err)
				}
				if graphApplySchedulingDependencyType(depType) {
					newSchedulingEdges = append(newSchedulingEdges, [2]string{fromID, toID})
				}
			}

			// Add per-node inline dependencies in stable order for this phase.
			for i, node := range plan.Nodes {
				for _, dep := range node.Deps {
					depType := types.DependencyType(dep.Type)
					if depType == "" {
						depType = types.DepBlocks
					}
					if (depType == types.DepParentChild) != parentPhase {
						continue
					}
					d, err := types.NewGraphNodeDependency(issues[i].ID, depType, dep.Target, keyToID)
					if err != nil {
						return fmt.Errorf("node %q: %w", node.Key, err)
					}
					if err := tx.AddDependency(ctx, d, actor); err != nil {
						return fmt.Errorf("node %q: adding dep to %q: %w", node.Key, dep.Target, err)
					}
					if graphApplySchedulingDependencyType(d.Type) {
						newSchedulingEdges = append(newSchedulingEdges, [2]string{d.IssueID, d.DependsOnID})
					}
				}
			}
		}
		if cyclePath, err := tx.CycleThroughEdges(ctx, newSchedulingEdges); err != nil {
			return fmt.Errorf("final graph cycle check: %w", err)
		} else if cyclePath != "" {
			return fmt.Errorf("graph dependency cycle would be created: %s", cyclePath)
		}

		// Apply deferred assignees.
		for i, assignee := range pendingAssignees {
			updates := map[string]interface{}{
				"assignee": assignee,
			}
			if err := tx.UpdateIssue(ctx, issues[i].ID, updates, actor); err != nil {
				return fmt.Errorf("node %q: setting assignee: %w", plan.Nodes[i].Key, err)
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return &GraphApplyResult{IDs: keyToID}, nil
}

// validateGraphApplyPlannedBlockingCycles rejects planned blocking edges that
// would close a blocking-dependency cycle, evaluated whole-graph before any
// insert. This early preflight is restricted to blocking edges for precise
// plan errors. Each stored edge still runs issueops.CheckDependencyCycleInTx,
// which enforces the combined blocks + conditional-blocks + parent-child graph.
func validateGraphApplyPlannedBlockingCycles(ctx context.Context, tx storage.Transaction, plan *GraphApplyPlan, keyToID map[string]string) error {
	type plannedEdge struct {
		index  int
		fromID string
		toID   string
	}

	adj := make(map[string][]string)
	checks := make([]plannedEdge, 0, len(plan.Edges))
	for i, edge := range plan.Edges {
		depType := graphApplyDependencyType(edge.Type)
		if !graphApplyCycleRelevantDependencyType(depType) {
			continue
		}
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if fromID == "" || toID == "" {
			continue
		}
		if fromID == toID {
			return fmt.Errorf("edge %d %s->%s creates a blocking dependency cycle", i, fromID, toID)
		}
		adj[fromID] = append(adj[fromID], toID)
		checks = append(checks, plannedEdge{index: i, fromID: fromID, toID: toID})
	}

	depCache := make(map[string][]*types.Dependency)
	for _, edge := range checks {
		hasPath, err := graphApplyHasPath(ctx, tx, adj, depCache, edge.toID, edge.fromID, graphApplyCycleRelevantDependencyType)
		if err != nil {
			return fmt.Errorf("edge %d %s->%s: checking planned blocking cycle: %w", edge.index, edge.fromID, edge.toID, err)
		}
		if hasPath {
			return fmt.Errorf("edge %d %s->%s creates a blocking dependency cycle", edge.index, edge.fromID, edge.toID)
		}
	}
	return nil
}

// validateGraphApplyPlannedParentBlockingPaths rejects plans where a planned
// blocking edge would create a path from a parent to its child. Unlike
// validateGraphApplyPlannedBlockingCycles, its existing-dep walk follows the
// full AffectsReadyWork set (blocks, conditional-blocks, parent-child,
// waits-for) because a parent→child path closed through any ready-affecting
// dependency is a real ready-work deadlock. The two predicates must stay
// distinct: narrowing this one would miss real deadlocks, while this broader
// walk may additionally reject a return path through waits-for.
func validateGraphApplyPlannedParentBlockingPaths(ctx context.Context, tx storage.Transaction, plan *GraphApplyPlan, keyToID map[string]string, parentDepPairs map[string]bool) error {
	adj := make(map[string][]string)
	for pair := range parentDepPairs {
		fromID, toID, ok := graphApplyDepPairIDs(pair)
		if ok {
			adj[fromID] = append(adj[fromID], toID)
		}
	}
	for _, edge := range plan.Edges {
		depType := graphApplyDependencyType(edge.Type)
		if !graphApplyReadyPathDependencyType(depType) {
			continue
		}
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if fromID == "" || toID == "" {
			continue
		}
		// Direct parent -> child blocking edges have a dedicated error below.
		// This prewrite pass covers transitive parent -> ... -> child paths.
		if graphApplyCycleRelevantDependencyType(depType) && parentDepPairs[graphApplyDepPairKey(toID, fromID)] {
			continue
		}
		adj[fromID] = append(adj[fromID], toID)
	}

	depCache := make(map[string][]*types.Dependency)
	for _, node := range plan.Nodes {
		childID := keyToID[node.Key]
		parentID := node.ParentID
		if parentKey := node.effectiveParentKey(); parentKey != "" {
			parentID = keyToID[parentKey]
		}
		if childID == "" || parentID == "" {
			continue
		}
		hasPath, err := graphApplyHasPath(ctx, tx, adj, depCache, parentID, childID, graphApplyReadyPathDependencyType)
		if err != nil {
			return err
		}
		if hasPath {
			return fmt.Errorf("node %q: planned blocking dependencies create a path from parent %q to child %q", node.Key, parentID, childID)
		}
	}
	return nil
}

// graphApplyHasPath reports whether fromID can reach toID by following the
// in-memory planned adjacency plus existing store dependencies. followExistingDep
// selects which existing dep types the walk traverses, letting callers mirror
// either the early blocking-only preflight or the broader ready-work graph.
func graphApplyHasPath(ctx context.Context, tx storage.Transaction, adj map[string][]string, depCache map[string][]*types.Dependency, fromID, toID string, followExistingDep func(types.DependencyType) bool) (bool, error) {
	seen := make(map[string]bool)
	var visit func(string) (bool, error)
	visit = func(id string) (bool, error) {
		if id == toID {
			return true, nil
		}
		if seen[id] {
			return false, nil
		}
		seen[id] = true
		for _, next := range adj[id] {
			found, err := visit(next)
			if err != nil || found {
				return found, err
			}
		}
		deps, ok := depCache[id]
		if !ok {
			var err error
			deps, err = tx.GetDependencyRecords(ctx, id)
			if err != nil {
				return false, fmt.Errorf("reading existing dependencies for %s: %w", id, err)
			}
			depCache[id] = deps
		}
		for _, dep := range deps {
			if !followExistingDep(dep.Type) {
				continue
			}
			found, err := visit(dep.DependsOnID)
			if err != nil || found {
				return found, err
			}
		}
		return false, nil
	}
	return visit(fromID)
}

// graphApplyEdgeIsLocalCycleRelevant reports whether an edge participates in the
// in-memory local cycle check run by validateGraphApplyLocalCycles: it must be a
// fully-local edge (both endpoints addressed by key, neither by an existing ID)
// of a cycle-relevant dependency type.
func graphApplyEdgeIsLocalCycleRelevant(edge GraphApplyEdge, depType types.DependencyType) bool {
	if edge.FromKey == "" || edge.ToKey == "" || edge.FromID != "" || edge.ToID != "" {
		return false
	}
	return graphApplyCycleRelevantDependencyType(depType)
}

func graphApplyDependencyType(depType string) types.DependencyType {
	if depType == "" {
		return types.DepBlocks
	}
	return types.DependencyType(depType)
}

func graphApplyCycleRelevantDependencyType(depType types.DependencyType) bool {
	return depType == types.DepBlocks || depType == types.DepConditionalBlocks
}

func graphApplySchedulingDependencyType(depType types.DependencyType) bool {
	return graphApplyCycleRelevantDependencyType(depType) || depType == types.DepParentChild
}

func graphApplyReadyPathDependencyType(depType types.DependencyType) bool {
	return depType.AffectsReadyWork()
}

func graphApplySortedKeys(keys map[string]bool) []string {
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func graphApplyParentDepPairs(nodes []GraphApplyNode, keyToID map[string]string) map[string]bool {
	pairs := make(map[string]bool)
	for _, node := range nodes {
		parentID := node.ParentID
		if parentKey := node.effectiveParentKey(); parentKey != "" {
			parentID = keyToID[parentKey]
		}
		childID := keyToID[node.Key]
		if childID != "" && parentID != "" {
			pairs[graphApplyDepPairKey(childID, parentID)] = true
		}
	}
	return pairs
}

func graphApplyDepPairKey(issueID, dependsOnID string) string {
	return issueID + "\x00" + dependsOnID
}

func graphApplyDepPairIDs(pair string) (string, string, bool) {
	for i := 0; i < len(pair); i++ {
		if pair[i] == 0 {
			return pair[:i], pair[i+1:], true
		}
	}
	return "", "", false
}

func resolveEdgeRef(key, id string, keyToID map[string]string) string {
	if id != "" {
		return id
	}
	if key != "" {
		return keyToID[key]
	}
	return ""
}
