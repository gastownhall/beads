// Package types defines core data structures for the bd issue tracker.
package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"regexp"
	"strings"
	"time"
)

// Issue represents a trackable work item.
// Fields are organized into logical groups for maintainability.
type Issue struct {
	// ===== Core Identification =====
	ID          string `json:"id"`
	ContentHash string `json:"-"` // Internal: SHA256 of canonical content

	// ===== Issue Content =====
	Title              string `json:"title"`
	Description        string `json:"description,omitempty"`
	Design             string `json:"design,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Notes              string `json:"notes,omitempty"`
	SpecID             string `json:"spec_id,omitempty"`

	// ===== Status & Workflow =====
	Status    Status    `json:"status,omitempty"`
	Priority  int       `json:"priority"` // No omitempty: 0 is valid (P0/critical)
	IssueType IssueType `json:"issue_type,omitempty"`

	// ===== Assignment =====
	Assignee         string `json:"assignee,omitempty"`
	Owner            string `json:"owner,omitempty"` // Human owner for CV attribution (git author email)
	EstimatedMinutes *int   `json:"estimated_minutes,omitempty"`

	// ===== Timestamps =====
	CreatedAt       time.Time  `json:"created_at"`
	CreatedBy       string     `json:"created_by,omitempty"` // Who created this issue (GH#748)
	UpdatedAt       time.Time  `json:"updated_at"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
	CloseReason     string     `json:"close_reason,omitempty"`      // Reason provided when closing
	ClosedBySession string     `json:"closed_by_session,omitempty"` // Claude Code session that closed this issue

	// ===== Time-Based Scheduling (GH#820) =====
	DueAt      *time.Time `json:"due_at,omitempty"`      // When this issue should be completed
	DeferUntil *time.Time `json:"defer_until,omitempty"` // Hide from bd ready until this time

	// ===== External Integration =====
	ExternalRef  *string `json:"external_ref,omitempty"`  // e.g., "gh-9", "jira-ABC"
	SourceSystem string  `json:"source_system,omitempty"` // Adapter/system that created this issue (federation)

	// ===== Custom Metadata =====
	// Metadata holds arbitrary JSON data for extension points (tool annotations, file lists, etc.)
	// Validated as well-formed JSON on create/update. See GH#1406.
	Metadata json.RawMessage `json:"metadata,omitempty"`

	// ===== Compaction Metadata =====
	CompactionLevel   int        `json:"compaction_level,omitempty"`
	CompactedAt       *time.Time `json:"compacted_at,omitempty"`
	CompactedAtCommit *string    `json:"compacted_at_commit,omitempty"` // Git commit hash when compacted
	OriginalSize      int        `json:"original_size,omitempty"`

	// ===== Internal Routing (not synced via git) =====
	SourceRepo     string `json:"-"` // Which repo owns this issue (multi-repo support)
	IDPrefix       string `json:"-"` // Override prefix for ID generation (appends to config prefix)
	PrefixOverride string `json:"-"` // Completely replace config prefix (for cross-rig creation)

	// ===== Relational Data (populated for export/import) =====
	Labels       []string      `json:"labels,omitempty"`
	Dependencies []*Dependency `json:"dependencies,omitempty"`
	Comments     []*Comment    `json:"comments,omitempty"`

	// ===== Messaging Fields (inter-agent communication) =====
	Sender    string   `json:"sender,omitempty"`     // Who sent this (for messages)
	Ephemeral bool     `json:"ephemeral,omitempty"`  // If true, not synced via git
	NoHistory bool     `json:"no_history,omitempty"` // If true, stored in wisps table but NOT GC-eligible
	WispType  WispType `json:"wisp_type,omitempty"`  // Classification for TTL-based compaction (gt-9br)
	// NOTE: RepliesTo, RelatesTo, DuplicateOf, SupersededBy moved to dependencies table
	// per Decision 004 (Edge Schema Consolidation). Use dependency API instead.

	// ===== Context Markers =====
	Pinned     bool `json:"pinned,omitempty"`      // Persistent context marker, not a work item
	IsTemplate bool `json:"is_template,omitempty"` // Read-only template molecule

	// ===== Bonding Fields (compound molecule lineage) =====
	BondedFrom []BondRef `json:"bonded_from,omitempty"` // For compounds: constituent protos

	// ===== Gate Fields (async coordination primitives) =====
	AwaitType string        `json:"await_type,omitempty"` // Condition type: gh:run, gh:pr, timer, human, mail
	AwaitID   string        `json:"await_id,omitempty"`   // Condition identifier (run ID, PR number, etc.)
	Timeout   time.Duration `json:"timeout,omitempty"`    // Max wait time before escalation
	Waiters   []string      `json:"waiters,omitempty"`    // Mail addresses to notify when gate clears

	// ===== Source Tracing Fields (formula cooking origin) =====
	SourceFormula  string `json:"source_formula,omitempty"`  // Formula name where step was defined
	SourceLocation string `json:"source_location,omitempty"` // Path: "steps[0]", "advice[0].after"

	// ===== Molecule Type Fields (swarm coordination) =====
	MolType MolType `json:"mol_type,omitempty"` // Molecule type: swarm|patrol|work (empty = work)

	// ===== Work Type Fields (assignment model - Decision 006) =====
	WorkType WorkType `json:"work_type,omitempty"` // Work type: mutex|open_competition (empty = mutex)

	// ===== Event Fields (operational state changes) =====
	EventKind string `json:"event_kind,omitempty"` // Namespaced event type: patrol.muted, agent.started
	Actor     string `json:"actor,omitempty"`      // Entity URI who caused this event
	Target    string `json:"target,omitempty"`     // Entity URI or bead ID affected
	Payload   string `json:"payload,omitempty"`    // Event-specific JSON data
}

// ComputeContentHash creates a deterministic hash of the issue's content.
// Uses all substantive fields (excluding ID, timestamps, and compaction metadata)
// to ensure that identical content produces identical hashes across all clones.
func (i *Issue) ComputeContentHash() string {
	h := sha256.New()
	w := hashFieldWriter{h}

	// Core fields in stable order
	w.str(i.Title)
	w.str(i.Description)
	w.str(i.Design)
	w.str(i.AcceptanceCriteria)
	w.str(i.Notes)
	w.str(i.SpecID)
	w.str(string(i.Status))
	w.int(i.Priority)
	w.str(string(i.IssueType))
	w.str(i.Assignee)
	w.str(i.Owner)
	w.str(i.CreatedBy)

	// Optional fields
	w.strPtr(i.ExternalRef)
	w.str(i.SourceSystem)
	w.flag(i.Pinned, "pinned")
	w.str(string(i.Metadata)) // Include metadata in content hash
	w.flag(i.IsTemplate, "template")

	// Bonded molecules
	for _, br := range i.BondedFrom {
		w.str(br.SourceID)
		w.str(br.BondType)
		w.str(br.BondPoint)
	}

	// Gate fields for async coordination
	w.str(i.AwaitType)
	w.str(i.AwaitID)
	w.duration(i.Timeout)
	for _, waiter := range i.Waiters {
		w.str(waiter)
	}

	// Molecule type
	w.str(string(i.MolType))

	// Work type
	w.str(string(i.WorkType))

	// Event fields
	w.str(i.EventKind)
	w.str(i.Actor)
	w.str(i.Target)
	w.str(i.Payload)

	return fmt.Sprintf("%x", h.Sum(nil))
}

// hashFieldWriter provides helper methods for writing fields to a hash.
// Each method writes the value followed by a null separator for consistency.
type hashFieldWriter struct {
	h hash.Hash
}

func (w hashFieldWriter) str(s string) {
	w.h.Write([]byte(s))
	w.h.Write([]byte{0})
}

func (w hashFieldWriter) int(n int) {
	w.h.Write([]byte(fmt.Sprintf("%d", n)))
	w.h.Write([]byte{0})
}

func (w hashFieldWriter) strPtr(p *string) {
	if p != nil {
		w.h.Write([]byte(*p))
	}
	w.h.Write([]byte{0})
}

func (w hashFieldWriter) duration(d time.Duration) {
	w.h.Write([]byte(fmt.Sprintf("%d", d)))
	w.h.Write([]byte{0})
}

func (w hashFieldWriter) flag(b bool, label string) {
	if b {
		w.h.Write([]byte(label))
	}
	w.h.Write([]byte{0})
}

// Validate checks if the issue has valid field values (built-in statuses only)
func (i *Issue) Validate() error {
	return i.ValidateWithCustomStatuses(nil)
}

// ValidateWithCustomStatuses checks if the issue has valid field values,
// allowing custom statuses in addition to built-in ones.
func (i *Issue) ValidateWithCustomStatuses(customStatuses []string) error {
	return i.ValidateWithCustom(customStatuses, nil)
}

// ValidateWithCustom checks if the issue has valid field values,
// allowing custom statuses and types in addition to built-in ones.
func (i *Issue) ValidateWithCustom(customStatuses, customTypes []string) error {
	if len(i.Title) == 0 {
		return fmt.Errorf("title is required")
	}
	if len(i.Title) > 500 {
		return fmt.Errorf("title must be 500 characters or less (got %d)", len(i.Title))
	}
	if i.Priority < 0 || i.Priority > 4 {
		return fmt.Errorf("priority must be between 0 and 4 (got %d)", i.Priority)
	}
	if !i.Status.IsValidWithCustom(customStatuses) {
		return fmt.Errorf("invalid status: %s", i.Status)
	}
	if !i.IssueType.IsValidWithCustom(customTypes) {
		return fmt.Errorf("invalid issue type: %s", i.IssueType)
	}
	if i.EstimatedMinutes != nil && *i.EstimatedMinutes < 0 {
		return fmt.Errorf("estimated_minutes cannot be negative")
	}
	// Enforce closed_at invariant: closed_at should be set if and only if status is closed
	if i.Status == StatusClosed && i.ClosedAt == nil {
		return fmt.Errorf("closed issues must have closed_at timestamp")
	}
	if i.Status != StatusClosed && i.ClosedAt != nil {
		return fmt.Errorf("non-closed issues cannot have closed_at timestamp")
	}
	// Validate metadata is well-formed JSON if set (GH#1406)
	if len(i.Metadata) > 0 {
		if !json.Valid(i.Metadata) {
			return fmt.Errorf("metadata must be valid JSON")
		}
	}
	// Ephemeral and NoHistory are mutually exclusive (GH#2619)
	if i.Ephemeral && i.NoHistory {
		return fmt.Errorf("ephemeral and no_history are mutually exclusive")
	}
	return nil
}

// ValidateForImport validates the issue for multi-repo import (federation trust model).
// Built-in types are validated (to catch typos). Non-built-in types are trusted
// since the source repo already validated them when the issue was created.
// This implements "trust the chain below you" from the HOP federation model.
func (i *Issue) ValidateForImport(customStatuses []string) error {
	if len(i.Title) == 0 {
		return fmt.Errorf("title is required")
	}
	if len(i.Title) > 500 {
		return fmt.Errorf("title must be 500 characters or less (got %d)", len(i.Title))
	}
	if i.Priority < 0 || i.Priority > 4 {
		return fmt.Errorf("priority must be between 0 and 4 (got %d)", i.Priority)
	}
	if !i.Status.IsValidWithCustom(customStatuses) {
		return fmt.Errorf("invalid status: %s", i.Status)
	}
	// Issue type validation: federation trust model
	// Only validate built-in types (catch typos like "tsak" vs "task")
	// Trust non-built-in types from source repo
	if i.IssueType != "" && i.IssueType.IsValid() {
		// Built-in type - it's valid
	} else if i.IssueType != "" && !i.IssueType.IsValid() {
		// Non-built-in type - trust it (child repo already validated)
	}
	if i.EstimatedMinutes != nil && *i.EstimatedMinutes < 0 {
		return fmt.Errorf("estimated_minutes cannot be negative")
	}
	// Enforce closed_at invariant
	if i.Status == StatusClosed && i.ClosedAt == nil {
		return fmt.Errorf("closed issues must have closed_at timestamp")
	}
	if i.Status != StatusClosed && i.ClosedAt != nil {
		return fmt.Errorf("non-closed issues cannot have closed_at timestamp")
	}
	// Validate metadata is well-formed JSON if set (GH#1406)
	if len(i.Metadata) > 0 {
		if !json.Valid(i.Metadata) {
			return fmt.Errorf("metadata must be valid JSON")
		}
	}
	return nil
}

// SetDefaults applies default values for fields that may be omitted during deserialization.
// Call this after json.Unmarshal to ensure missing fields have proper defaults:
//   - Status: defaults to StatusOpen if empty
//   - Priority: defaults to 2 if zero (note: P0 issues must explicitly set priority=0)
//   - IssueType: defaults to TypeTask if empty
func (i *Issue) SetDefaults() {
	if i.Status == "" {
		i.Status = StatusOpen
	}
	// Note: priority 0 (P0) is a valid value, so we can't distinguish between
	// "explicitly set to 0" and "omitted". We treat priority 0 as P0,
	// not as "use default". P0 issues are explicitly marked.
	// Priority default of 2 only applies to new issues via Create, not import.
	if i.IssueType == "" {
		i.IssueType = TypeTask
	}
}

// Status represents the current state of an issue
type Status string

// Issue status constants
const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusDeferred   Status = "deferred" // Deliberately put on ice for later
	StatusClosed     Status = "closed"
	StatusPinned     Status = "pinned" // Persistent bead that stays open indefinitely
	StatusHooked     Status = "hooked" // Work actively claimed by a worker
)

// IsValid checks if the status value is valid (built-in statuses only)
func (s Status) IsValid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked, StatusDeferred, StatusClosed, StatusPinned, StatusHooked:
		return true
	}
	return false
}

// IsValidWithCustom checks if the status is valid, including custom statuses.
// Custom statuses are user-defined via bd config set status.custom "status1,status2,..."
func (s Status) IsValidWithCustom(customStatuses []string) bool {
	// First check built-in statuses
	if s.IsValid() {
		return true
	}
	// Then check custom statuses
	for _, custom := range customStatuses {
		if string(s) == custom {
			return true
		}
	}
	return false
}

// IsValidWithCustomStatuses checks if the status is valid, including typed custom statuses.
func (s Status) IsValidWithCustomStatuses(customStatuses []CustomStatus) bool {
	if s.IsValid() {
		return true
	}
	for _, cs := range customStatuses {
		if string(s) == cs.Name {
			return true
		}
	}
	return false
}

// StatusCategory defines how a custom status behaves in views and commands.
type StatusCategory string

const (
	// CategoryActive statuses appear in bd ready and default bd list.
	CategoryActive StatusCategory = "active"
	// CategoryWIP statuses are excluded from bd ready but visible in default bd list.
	CategoryWIP StatusCategory = "wip"
	// CategoryDone statuses are excluded from bd ready and default bd list.
	CategoryDone StatusCategory = "done"
	// CategoryFrozen statuses are excluded from bd ready and default bd list.
	CategoryFrozen StatusCategory = "frozen"
	// CategoryUnspecified is assigned when no category is provided (backward compat).
	// Behaves like current behavior: valid, visible in default bd list, absent from bd ready.
	CategoryUnspecified StatusCategory = "unspecified"
)

// validCategories is the set of user-assignable categories (excludes CategoryUnspecified).
var validCategories = map[StatusCategory]bool{
	CategoryActive: true,
	CategoryWIP:    true,
	CategoryDone:   true,
	CategoryFrozen: true,
}

// CustomStatus represents a user-defined status with its behavioral category.
type CustomStatus struct {
	Name     string         `json:"name"`
	Category StatusCategory `json:"category"`
}

// statusNameRegexp validates custom status names: letter-first, lowercase alphanumeric with hyphens/underscores.
var statusNameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// maxCustomStatuses is the maximum number of custom statuses allowed.
const maxCustomStatuses = 50

// builtInStatusNames contains all built-in status names in lowercase for collision detection.
var builtInStatusNames = map[string]bool{
	"open": true, "in_progress": true, "blocked": true,
	"deferred": true, "closed": true, "pinned": true, "hooked": true,
}

// ParseCustomStatusConfig parses a status.custom config value into typed CustomStatus entries.
// Supports both legacy flat format ("foo,bar") and category-annotated format ("foo:active,bar:wip").
// Statuses without a category annotation get CategoryUnspecified (backward compatible).
func ParseCustomStatusConfig(value string) ([]CustomStatus, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	var result []CustomStatus
	seen := make(map[string]bool)

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		var name string
		var category StatusCategory

		// Split on first colon only
		if idx := strings.IndexByte(part, ':'); idx >= 0 {
			name = part[:idx]
			catStr := part[idx+1:]
			if catStr == "" {
				return nil, fmt.Errorf("invalid custom status %q: trailing colon with empty category", part)
			}
			category = StatusCategory(catStr)
			if !validCategories[category] {
				return nil, fmt.Errorf("invalid category %q for status %q: must be one of active, wip, done, frozen", catStr, name)
			}
		} else {
			name = part
			category = CategoryUnspecified
		}

		if !statusNameRegexp.MatchString(name) {
			return nil, fmt.Errorf("invalid status name %q: must match [a-z][a-z0-9_-]* (lowercase, letter-first, no spaces)", name)
		}

		if builtInStatusNames[strings.ToLower(name)] {
			return nil, fmt.Errorf("custom status %q collides with built-in status", name)
		}

		if seen[name] {
			return nil, fmt.Errorf("duplicate custom status name %q", name)
		}
		seen[name] = true

		result = append(result, CustomStatus{Name: name, Category: category})
	}

	if len(result) > maxCustomStatuses {
		return nil, fmt.Errorf("too many custom statuses (%d): maximum is %d", len(result), maxCustomStatuses)
	}

	return result, nil
}

// CustomStatusNames extracts just the name strings from a slice of CustomStatus.
// Useful for backward-compatible callers that only need names for validation.
func CustomStatusNames(statuses []CustomStatus) []string {
	if len(statuses) == 0 {
		return nil
	}
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = s.Name
	}
	return names
}

// CustomStatusesByCategory returns custom statuses filtered by the given category.
func CustomStatusesByCategory(statuses []CustomStatus, category StatusCategory) []CustomStatus {
	var result []CustomStatus
	for _, s := range statuses {
		if s.Category == category {
			result = append(result, s)
		}
	}
	return result
}

// BuiltInStatusCategory returns the category for a built-in status.
func BuiltInStatusCategory(status Status) StatusCategory {
	switch status {
	case StatusOpen:
		return CategoryActive
	case StatusInProgress, StatusBlocked, StatusHooked:
		return CategoryWIP
	case StatusClosed:
		return CategoryDone
	case StatusDeferred, StatusPinned:
		return CategoryFrozen
	default:
		return CategoryUnspecified
	}
}

// IssueType categorizes the kind of work
type IssueType string

// Core work type constants - these are the built-in types that beads validates.
// All other types require configuration via types.custom in config.yaml.
const (
	TypeBug      IssueType = "bug"
	TypeFeature  IssueType = "feature"
	TypeTask     IssueType = "task"
	TypeEpic     IssueType = "epic"
	TypeChore    IssueType = "chore"
	TypeDecision IssueType = "decision"
	TypeMessage  IssueType = "message"
	TypeMolecule IssueType = "molecule" // Molecule type for swarm coordination (internal use)
)

// TypeEvent is a system-internal type used by set-state for audit trail beads.
// Originally an orchestrator type, promoted to built-in internal type. It is not a
// core work type (not in IsValid) but is accepted by IsValidWithCustom /
// ValidateWithCustom and treated as built-in for hydration trust (GH#1356).
const TypeEvent IssueType = "event"

// Note: Orchestrator types (molecule, gate, convoy, merge-request, slot, agent, role, rig)
// were removed from beads core. They are now purely custom types with no built-in constants.
// Use string literals like types.IssueType("molecule") if needed, and configure types.custom.
// (event was also an orchestrator type but was promoted to a built-in internal type above.)
// (message was re-promoted to built-in for inter-agent communication — GH#1347.)

// IsValid checks if the issue type is a core work type.
// Core work types (bug, feature, task, epic, chore, decision, message) and molecule type are built-in.
// Other types (gate, convoy, etc.) require types.custom configuration.
func (t IssueType) IsValid() bool {
	switch t {
	case TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore, TypeDecision, TypeMessage, TypeMolecule:
		return true
	}
	return false
}

// IsBuiltIn returns true for core work types and system-internal types
// (i.e. TypeEvent). Used during multi-repo hydration to determine trust:
// - Built-in/internal types: validate (catch typos)
// - Custom types (!IsBuiltIn): trust from source repo
func (t IssueType) IsBuiltIn() bool {
	return t.IsValid() || t == TypeEvent
}

// IsValidWithCustom checks if the issue type is valid, including custom types.
// Custom types are user-defined via bd config set types.custom "type1,type2,..."
func (t IssueType) IsValidWithCustom(customTypes []string) bool {
	if t.IsBuiltIn() {
		return true
	}
	// Check user-configured custom types
	for _, custom := range customTypes {
		if string(t) == custom {
			return true
		}
	}
	return false
}

// Normalize maps issue type aliases to their canonical form.
// For example, "enhancement" -> "feature".
// Case-insensitive to match util.NormalizeIssueType behavior.
func (t IssueType) Normalize() IssueType {
	switch strings.ToLower(string(t)) {
	case "enhancement", "feat":
		return TypeFeature
	case "dec", "adr":
		return TypeDecision
	default:
		return t
	}
}

// RequiredSection describes a recommended section for an issue type.
// Used by bd lint and bd create --validate for template validation.
type RequiredSection struct {
	Heading string // Markdown heading, e.g., "## Steps to Reproduce"
	Hint    string // Guidance for what to include
}

// RequiredSections returns the recommended sections for this issue type.
// Returns nil for types with no specific section requirements.
func (t IssueType) RequiredSections() []RequiredSection {
	switch t {
	case TypeBug:
		return []RequiredSection{
			{Heading: "## Steps to Reproduce", Hint: "Describe how to reproduce the bug"},
			{Heading: "## Acceptance Criteria", Hint: "Define criteria to verify the fix"},
		}
	case TypeTask, TypeFeature:
		return []RequiredSection{
			{Heading: "## Acceptance Criteria", Hint: "Define criteria to verify completion"},
		}
	case TypeEpic:
		return []RequiredSection{
			{Heading: "## Success Criteria", Hint: "Define high-level success criteria"},
		}
	case TypeDecision:
		return []RequiredSection{
			{Heading: "## Decision", Hint: "Summarize what was decided"},
			{Heading: "## Rationale", Hint: "Explain why this option was chosen"},
			{Heading: "## Alternatives Considered", Hint: "List alternatives and why they were rejected"},
		}
	default:
		// Chore and custom types have no required sections
		return nil
	}
}

// MolType categorizes the molecule type for swarm coordination
type MolType string

// MolType constants
const (
	MolTypeSwarm  MolType = "swarm"  // Swarm molecule: coordinated multi-worker work
	MolTypePatrol MolType = "patrol" // Patrol molecule: recurring operational work
	MolTypeWork   MolType = "work"   // Work molecule: regular assigned work (default)
)

// IsValid checks if the mol type value is valid
func (m MolType) IsValid() bool {
	switch m {
	case MolTypeSwarm, MolTypePatrol, MolTypeWork, "":
		return true // empty is valid (defaults to work)
	}
	return false
}

// WispType categorizes ephemeral wisps for TTL-based compaction (gt-9br)
type WispType string

// WispType constants - see WISP-COMPACTION-POLICY.md for TTL assignments
const (
	// Category 1: High-churn, low forensic value (TTL: 6h)
	WispTypeHeartbeat WispType = "heartbeat" // Liveness pings
	WispTypePing      WispType = "ping"      // Health check ACKs

	// Category 2: Operational state (TTL: 24h)
	WispTypePatrol   WispType = "patrol"    // Patrol cycle reports
	WispTypeGCReport WispType = "gc_report" // Garbage collection reports

	// Category 3: Significant events (TTL: 7d)
	WispTypeRecovery   WispType = "recovery"   // Force-kill, recovery actions
	WispTypeError      WispType = "error"      // Error reports
	WispTypeEscalation WispType = "escalation" // Human escalations
)

// IsValid checks if the wisp type value is valid
func (w WispType) IsValid() bool {
	switch w {
	case WispTypeHeartbeat, WispTypePing, WispTypePatrol, WispTypeGCReport,
		WispTypeRecovery, WispTypeError, WispTypeEscalation, "":
		return true // empty is valid (uses default TTL)
	}
	return false
}

// WorkType categorizes how work assignment operates for a bead (Decision 006)
type WorkType string

// WorkType constants
const (
	WorkTypeMutex           WorkType = "mutex"            // One worker, exclusive assignment (default)
	WorkTypeOpenCompetition WorkType = "open_competition" // Many submit, buyer picks
)

// IsValid checks if the work type value is valid
func (w WorkType) IsValid() bool {
	switch w {
	case WorkTypeMutex, WorkTypeOpenCompetition, "":
		return true // empty is valid (defaults to mutex)
	}
	return false
}

// Dependency represents a relationship between issues
type Dependency struct {
	IssueID     string         `json:"issue_id"`
	DependsOnID string         `json:"depends_on_id"`
	Type        DependencyType `json:"type"`
	CreatedAt   time.Time      `json:"created_at"`
	CreatedBy   string         `json:"created_by,omitempty"`
	// Metadata contains type-specific edge data (JSON blob)
	// Examples: similarity scores, approval details, skill proficiency
	Metadata string `json:"metadata,omitempty"`
	// ThreadID groups conversation edges for efficient thread queries
	// For replies-to edges, this identifies the conversation root
	ThreadID string `json:"thread_id,omitempty"`
}

// DependencyCounts holds counts for dependencies and dependents
type DependencyCounts struct {
	DependencyCount int `json:"dependency_count"` // Number of issues this issue depends on
	DependentCount  int `json:"dependent_count"`  // Number of issues that depend on this issue
}

// IssueWithDependencyMetadata extends Issue with dependency relationship type
// Note: We explicitly include all Issue fields to ensure proper JSON marshaling
type IssueWithDependencyMetadata struct {
	Issue
	DependencyType DependencyType `json:"dependency_type"`
}

// IssueWithCounts extends Issue with dependency relationship counts
type IssueWithCounts struct {
	*Issue
	DependencyCount int     `json:"dependency_count"`
	DependentCount  int     `json:"dependent_count"`
	CommentCount    int     `json:"comment_count"`
	Parent          *string `json:"parent,omitempty"` // Computed parent from parent-child dep (bd-ym8c)
}

// IssueDetails extends Issue with labels, dependencies, dependents, and comments.
// Used for JSON serialization in bd show and RPC responses.
type IssueDetails struct {
	Issue
	Labels       []string                       `json:"labels,omitempty"`
	Dependencies []*IssueWithDependencyMetadata `json:"dependencies,omitempty"`
	Dependents   []*IssueWithDependencyMetadata `json:"dependents,omitempty"`
	Comments     []*Comment                     `json:"comments,omitempty"`
	Parent       *string                        `json:"parent,omitempty"`

	// Epic progress fields (populated only for issue_type=epic with children)
	EpicTotalChildren  *int  `json:"epic_total_children,omitempty"`
	EpicClosedChildren *int  `json:"epic_closed_children,omitempty"`
	EpicCloseable      *bool `json:"epic_closeable,omitempty"`
}

// DependencyType categorizes the relationship
type DependencyType string

// Dependency type constants
const (
	// Workflow types (affect ready work calculation)
	DepBlocks            DependencyType = "blocks"
	DepParentChild       DependencyType = "parent-child"
	DepConditionalBlocks DependencyType = "conditional-blocks" // B runs only if A fails
	DepWaitsFor          DependencyType = "waits-for"          // Fanout gate: wait for dynamic children

	// Association types
	DepRelated        DependencyType = "related"
	DepDiscoveredFrom DependencyType = "discovered-from"

	// Graph link types
	DepRepliesTo  DependencyType = "replies-to" // Conversation threading
	DepRelatesTo  DependencyType = "relates-to" // Loose knowledge graph edges
	DepDuplicates DependencyType = "duplicates" // Deduplication link
	DepSupersedes DependencyType = "supersedes" // Version chain link

	// Entity types (HOP foundation - Decision 004)
	DepAuthoredBy DependencyType = "authored-by" // Creator relationship
	DepAssignedTo DependencyType = "assigned-to" // Assignment relationship
	DepApprovedBy DependencyType = "approved-by" // Approval relationship
	DepAttests    DependencyType = "attests"     // Skill attestation: X attests Y has skill Z

	// Convoy tracking (non-blocking cross-project references)
	DepTracks DependencyType = "tracks" // Convoy → issue tracking (non-blocking)

	// Reference types (cross-referencing without blocking)
	DepUntil     DependencyType = "until"     // Active until target closes (e.g., muted until issue resolved)
	DepCausedBy  DependencyType = "caused-by" // Triggered by target (audit trail)
	DepValidates DependencyType = "validates" // Approval/validation relationship

	// Delegation types (work delegation chains)
	DepDelegatedFrom DependencyType = "delegated-from" // Work delegated from parent; completion cascades up
)

// IsValid checks if the dependency type value is valid.
// Accepts any non-empty string up to 50 characters.
// Use IsWellKnown() to check if it's a built-in type.
func (d DependencyType) IsValid() bool {
	return len(d) > 0 && len(d) <= 50
}

// IsWellKnown checks if the dependency type is a well-known constant.
// Returns false for custom/user-defined types (which are still valid).
func (d DependencyType) IsWellKnown() bool {
	switch d {
	case DepBlocks, DepParentChild, DepConditionalBlocks, DepWaitsFor, DepRelated, DepDiscoveredFrom,
		DepRepliesTo, DepRelatesTo, DepDuplicates, DepSupersedes,
		DepAuthoredBy, DepAssignedTo, DepApprovedBy, DepAttests, DepTracks,
		DepUntil, DepCausedBy, DepValidates, DepDelegatedFrom:
		return true
	}
	return false
}

// AffectsReadyWork returns true if this dependency type blocks work.
// Only blocking types affect the ready work calculation.
func (d DependencyType) AffectsReadyWork() bool {
	return d == DepBlocks || d == DepParentChild || d == DepConditionalBlocks || d == DepWaitsFor
}

// WaitsForMeta holds metadata for waits-for dependencies (fanout gates).
// Stored as JSON in the Dependency.Metadata field.
type WaitsForMeta struct {
	// Gate type: "all-children" (wait for all), "any-children" (wait for first)
	Gate string `json:"gate"`
	// SpawnerID identifies which step/issue spawns the children to wait for.
	// If empty, waits for all direct children of the depends_on_id issue.
	SpawnerID string `json:"spawner_id,omitempty"`
}

// WaitsForGate constants
const (
	WaitsForAllChildren = "all-children" // Wait for all dynamic children to complete
	WaitsForAnyChildren = "any-children" // Proceed when first child completes (future)
)

// ParseWaitsForGateMetadata extracts the waits-for gate type from dependency metadata.
// Note: spawner identity comes from dependencies.depends_on_id in storage/query paths;
// metadata.spawner_id is parsed for compatibility/future explicit targeting.
// Returns WaitsForAllChildren on empty/invalid metadata for backward compatibility.
func ParseWaitsForGateMetadata(metadata string) string {
	if strings.TrimSpace(metadata) == "" {
		return WaitsForAllChildren
	}

	var meta WaitsForMeta
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return WaitsForAllChildren
	}
	if meta.Gate == WaitsForAnyChildren {
		return WaitsForAnyChildren
	}
	return WaitsForAllChildren
}

// AttestsMeta holds metadata for attests dependencies (skill attestations).
// Stored as JSON in the Dependency.Metadata field.
// Enables: Entity X attests that Entity Y has skill Z at level N.
type AttestsMeta struct {
	// Skill is the identifier of the skill being attested (e.g., "go", "rust", "code-review")
	Skill string `json:"skill"`
	// Level is the proficiency level (e.g., "beginner", "intermediate", "expert", or numeric 1-5)
	Level string `json:"level"`
	// Date is when the attestation was made (RFC3339 format)
	Date string `json:"date"`
	// Evidence is optional reference to supporting evidence (e.g., issue ID, commit, PR)
	Evidence string `json:"evidence,omitempty"`
	// Notes is optional free-form notes about the attestation
	Notes string `json:"notes,omitempty"`
}

// FailureCloseKeywords are keywords that indicate an issue was closed due to failure.
// Used by conditional-blocks dependencies to determine if the condition is met.
var FailureCloseKeywords = []string{
	"failed",
	"rejected",
	"wontfix",
	"won't fix",
	"canceled",
	"cancelled", //nolint:misspell // British spelling intentionally included
	"abandoned",
	"blocked",
	"error",
	"timeout",
	"aborted",
}

// IsFailureClose returns true if the close reason indicates the issue failed.
// This is used by conditional-blocks dependencies: B runs only if A fails.
// A "failure" close reason contains one of the FailureCloseKeywords (case-insensitive).
func IsFailureClose(closeReason string) bool {
	if closeReason == "" {
		return false
	}
	lower := strings.ToLower(closeReason)
	for _, keyword := range FailureCloseKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// Label represents a tag on an issue
type Label struct {
	IssueID string `json:"issue_id"`
	Label   string `json:"label"`
}

// Comment represents a comment on an issue
type Comment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Event represents an audit trail entry
type Event struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	EventType EventType `json:"event_type"`
	Actor     string    `json:"actor"`
	OldValue  *string   `json:"old_value,omitempty"`
	NewValue  *string   `json:"new_value,omitempty"`
	Comment   *string   `json:"comment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// EventType categorizes audit trail events
type EventType string

// Event type constants for audit trail
const (
	EventCreated           EventType = "created"
	EventUpdated           EventType = "updated"
	EventStatusChanged     EventType = "status_changed"
	EventCommented         EventType = "commented"
	EventClosed            EventType = "closed"
	EventReopened          EventType = "reopened"
	EventDependencyAdded   EventType = "dependency_added"
	EventDependencyRemoved EventType = "dependency_removed"
	EventLabelAdded        EventType = "label_added"
	EventLabelRemoved      EventType = "label_removed"
	EventCompacted         EventType = "compacted"
)

// BlockedIssue extends Issue with blocking information
type BlockedIssue struct {
	Issue
	BlockedByCount int      `json:"blocked_by_count"`
	BlockedBy      []string `json:"blocked_by"`
}

// TreeNode represents a node in a dependency tree
type TreeNode struct {
	Issue
	Depth     int    `json:"depth"`
	ParentID  string `json:"parent_id"`
	Truncated bool   `json:"truncated"`
}

// MoleculeProgressStats provides efficient progress info for large molecules.
// This uses indexed queries instead of loading all steps into memory.
type MoleculeProgressStats struct {
	MoleculeID    string     `json:"molecule_id"`
	MoleculeTitle string     `json:"molecule_title"`
	Total         int        `json:"total"`           // Total steps (direct children)
	Completed     int        `json:"completed"`       // Closed steps
	InProgress    int        `json:"in_progress"`     // Steps currently in progress
	CurrentStepID string     `json:"current_step_id"` // First in_progress step ID (if any)
	FirstClosed   *time.Time `json:"first_closed,omitempty"`
	LastClosed    *time.Time `json:"last_closed,omitempty"`
}

// MoleculeLastActivity holds the most recent activity timestamp for a molecule.
type MoleculeLastActivity struct {
	MoleculeID   string    `json:"molecule_id"`
	LastActivity time.Time `json:"last_activity"`
	Source       string    `json:"source"` // "step_closed", "step_updated", "molecule_updated"
	SourceStepID string    `json:"source_step_id,omitempty"`
}

// Statistics provides aggregate metrics
type Statistics struct {
	TotalIssues             int     `json:"total_issues"`
	OpenIssues              int     `json:"open_issues"`
	InProgressIssues        int     `json:"in_progress_issues"`
	ClosedIssues            int     `json:"closed_issues"`
	BlockedIssues           int     `json:"blocked_issues"`
	DeferredIssues          int     `json:"deferred_issues"` // Issues on ice
	ReadyIssues             int     `json:"ready_issues"`
	PinnedIssues            int     `json:"pinned_issues"` // Persistent issues
	EpicsEligibleForClosure int     `json:"epics_eligible_for_closure"`
	AverageLeadTime         float64 `json:"average_lead_time_hours"`
}

// IssueFilter is used to filter issue queries
type IssueFilter struct {
	Status       *Status
	Priority     *int
	IssueType    *IssueType
	Assignee     *string
	Labels       []string // AND semantics: issue must have ALL these labels
	LabelsAny    []string // OR semantics: issue must have AT LEAST ONE of these labels
	LabelPattern string   // Glob pattern for label matching (e.g., "tech-*")
	LabelRegex   string   // Regex pattern for label matching (e.g., "tech-(debt|legacy)")
	TitleSearch  string
	IDs          []string // Filter by specific issue IDs
	IDPrefix     string   // Filter by ID prefix (e.g., "bd-" to match "bd-abc123")
	SpecIDPrefix string   // Filter by spec_id prefix
	Limit        int

	// Pattern matching
	TitleContains       string
	DescriptionContains string
	NotesContains       string
	ExternalRefContains string

	// Date ranges
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
	ClosedAfter   *time.Time
	ClosedBefore  *time.Time

	// Empty/null checks
	EmptyDescription bool
	NoAssignee       bool
	NoLabels         bool

	// Numeric ranges
	PriorityMin *int
	PriorityMax *int

	// Source repo filtering (for multi-repo support)
	SourceRepo *string // Filter by source_repo field (nil = any)

	// Ephemeral filtering
	Ephemeral *bool // Filter by ephemeral flag (nil = any, true = only ephemeral, false = only persistent)

	// Pinned filtering
	Pinned *bool // Filter by pinned flag (nil = any, true = only pinned, false = only non-pinned)

	// Template filtering
	IsTemplate *bool // Filter by template flag (nil = any, true = only templates, false = exclude templates)

	// Parent filtering: filter children by parent issue ID
	ParentID *string // Filter by parent issue (via parent-child dependency)
	NoParent bool    // Exclude issues that are children of another issue

	// Molecule type filtering
	MolType *MolType // Filter by molecule type (nil = any, swarm/patrol/work)

	// Wisp type filtering (TTL-based compaction classification)
	WispType *WispType // Filter by wisp type (nil = any, heartbeat/ping/patrol/gc_report/recovery/error/escalation)

	// Status exclusion (for default non-closed behavior)
	ExcludeStatus []Status // Exclude issues with these statuses

	// Type exclusion (for hiding internal types like gates)
	ExcludeTypes []IssueType // Exclude issues with these types

	// Time-based scheduling filters (GH#820)
	Deferred    bool       // Filter issues with defer_until set (any value)
	DeferAfter  *time.Time // Filter issues with defer_until > this time
	DeferBefore *time.Time // Filter issues with defer_until < this time
	DueAfter    *time.Time // Filter issues with due_at > this time
	DueBefore   *time.Time // Filter issues with due_at < this time
	Overdue     bool       // Filter issues where due_at < now AND status != closed

	// Metadata field filtering (GH#1406)
	MetadataFields map[string]string // Top-level key=value equality; AND semantics (all must match)
	HasMetadataKey string            // Existence check: issue has this top-level key set (non-null)
}

// SortPolicy determines how ready work is ordered
type SortPolicy string

// Sort policy constants
const (
	// SortPolicyHybrid prioritizes recent issues by priority, older by age
	// Recent = created within 48 hours
	// This is the default for backwards compatibility
	SortPolicyHybrid SortPolicy = "hybrid"

	// SortPolicyPriority always sorts by priority first, then creation date
	// Use for autonomous execution, CI/CD, priority-driven workflows
	SortPolicyPriority SortPolicy = "priority"

	// SortPolicyOldest always sorts by creation date (oldest first)
	// Use for backlog clearing, preventing issue starvation
	SortPolicyOldest SortPolicy = "oldest"
)

// IsValid checks if the sort policy value is valid
func (s SortPolicy) IsValid() bool {
	switch s {
	case SortPolicyHybrid, SortPolicyPriority, SortPolicyOldest, "":
		return true
	}
	return false
}

// WorkFilter is used to filter ready work queries
type WorkFilter struct {
	Status       Status
	Type         string // Filter by issue type (task, bug, feature, epic, merge-request, etc.)
	Priority     *int
	Assignee     *string
	Unassigned   bool     // Filter for issues with no assignee
	Labels       []string // AND semantics: issue must have ALL these labels
	LabelsAny    []string // OR semantics: issue must have AT LEAST ONE of these labels
	LabelPattern string   // Glob pattern for label matching (e.g., "tech-*")
	LabelRegex   string   // Regex pattern for label matching (e.g., "tech-(debt|legacy)")
	Limit        int
	SortPolicy   SortPolicy

	// Parent filtering: filter to descendants of a bead/epic (recursive)
	ParentID *string // Show all descendants of this issue

	// Molecule filtering: filter to direct children of this molecule
	MoleculeID string // If set, only return issues that are children of this molecule

	// Molecule type filtering
	MolType *MolType // Filter by molecule type (nil = any, swarm/patrol/work)

	// Wisp type filtering (TTL-based compaction classification)
	WispType *WispType // Filter by wisp type (nil = any, heartbeat/ping/patrol/gc_report/recovery/error/escalation)

	// Time-based deferral filtering (GH#820)
	IncludeDeferred bool // If true, include issues with future defer_until timestamps

	// Ephemeral issue filtering
	// By default, GetReadyWork excludes ephemeral issues (wisps).
	// Set to true to include them (e.g., for merge-request processing).
	IncludeEphemeral bool

	// Type exclusion: exclude issues with these types from results.
	// Appended to the default exclusion list (merge-request, gate, molecule, etc.).
	// When Type is set, ExcludeTypes is ignored (explicit type inclusion wins).
	ExcludeTypes []IssueType

	// Metadata field filtering (GH#1406)
	MetadataFields map[string]string // Top-level key=value equality; AND semantics (all must match)
	HasMetadataKey string            // Existence check: issue has this top-level key set (non-null)
}

// StaleFilter is used to filter stale issue queries
type StaleFilter struct {
	Days   int    // Issues not updated in this many days
	Status string // Filter by status (open|in_progress|blocked), empty = all non-closed
	Limit  int    // Maximum issues to return
}

// EpicStatus represents an epic with its completion status
type EpicStatus struct {
	Epic             *Issue `json:"epic"`
	TotalChildren    int    `json:"total_children"`
	ClosedChildren   int    `json:"closed_children"`
	EligibleForClose bool   `json:"eligible_for_close"`
}

// BondRef tracks compound molecule lineage.
// When protos or molecules are bonded together, BondRefs record
// which sources were combined and how they were attached.
type BondRef struct {
	SourceID  string `json:"source_id"`            // Source proto or molecule ID
	BondType  string `json:"bond_type"`            // sequential, parallel, conditional
	BondPoint string `json:"bond_point,omitempty"` // Attachment site (issue ID or empty for root)
}

// Bond type constants for compound molecules
const (
	BondTypeSequential  = "sequential"  // B runs after A completes
	BondTypeParallel    = "parallel"    // B runs alongside A
	BondTypeConditional = "conditional" // B runs only if A fails
	BondTypeRoot        = "root"        // Marks the primary/root component
)

// ID prefix constants for molecule/wisp instantiation.
// These prefixes are inserted into issue IDs: <project>-<prefix>-<id>
// Used by: cmd/bd/pour.go, cmd/bd/wisp.go (ID generation)
const (
	IDPrefixMol  = "mol"  // Persistent molecules (bd-mol-xxx)
	IDPrefixWisp = "wisp" // Ephemeral wisps (bd-wisp-xxx)
)

// IsCompound returns true if this issue is a compound (bonded from multiple sources).
func (i *Issue) IsCompound() bool {
	return len(i.BondedFrom) > 0
}

// GetConstituents returns the BondRefs for this compound's constituent protos.
// Returns nil for non-compound issues.
func (i *Issue) GetConstituents() []BondRef {
	return i.BondedFrom
}
