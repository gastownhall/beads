package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// configReader is the minimal slice of storage.Storage that config-reading
// helpers depend on, letting tests inject a fake without spinning up a Dolt
// server. Transaction-bound writers (storeMolWriter) satisfy it with reads
// that see in-transaction config writes.
type configReader interface {
	GetConfig(ctx context.Context, key string) (string, error)
}

// BeadsTemplateLabel is the label used to identify Beads-based templates
const BeadsTemplateLabel = "template"

// variablePattern matches {{variable}} placeholders
var variablePattern = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// TemplateSubgraph holds a template epic and all its descendants
type TemplateSubgraph struct {
	Root         *types.Issue              // The template epic
	Issues       []*types.Issue            // All issues in the subgraph (including root)
	Dependencies []*types.Dependency       // All dependencies within the subgraph
	IssueMap     map[string]*types.Issue   // ID -> Issue for quick lookup
	VarDefs      map[string]formula.VarDef // Variable definitions from formula (for defaults)
	Phase        string                    // Recommended phase: "liquid" (pour) or "vapor" (wisp)
	Pour         bool                      // If true, steps should be materialized as sub-issues (from formula pour=true)
}

// InstantiateResult holds the result of template instantiation
type InstantiateResult struct {
	NewEpicID string            `json:"new_epic_id"`
	IDMapping map[string]string `json:"id_mapping"` // old ID -> new ID
	Created   int               `json:"created"`    // number of issues created
}

// CloneOptions controls how the subgraph is cloned during spawn/bond
type CloneOptions struct {
	Vars      map[string]string // Variable substitutions for {{key}} placeholders
	Assignee  string            // Assign the root epic to this agent/user
	Actor     string            // Actor performing the operation
	Ephemeral bool              // If true, spawned issues are marked for bulk deletion
	Prefix    string            // Override prefix for ID generation (bd-hobo: distinct prefixes)

	// Dynamic bonding fields (for Christmas Ornament pattern)
	ParentID string // Parent molecule ID to bond under (e.g., "patrol-x7k")
	ChildRef string // Child reference with variables (e.g., "arm-{{polecat_name}}")

	// Atomic attachment: if set, adds a dependency from the spawned root to
	// AttachToID within the same transaction as the clone, preventing orphans.
	AttachToID    string               // Molecule ID to attach spawned root to
	AttachDepType types.DependencyType // Dependency type for the attachment

	// RootOnly: if true, only create the root issue (no child step issues).
	// Used by patrol wisps where steps are inlined at prime time, not tracked as beads.
	RootOnly bool
}

// bondedIDPattern validates bonded IDs (alphanumeric, dash, underscore, dot)
var bondedIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// =============================================================================
// Beads Template Functions
// =============================================================================

// loadTemplateSubgraph loads a template epic and all its descendants
func loadTemplateSubgraph(ctx context.Context, s molReader, templateID string) (*TemplateSubgraph, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Get the root issue
	root, err := s.GetIssue(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("template %s not found", templateID)
	}

	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	// Recursively load all children (with cycle detection, GH#2719)
	visited := map[string]bool{root.ID: true}
	if err := loadDescendants(ctx, s, subgraph, root.ID, visited); err != nil {
		return nil, err
	}

	// Load all dependencies within the subgraph
	for _, issue := range subgraph.Issues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get dependencies for %s: %w", issue.ID, err)
		}
		for _, dep := range deps {
			// Only include dependencies where both ends are in the subgraph
			if _, ok := subgraph.IssueMap[dep.DependsOnID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, dep)
			}
		}
	}

	return subgraph, nil
}

// loadDescendants recursively loads all child issues.
// It uses two strategies to find children:
// 1. Check dependency records for parent-child relationships
// 2. Check for hierarchical IDs (parent.N) to catch children with missing/wrong deps
//
// The visited set tracks IDs already expanded to detect cycles (GH#2719).
// Without this, cyclic parent-child dependencies cause unbounded recursion leading to OOM.
func loadDescendants(ctx context.Context, s molReader, subgraph *TemplateSubgraph, parentID string, visited map[string]bool) error {
	// Track children we've already added to avoid duplicates
	addedChildren := make(map[string]bool)

	// Strategy 1: Get direct parent-child dependents with relationship metadata.
	dependents, err := s.GetDependentsWithMetadata(ctx, parentID)
	if err != nil {
		return fmt.Errorf("failed to get dependents of %s: %w", parentID, err)
	}

	// Only keep explicit parent-child relationships.
	for _, dependent := range dependents {
		if dependent.DependencyType != types.DepParentChild {
			continue
		}

		if _, exists := subgraph.IssueMap[dependent.ID]; exists {
			continue // Already in subgraph
		}

		// Cycle detection (GH#2719)
		if visited[dependent.ID] {
			continue
		}

		child := dependent.Issue

		// Add to subgraph
		subgraph.Issues = append(subgraph.Issues, &child)
		subgraph.IssueMap[child.ID] = &child
		addedChildren[child.ID] = true

		// Mark visited before recursing
		visited[child.ID] = true
		if err := loadDescendants(ctx, s, subgraph, child.ID, visited); err != nil {
			return err
		}
	}

	// Strategy 2: Find hierarchical children by ID pattern
	// This catches children that have missing or incorrect dependency types.
	// Hierarchical IDs follow the pattern: parentID.N (e.g., "gt-abc.1", "gt-abc.2")
	hierarchicalChildren, err := findHierarchicalChildren(ctx, s, parentID)
	if err != nil {
		// Non-fatal: continue with what we have
		return nil
	}

	for _, child := range hierarchicalChildren {
		if addedChildren[child.ID] {
			continue // Already added via dependency
		}
		if _, exists := subgraph.IssueMap[child.ID]; exists {
			continue // Already in subgraph
		}

		// Cycle detection (GH#2719)
		if visited[child.ID] {
			continue
		}

		// Check if this hierarchical child has been reparented to a different parent (GH#2476).
		// If it has an explicit parent-child dependency pointing elsewhere, skip it —
		// the ID pattern match is stale and the child belongs to another molecule.
		depRecs, err := s.GetDependencyRecords(ctx, child.ID)
		if err == nil {
			reparented := false
			for _, dep := range depRecs {
				if dep.Type == types.DepParentChild && dep.DependsOnID != parentID {
					reparented = true
					break
				}
			}
			if reparented {
				continue
			}
		}

		// Add to subgraph
		subgraph.Issues = append(subgraph.Issues, child)
		subgraph.IssueMap[child.ID] = child
		addedChildren[child.ID] = true

		// Mark visited before recursing
		visited[child.ID] = true
		if err := loadDescendants(ctx, s, subgraph, child.ID, visited); err != nil {
			return err
		}
	}

	return nil
}

// findHierarchicalChildren finds issues with IDs that match the pattern parentID.N
// This catches hierarchical children that may be missing parent-child dependencies.
func findHierarchicalChildren(ctx context.Context, s molReader, parentID string) ([]*types.Issue, error) {
	pattern := parentID + "."
	candidates, err := s.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: pattern})
	if err != nil {
		return nil, err
	}

	var children []*types.Issue
	for _, issue := range candidates {
		_, directParentID, depth := types.ParseHierarchicalID(issue.ID)
		if depth > 0 && directParentID == parentID {
			children = append(children, issue)
		}
	}

	return children, nil
}

// =============================================================================
// Proto Lookup Functions
// =============================================================================

// resolveProtoIDOrTitle resolves a proto by ID or title.
// It first tries to resolve as an ID (via ResolvePartialID).
// If that fails, it searches for protos with matching titles.
// Returns the proto ID if found, or an error if not found or ambiguous.
func resolveProtoIDOrTitle(ctx context.Context, s molReader, input string) (string, error) {
	// Strategy 1: Try to resolve as an ID
	protoID, err := utils.ResolvePartialID(ctx, s, input)
	if err == nil {
		// Verify it's a proto (has template label)
		issue, getErr := s.GetIssue(ctx, protoID)
		if getErr == nil && issue != nil {
			labels, _ := s.GetLabels(ctx, protoID)
			for _, label := range labels {
				if label == BeadsTemplateLabel {
					return protoID, nil // Found a valid proto by ID
				}
			}
		}
		// ID resolved but not a proto - continue to title search
	}

	// Strategy 2: Search for protos by title
	protos, err := s.GetIssuesByLabel(ctx, BeadsTemplateLabel)
	if err != nil {
		return "", fmt.Errorf("failed to search protos: %w", err)
	}

	var matches []*types.Issue
	var exactMatch *types.Issue

	for _, proto := range protos {
		// Check for exact title match (case-insensitive)
		if strings.EqualFold(proto.Title, input) {
			exactMatch = proto
			break
		}
		// Check for partial title match (case-insensitive)
		if strings.Contains(strings.ToLower(proto.Title), strings.ToLower(input)) {
			matches = append(matches, proto)
		}
	}

	if exactMatch != nil {
		return exactMatch.ID, nil
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no proto found matching %q (by ID or title)", input)
	}

	if len(matches) == 1 {
		return matches[0].ID, nil
	}

	// Multiple matches - show them all for disambiguation
	var matchNames []string
	for _, m := range matches {
		matchNames = append(matchNames, fmt.Sprintf("%s: %s", m.ID, m.Title))
	}
	return "", fmt.Errorf("ambiguous: %q matches %d protos:\n  %s\nUse the ID or a more specific title", input, len(matches), strings.Join(matchNames, "\n  "))
}

// extractVariables finds all {{variable}} patterns in text.
// Handlebars control keywords like "else", "this" are excluded.
func extractVariables(text string) []string {
	matches := variablePattern.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var vars []string
	for _, match := range matches {
		if len(match) >= 2 && !seen[match[1]] {
			name := match[1]
			// Skip Handlebars control keywords
			if isHandlebarsKeyword(name) {
				continue
			}
			vars = append(vars, name)
			seen[name] = true
		}
	}
	return vars
}

// isHandlebarsKeyword returns true for Handlebars control keywords
// that look like variables but aren't (e.g., "else", "this").
func isHandlebarsKeyword(name string) bool {
	switch name {
	case "else", "this", "root", "index", "key", "first", "last":
		return true
	default:
		return false
	}
}

// extractAllVariables finds all variables across the entire subgraph.
//
// The fields scanned here must stay in sync with the fields cloneSubgraphInto
// substitutes - a var that pour resolves but never demands leaves a silent
// literal placeholder in the poured bead, and a var it demands but never
// resolves is a closed loop that fails the pour for nothing (GH#5110,
// GH#5754).
func extractAllVariables(subgraph *TemplateSubgraph) []string {
	var sb strings.Builder
	write := func(parts ...string) {
		for _, p := range parts {
			if p == "" {
				continue
			}
			sb.WriteString(p)
			sb.WriteByte(' ')
		}
	}
	for _, issue := range subgraph.Issues {
		write(issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes)
		write(issue.Assignee, issue.AwaitID)
		write(issue.Labels...)
		write(metadataVarStrings(issue.Metadata)...)
	}
	return extractVariables(sb.String())
}

// extractRequiredVariables returns only variables that don't have defaults.
// If VarDefs is available (from a cooked formula), uses it to filter out defaulted vars.
// Otherwise, falls back to returning all variables.
func extractRequiredVariables(subgraph *TemplateSubgraph) []string {
	allVars := extractAllVariables(subgraph)

	// If no VarDefs, assume all variables are required (legacy template behavior)
	if subgraph.VarDefs == nil {
		return allVars
	}

	// VarDefs exists (from a cooked formula) - only declared variables matter.
	// Variables in text but NOT in VarDefs are ignored - they're documentation
	// handlebars meant for LLM agents, not formula input variables (gt-ky9loa).
	var required []string
	for _, v := range allVars {
		def, exists := subgraph.VarDefs[v]
		if !exists {
			// Not a declared formula variable - skip (documentation handlebars)
			continue
		}
		// A declared variable is required if it has no default.
		// nil Default = no default specified (must provide).
		// Non-nil Default (including &"") = has explicit default (optional).
		if def.Default == nil {
			required = append(required, v)
		}
	}
	return required
}

// applyVariableDefaults merges formula default values with provided variables.
// Returns a new map with defaults applied for any missing variables.
func applyVariableDefaults(vars map[string]string, subgraph *TemplateSubgraph) map[string]string {
	if subgraph.VarDefs == nil {
		return vars
	}

	result := make(map[string]string)
	for k, v := range vars {
		result[k] = v
	}

	// Apply defaults for missing variables (including empty-string defaults)
	for name, def := range subgraph.VarDefs {
		if _, exists := result[name]; !exists && def.Default != nil {
			result[name] = *def.Default
		}
	}

	return result
}

// substituteVariables replaces {{variable}} with values
func substituteVariables(text string, vars map[string]string) string {
	return variablePattern.ReplaceAllStringFunc(text, func(match string) string {
		// Extract variable name from {{name}}
		name := match[2 : len(match)-2]
		if val, ok := vars[name]; ok {
			return val
		}
		return match // Leave unchanged if not found
	})
}

// maxMetadataSubstitutionDepth bounds the recursion in substituteJSONVars.
// Metadata is arbitrary JSON that can arrive from an untrusted proto, and a
// deeply nested value must not blow the stack.
const maxMetadataSubstitutionDepth = 32

// substituteMetadataVars substitutes {{variable}} placeholders in every string
// value of an issue's metadata, at any nesting depth.
//
// Formula step metadata (`[steps.metadata]`) and a gate step's `repo` selector
// (repo = "{{gate_repo}}") are stored literally on the persisted proto by
// processStepToIssue/createGateIssue - `bd cook --persist` keeps the proto
// reusable across pours rather than substituting at compile time. Substitution
// instead happens at the same point as every other var-bearing issue field
// (Title, Description, Assignee, Labels, AwaitID, ...): here, in
// cloneSubgraphInto, when a proto is poured/spawned into real issues.
//
// This supersedes the earlier gh:*-gate-only, top-level-"repo"-only rule
// (SF2/SF4). That restriction existed because interpreting a `repo` key as a
// GitHub selector is only correct on a gh:* gate - but substituting a
// {{var}} placeholder interprets nothing about the key, and general metadata
// carrying literal placeholders was its own bug (GH#5110). A value with no
// placeholder is unaffected either way.
//
// The walk decodes into json.RawMessage rather than interface{} and rebuilds
// only the containers along a changed path, so every untouched value survives
// byte-identical - interface{} would mangle numbers to float64, and a full
// re-marshal of decoded values can HTML-escape strings that were never
// touched. Metadata with no substitutable placeholder is returned as-is.
func substituteMetadataVars(metadata json.RawMessage, vars map[string]string) json.RawMessage {
	if len(metadata) == 0 {
		return metadata
	}
	out, changed := walkJSONStrings(metadata, 0, func(s string) string {
		return substituteVariables(s, vars)
	})
	if !changed {
		return metadata
	}
	return out
}

// metadataVarStrings returns every string leaf in an issue's metadata, for
// variable extraction. Object keys are excluded because substitution does not
// touch them - scanning them would make pour demand a variable it then refuses
// to resolve.
func metadataVarStrings(metadata json.RawMessage) []string {
	if len(metadata) == 0 {
		return nil
	}
	var found []string
	walkJSONStrings(metadata, 0, func(s string) string {
		found = append(found, s)
		return s
	})
	return found
}

// walkJSONStrings applies fn to every string leaf of a JSON value, at any
// nesting depth. It reports whether fn changed anything; when nothing did, the
// input bytes are returned untouched.
//
// Object keys are deliberately left alone: rewriting a key could collide with
// a sibling key and silently drop a value.
func walkJSONStrings(raw json.RawMessage, depth int, fn func(string) string) (json.RawMessage, bool) {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return raw, false
	}

	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return raw, false
		}
		replaced := fn(s)
		if replaced == s {
			return raw, false
		}
		encoded, err := marshalNoHTMLEscape(replaced)
		if err != nil {
			return raw, false
		}
		return encoded, true

	case '{':
		if depth >= maxMetadataSubstitutionDepth {
			return raw, false
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return raw, false
		}
		changed := false
		for k, v := range obj {
			if newV, c := walkJSONStrings(v, depth+1, fn); c {
				obj[k] = newV
				changed = true
			}
		}
		if !changed {
			return raw, false
		}
		out, err := marshalNoHTMLEscape(obj)
		if err != nil {
			return raw, false
		}
		return out, true

	case '[':
		if depth >= maxMetadataSubstitutionDepth {
			return raw, false
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return raw, false
		}
		changed := false
		for i, v := range arr {
			if newV, c := walkJSONStrings(v, depth+1, fn); c {
				arr[i] = newV
				changed = true
			}
		}
		if !changed {
			return raw, false
		}
		out, err := marshalNoHTMLEscape(arr)
		if err != nil {
			return raw, false
		}
		return out, true
	}

	// Number, bool, null: no string leaf here.
	return raw, false
}

// substituteLabels returns labels with {{variable}} placeholders substituted.
// A formula step's labels are carried onto the proto literally by
// processStepToIssue, so - like Title and Description - they resolve here, at
// pour time (GH#5110). Returns nil for an empty input so an issue with no
// labels keeps a nil slice.
func substituteLabels(labels []string, vars map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = substituteVariables(l, vars)
	}
	return out
}

// marshalNoHTMLEscape is json.Marshal without HTML-escaping '<', '>', and
// '&' - the stdlib's json.Marshal escapes them by default (aimed at
// embedding JSON in HTML), which would silently corrupt an unrelated
// metadata value round-tripped through substituteMetadataVars.
func marshalNoHTMLEscape(v interface{}) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder.Encode appends a trailing newline; callers embed this
	// result as a json.RawMessage value, which must not carry one.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// generateBondedID creates a custom ID for dynamically bonded molecules.
// When bonding a proto to a parent molecule, this generates IDs like:
//   - Root: parent.childref (e.g., "patrol-x7k.arm-ace")
//   - Children: parent.childref.step (e.g., "patrol-x7k.arm-ace.capture")
//
// The childRef is variable-substituted before use.
// Returns empty string if not a bonded operation (opts.ParentID empty).
func generateBondedID(oldID string, rootID string, opts CloneOptions) (string, error) {
	if opts.ParentID == "" {
		return "", nil // Not a bonded operation
	}

	// Substitute variables in childRef
	childRef := substituteVariables(opts.ChildRef, opts.Vars)

	// Validate childRef after substitution
	if childRef == "" {
		return "", fmt.Errorf("childRef is empty after variable substitution")
	}
	if !bondedIDPattern.MatchString(childRef) {
		return "", fmt.Errorf("invalid childRef '%s': must be alphanumeric, dash, underscore, or dot only", childRef)
	}

	if oldID == rootID {
		// Root issue: parent.childref
		newID := fmt.Sprintf("%s.%s", opts.ParentID, childRef)
		return newID, nil
	}

	// Child issue: parent.childref.relative
	// Extract the relative portion of the old ID (part after root)
	relativeID := getRelativeID(oldID, rootID)
	if relativeID == "" {
		// No hierarchical relationship - use a suffix from the old ID to ensure uniqueness.
		// Extract the last part of the old ID (after any prefix or dash)
		suffix := extractIDSuffix(oldID)
		newID := fmt.Sprintf("%s.%s.%s", opts.ParentID, childRef, suffix)
		return newID, nil
	}

	newID := fmt.Sprintf("%s.%s.%s", opts.ParentID, childRef, relativeID)
	return newID, nil
}

// extractIDSuffix extracts a suffix from an ID for use when IDs aren't hierarchical.
// For "patrol-abc123", returns "abc123".
// For "bd-xyz.1", returns "1".
// This ensures child IDs remain unique when bonding.
func extractIDSuffix(id string) string {
	// First try to get the part after the last dot (for hierarchical IDs)
	if lastDot := strings.LastIndex(id, "."); lastDot >= 0 {
		return id[lastDot+1:]
	}
	// Otherwise, get the part after the last dash (for prefix-hash IDs)
	if lastDash := strings.LastIndex(id, "-"); lastDash >= 0 {
		return id[lastDash+1:]
	}
	// Fallback: use the whole ID
	return id
}

// getRelativeID extracts the relative portion of a child ID from its parent.
// For example: getRelativeID("bd-abc.step1.sub", "bd-abc") returns "step1.sub"
// Returns empty string if oldID equals rootID or doesn't start with rootID.
func getRelativeID(oldID, rootID string) string {
	if oldID == rootID {
		return ""
	}
	// Check if oldID starts with rootID followed by a dot
	prefix := rootID + "."
	if strings.HasPrefix(oldID, prefix) {
		return oldID[len(prefix):]
	}
	return ""
}

// flattenUnregisteredIssueTypes flattens issue types that are neither
// built-in nor already registered in types.custom, printing a warning
// naming each flattened type. Issues with children (the DependsOnID side
// of a parent-child dep) flatten to epic — matching the default for
// undeclared parent step types — and leaves flatten to task.
// Materializing a formula must not silently grow the type whitelist — a
// typo'd step type would become a permanently registered custom type — so
// unregistered types degrade instead; operators opt in with bd config set
// types.custom before pouring. Without the flatten, issue creation fails
// with "invalid issue type" on the first unregistered bead.
// (GH#3213, GH#5443)
func flattenUnregisteredIssueTypes(ctx context.Context, s configReader, issues []*types.Issue, deps []*types.Dependency) error {
	// Seed with every non-built-in type used by the issues, then remove the
	// registered ones below; what survives is unknown. IsBuiltIn (not
	// IsValid) matches the validator this check exists to satisfy:
	// IsValidWithCustom short-circuits on IsBuiltIn, so types like "event"
	// need no types.custom entry.
	unknown := make(map[types.IssueType]bool)
	for _, issue := range issues {
		t := issue.IssueType
		if t == "" || t.IsBuiltIn() {
			continue
		}
		unknown[t] = true
	}
	if len(unknown) == 0 {
		return nil
	}

	// Match insert validation's sources: the types.custom config value
	// (kept in step with the custom_types table by SyncConfigTables)
	// overlaid with config.yaml-declared types. Read through s so a
	// transaction-bound caller sees in-transaction registration.
	existing, err := s.GetConfig(ctx, "types.custom")
	if err != nil {
		// Don't degrade to "nothing registered": a transient read failure
		// would silently flatten types the operator did register.
		return fmt.Errorf("reading types.custom: %w", err)
	}
	for _, t := range issueops.ParseTypesConfigValue(existing) {
		delete(unknown, types.IssueType(t))
	}
	for _, t := range config.GetCustomTypesFromYAML() {
		delete(unknown, types.IssueType(t))
	}
	if len(unknown) == 0 {
		return nil
	}

	names := make([]string, 0, len(unknown))
	for t := range unknown {
		names = append(names, string(t))
	}
	sort.Strings(names)
	WarnError("flattening unregistered issue type(s) to task (epic for steps with children): %s (register with bd config set types.custom to keep them)", strings.Join(names, ", "))

	hasChildren := make(map[string]bool)
	for _, dep := range deps {
		if dep.Type == types.DepParentChild {
			hasChildren[dep.DependsOnID] = true
		}
	}
	for _, issue := range issues {
		if unknown[issue.IssueType] {
			if hasChildren[issue.ID] {
				issue.IssueType = types.TypeEpic
			} else {
				issue.IssueType = types.TypeTask
			}
		}
	}
	return nil
}

// cloneSubgraph creates new issues from the template with variable substitution.
// Uses CloneOptions to control all spawn/bond behavior including dynamic bonding.
func cloneSubgraph(ctx context.Context, s storage.DoltStorage, subgraph *TemplateSubgraph, opts CloneOptions) (*InstantiateResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	var result *InstantiateResult
	err := transact(ctx, s, "bd: clone template subgraph", func(tx storage.Transaction) error {
		r, err := cloneSubgraphInto(ctx, storeMolWriter{DoltStorage: s, tx: tx}, subgraph, opts)
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func cloneSubgraphInto(ctx context.Context, w molWriter, subgraph *TemplateSubgraph, opts CloneOptions) (*InstantiateResult, error) {
	if err := flattenUnregisteredIssueTypes(ctx, w, subgraph.Issues, subgraph.Dependencies); err != nil {
		return nil, fmt.Errorf("checking custom types for subgraph: %w", err)
	}

	idMapping := make(map[string]string)

	// First pass: create all issues with new IDs
	for _, oldIssue := range subgraph.Issues {
		// RootOnly: skip child issues, only create the root
		if opts.RootOnly && oldIssue.ID != subgraph.Root.ID {
			continue
		}
		// Determine assignee: use override for root epic, otherwise substitute
		// the template's. Step.assignee is documented as supporting
		// substitution, and an unsubstituted one is worse than cosmetic - it
		// makes the poured bead unclosable, because close refuses when the
		// actor doesn't match the assignee (GH#5754). The --assignee override
		// is a literal value supplied on the command line, so it wins as-is.
		issueAssignee := substituteVariables(oldIssue.Assignee, opts.Vars)
		if oldIssue.ID == subgraph.Root.ID && opts.Assignee != "" {
			issueAssignee = opts.Assignee
		}

		newIssue := &types.Issue{
			// ID will be set below based on bonding options
			Title:              substituteVariables(oldIssue.Title, opts.Vars),
			Description:        substituteVariables(oldIssue.Description, opts.Vars),
			Design:             substituteVariables(oldIssue.Design, opts.Vars),
			AcceptanceCriteria: substituteVariables(oldIssue.AcceptanceCriteria, opts.Vars),
			Notes:              substituteVariables(oldIssue.Notes, opts.Vars),
			Status:             types.StatusOpen, // Always start fresh
			Priority:           oldIssue.Priority,
			IssueType:          oldIssue.IssueType,
			Assignee:           issueAssignee,
			EstimatedMinutes:   oldIssue.EstimatedMinutes,
			Ephemeral:          opts.Ephemeral, // mark for cleanup when closed
			IDPrefix:           opts.Prefix,    // distinct prefixes for mols/wisps
			// Gate fields (for async coordination)
			AwaitType: oldIssue.AwaitType,
			AwaitID:   substituteVariables(oldIssue.AwaitID, opts.Vars),
			Timeout:   oldIssue.Timeout,
			Labels:    substituteLabels(oldIssue.Labels, opts.Vars),
			Metadata:  substituteMetadataVars(oldIssue.Metadata, opts.Vars),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		// Generate custom ID for dynamic bonding if ParentID is set
		if opts.ParentID != "" {
			bondedID, err := generateBondedID(oldIssue.ID, subgraph.Root.ID, opts)
			if err != nil {
				return nil, fmt.Errorf("failed to generate bonded ID for %s: %w", oldIssue.ID, err)
			}
			newIssue.ID = bondedID
		}

		if err := w.CreateIssue(ctx, newIssue, opts.Actor); err != nil {
			return nil, fmt.Errorf("failed to create issue from %s: %w", oldIssue.ID, err)
		}

		idMapping[oldIssue.ID] = newIssue.ID
	}

	// Second pass: recreate dependencies with new IDs
	for _, dep := range subgraph.Dependencies {
		newFromID, ok1 := idMapping[dep.IssueID]
		newToID, ok2 := idMapping[dep.DependsOnID]
		if !ok1 || !ok2 {
			continue // Skip if either end is outside the subgraph
		}

		newDep := &types.Dependency{
			IssueID:     newFromID,
			DependsOnID: newToID,
			Type:        dep.Type,
			Metadata:    dep.Metadata,
		}
		if err := w.AddDependency(ctx, newDep, opts.Actor); err != nil {
			return nil, fmt.Errorf("failed to create dependency: %w", err)
		}
	}

	// Atomic attachment: link spawned root to target molecule within
	// the same transaction (bd-wvplu: prevents orphaned spawns)
	if opts.AttachToID != "" {
		attachDep := &types.Dependency{
			IssueID:     idMapping[subgraph.Root.ID],
			DependsOnID: opts.AttachToID,
			Type:        opts.AttachDepType,
		}
		if err := w.AddDependency(ctx, attachDep, opts.Actor); err != nil {
			return nil, fmt.Errorf("attaching to molecule: %w", err)
		}
	}

	return &InstantiateResult{
		NewEpicID: idMapping[subgraph.Root.ID],
		IDMapping: idMapping,
		Created:   len(idMapping),
	}, nil
}

// printTemplateTree prints the template structure as a tree.
// Uses a visited set to detect cycles (GH#2719) and avoid infinite recursion.
func printTemplateTree(subgraph *TemplateSubgraph, parentID string, depth int, isRoot bool) {
	visited := make(map[string]bool)
	printTemplateTreeVisited(subgraph, parentID, depth, isRoot, visited)
}

// printTemplateTreeVisited is the internal recursive implementation with cycle tracking.
func printTemplateTreeVisited(subgraph *TemplateSubgraph, parentID string, depth int, isRoot bool, visited map[string]bool) {
	indent := strings.Repeat("  ", depth)

	// Print root
	if isRoot {
		fmt.Printf("%s   %s (root)\n", indent, subgraph.Root.Title)
		visited[parentID] = true
	}

	// Find children of this parent
	var children []*types.Issue
	for _, dep := range subgraph.Dependencies {
		if dep.DependsOnID == parentID && dep.Type == types.DepParentChild {
			if child, ok := subgraph.IssueMap[dep.IssueID]; ok {
				children = append(children, child)
			}
		}
	}

	// Print children
	for i, child := range children {
		connector := "├──"
		if i == len(children)-1 {
			connector = "└──"
		}
		vars := extractVariables(child.Title)
		varStr := ""
		if len(vars) > 0 {
			varStr = fmt.Sprintf(" [%s]", strings.Join(vars, ", "))
		}

		// Cycle detection (GH#2719)
		if visited[child.ID] {
			fmt.Printf("%s   %s %s%s (cycle detected, skipping)\n", indent, connector, child.Title, varStr)
			continue
		}
		fmt.Printf("%s   %s %s%s\n", indent, connector, child.Title, varStr)
		visited[child.ID] = true
		printTemplateTreeVisited(subgraph, child.ID, depth+1, false, visited)
	}
}
