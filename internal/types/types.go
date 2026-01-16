// Package types defines core data structures for the bd issue tracker.
package types

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"strings"
	"time"
)

// Issue represents a trackable work item.
// Fields are organized into logical groups for maintainability.
type Issue struct {
	// ===== Core Identification =====
	ID          string `json:"id"`
	ContentHash string `json:"-"` // Internal: SHA256 of canonical content - NOT exported to JSONL

	// ===== Issue Content =====
	Title              string `json:"title"`
	Description        string `json:"description,omitempty"`
	Design             string `json:"design,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Notes              string `json:"notes,omitempty"`

	// ===== Status & Workflow =====
	Status    Status    `json:"status,omitempty"`
	Priority  int       `json:"priority"` // No omitempty: 0 is valid (P0/critical)
	IssueType IssueType `json:"issue_type,omitempty"`

	// ===== Assignment =====
	Assignee         string `json:"assignee,omitempty"`
	Owner            string `json:"owner,omitempty"` // Human owner for CV attribution (git author email)
	EstimatedMinutes *int   `json:"estimated_minutes,omitempty"`

	// ===== Timestamps =====
	CreatedAt   time.Time  `json:"created_at"`
	CreatedBy   string     `json:"created_by,omitempty"` // Who created this issue (GH#748)
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
	CloseReason     string     `json:"close_reason,omitempty"`      // Reason provided when closing
	ClosedBySession string     `json:"closed_by_session,omitempty"` // Claude Code session that closed this issue

	// ===== Time-Based Scheduling (GH#820) =====
	DueAt      *time.Time `json:"due_at,omitempty"`      // When this issue should be completed
	DeferUntil *time.Time `json:"defer_until,omitempty"` // Hide from bd ready until this time

	// ===== External Integration =====
	ExternalRef  *string `json:"external_ref,omitempty"`  // e.g., "gh-9", "jira-ABC"
	SourceSystem string  `json:"source_system,omitempty"` // Adapter/system that created this issue (federation)

	// ===== Compaction Metadata =====
	CompactionLevel   int        `json:"compaction_level,omitempty"`
	CompactedAt       *time.Time `json:"compacted_at,omitempty"`
	CompactedAtCommit *string    `json:"compacted_at_commit,omitempty"` // Git commit hash when compacted
	OriginalSize      int        `json:"original_size,omitempty"`

	// ===== Internal Routing (not exported to JSONL) =====
	SourceRepo     string `json:"-"` // Which repo owns this issue (multi-repo support)
	IDPrefix       string `json:"-"` // Override prefix for ID generation (appends to config prefix)
	PrefixOverride string `json:"-"` // Completely replace config prefix (for cross-rig creation)

	// ===== Relational Data (populated for export/import) =====
	Labels       []string      `json:"labels,omitempty"`
	Dependencies []*Dependency `json:"dependencies,omitempty"`
	Comments     []*Comment    `json:"comments,omitempty"`

	// ===== Tombstone Fields (soft-delete support) =====
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`    // When deleted
	DeletedBy    string     `json:"deleted_by,omitempty"`    // Who deleted
	DeleteReason string     `json:"delete_reason,omitempty"` // Why deleted
	OriginalType string     `json:"original_type,omitempty"` // Issue type before deletion

	// ===== Messaging Fields (inter-agent communication) =====
	Sender    string `json:"sender,omitempty"`    // Who sent this (for messages)
	Ephemeral bool   `json:"ephemeral,omitempty"` // If true, not exported to JSONL
	// NOTE: RepliesTo, RelatesTo, DuplicateOf, SupersededBy moved to dependencies table
	// per Decision 004 (Edge Schema Consolidation). Use dependency API instead.

	// ===== Context Markers =====
	Pinned     bool `json:"pinned,omitempty"`      // Persistent context marker, not a work item
	IsTemplate bool `json:"is_template,omitempty"` // Read-only template molecule

	// ===== Bonding Fields (compound molecule lineage) =====
	BondedFrom []BondRef `json:"bonded_from,omitempty"` // For compounds: constituent protos

	// ===== HOP Fields (entity tracking for CV chains) =====
	Creator      *EntityRef   `json:"creator,omitempty"`       // Who created (human, agent, or org)
	Validations  []Validation `json:"validations,omitempty"`   // Who validated/approved
	QualityScore *float32     `json:"quality_score,omitempty"` // Aggregate quality (0.0-1.0), set by Refineries on merge
	Crystallizes bool         `json:"crystallizes,omitempty"`  // Work that compounds (true: code, features) vs evaporates (false: ops, support) - affects CV weighting per Decision 006

	// ===== Gate Fields (async coordination primitives) =====
	AwaitType string        `json:"await_type,omitempty"` // Condition type: gh:run, gh:pr, timer, human, mail
	AwaitID   string        `json:"await_id,omitempty"`   // Condition identifier (run ID, PR number, etc.)
	Timeout   time.Duration `json:"timeout,omitempty"`    // Max wait time before escalation
	Waiters   []string      `json:"waiters,omitempty"`    // Mail addresses to notify when gate clears

	// ===== Slot Fields (exclusive access primitives) =====
	Holder string `json:"holder,omitempty"` // Who currently holds the slot (empty = available)

	// ===== Source Tracing Fields (formula cooking origin) =====
	SourceFormula  string `json:"source_formula,omitempty"`  // Formula name where step was defined
	SourceLocation string `json:"source_location,omitempty"` // Path: "steps[0]", "advice[0].after"

	// ===== Agent Identity Fields (agent-as-bead support) =====
	HookBead     string     `json:"hook_bead,omitempty"`     // Current work on agent's hook (0..1)
	RoleBead     string     `json:"role_bead,omitempty"`     // Role definition bead (required for agents)
	AgentState   AgentState `json:"agent_state,omitempty"`   // Agent state: idle|running|stuck|stopped
	LastActivity *time.Time `json:"last_activity,omitempty"` // Updated on each action (timeout detection)
	RoleType     string     `json:"role_type,omitempty"`     // Role: polecat|crew|witness|refinery|mayor|deacon
	Rig          string     `json:"rig,omitempty"`           // Rig name (empty for town-level agents)

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
	w.flag(i.IsTemplate, "template")

	// Bonded molecules
	for _, br := range i.BondedFrom {
		w.str(br.SourceID)
		w.str(br.BondType)
		w.str(br.BondPoint)
	}

	// HOP entity tracking
	w.entityRef(i.Creator)

	// HOP validations
	for _, v := range i.Validations {
		w.entityRef(v.Validator)
		w.str(v.Outcome)
		w.str(v.Timestamp.Format(time.RFC3339))
		w.float32Ptr(v.Score)
	}

	// HOP aggregate quality score and crystallizes
	w.float32Ptr(i.QualityScore)
	w.flag(i.Crystallizes, "crystallizes")

	// Gate fields for async coordination
	w.str(i.AwaitType)
	w.str(i.AwaitID)
	w.duration(i.Timeout)
	for _, waiter := range i.Waiters {
		w.str(waiter)
	}

	// Slot fields for exclusive access
	w.str(i.Holder)

	// Agent identity fields
	w.str(i.HookBead)
	w.str(i.RoleBead)
	w.str(string(i.AgentState))
	w.str(i.RoleType)
	w.str(i.Rig)

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

func (w hashFieldWriter) float32Ptr(p *float32) {
	if p != nil {
		w.h.Write([]byte(fmt.Sprintf("%f", *p)))
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

func (w hashFieldWriter) entityRef(e *EntityRef) {
	if e != nil {
		w.str(e.Name)
		w.str(e.Platform)
		w.str(e.Org)
		w.str(e.ID)
	}
}

// DefaultTombstoneTTL is the default time-to-live for tombstones (30 days)
const DefaultTombstoneTTL = 30 * 24 * time.Hour

// MinTombstoneTTL is the minimum allowed TTL (7 days) to prevent data loss
const MinTombstoneTTL = 7 * 24 * time.Hour

// ClockSkewGrace is added to TTL to handle clock drift between machines
const ClockSkewGrace = 1 * time.Hour

// IsTombstone returns true if the issue has been soft-deleted
func (i *Issue) IsTombstone() bool {
	return i.Status == StatusTombstone
}

// IsExpired returns true if the tombstone has exceeded its TTL.
// Non-tombstone issues always return false.
// ttl is the configured TTL duration:
//   - If zero, DefaultTombstoneTTL (30 days) is used
//   - If negative, the tombstone is immediately expired (for --hard mode)
//   - If positive, ClockSkewGrace is added only for TTLs > 1 hour
func (i *Issue) IsExpired(ttl time.Duration) bool {
	// Non-tombstones never expire
	if !i.IsTombstone() {
		return false
	}

	// Tombstones without DeletedAt are not expired (safety: shouldn't happen in valid data)
	if i.DeletedAt == nil {
		return false
	}

	// Negative TTL means "immediately expired" - for --hard mode
	if ttl < 0 {
		return true
	}

	// Use default TTL if not specified
	if ttl == 0 {
		ttl = DefaultTombstoneTTL
	}

	// Only add clock skew grace period for normal TTLs (> 1 hour).
	// For short TTLs (testing/development), skip grace period.
	effectiveTTL := ttl
	if ttl > ClockSkewGrace {
		effectiveTTL = ttl + ClockSkewGrace
	}

	// Check if the tombstone has exceeded its TTL
	expirationTime := i.DeletedAt.Add(effectiveTTL)
	return time.Now().After(expirationTime)
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
	// Exception: tombstones may retain closed_at from before deletion
	if i.Status == StatusClosed && i.ClosedAt == nil {
		return fmt.Errorf("closed issues must have closed_at timestamp")
	}
	if i.Status != StatusClosed && i.Status != StatusTombstone && i.ClosedAt != nil {
		return fmt.Errorf("non-closed issues cannot have closed_at timestamp")
	}
	// Enforce tombstone invariants: deleted_at must be set for tombstones, and only for tombstones
	if i.Status == StatusTombstone && i.DeletedAt == nil {
		return fmt.Errorf("tombstone issues must have deleted_at timestamp")
	}
	if i.Status != StatusTombstone && i.DeletedAt != nil {
		return fmt.Errorf("non-tombstone issues cannot have deleted_at timestamp")
	}
	// Validate agent state if set
	if !i.AgentState.IsValid() {
		return fmt.Errorf("invalid agent state: %s", i.AgentState)
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
	if i.Status != StatusClosed && i.Status != StatusTombstone && i.ClosedAt != nil {
		return fmt.Errorf("non-closed issues cannot have closed_at timestamp")
	}
	// Enforce tombstone invariants
	if i.Status == StatusTombstone && i.DeletedAt == nil {
		return fmt.Errorf("tombstone issues must have deleted_at timestamp")
	}
	if i.Status != StatusTombstone && i.DeletedAt != nil {
		return fmt.Errorf("non-tombstone issues cannot have deleted_at timestamp")
	}
	// Validate agent state if set
	if !i.AgentState.IsValid() {
		return fmt.Errorf("invalid agent state: %s", i.AgentState)
	}
	return nil
}

// SetDefaults applies default values for fields omitted during JSONL import.
// Call this after json.Unmarshal to ensure missing fields have proper defaults:
//   - Status: defaults to StatusOpen if empty
//   - Priority: defaults to 2 if zero (note: P0 issues must explicitly set priority=0)
//   - IssueType: defaults to TypeTask if empty
//
// This enables smaller JSONL output by using omitempty on these fields.
func (i *Issue) SetDefaults() {
	if i.Status == "" {
		i.Status = StatusOpen
	}
	// Note: priority 0 (P0) is a valid value, so we can't distinguish between
	// "explicitly set to 0" and "omitted". For JSONL compactness, we treat
	// priority 0 in JSONL as P0, not as "use default". This is the expected
	// behavior since P0 issues are explicitly marked.
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
	StatusTombstone  Status = "tombstone" // Soft-deleted issue
	StatusPinned     Status = "pinned"    // Persistent bead that stays open indefinitely
	StatusHooked     Status = "hooked"    // Work attached to an agent's hook (GUPP)
)

// IsValid checks if the status value is valid (built-in statuses only)
func (s Status) IsValid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked, StatusDeferred, StatusClosed, StatusTombstone, StatusPinned, StatusHooked:
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

// IssueType categorizes the kind of work
type IssueType string

// Issue type constants
const (
	TypeBug          IssueType = "bug"
	TypeFeature      IssueType = "feature"
	TypeTask         IssueType = "task"
	TypeEpic         IssueType = "epic"
	TypeChore        IssueType = "chore"
	TypeMessage      IssueType = "message"       // Ephemeral communication between workers
	TypeMergeRequest IssueType = "merge-request" // Merge queue entry for refinery processing
	TypeMolecule     IssueType = "molecule"      // Template molecule for issue hierarchies
	TypeGate         IssueType = "gate"          // Async coordination gate
	TypeAgent        IssueType = "agent"         // Agent identity bead
	TypeRole         IssueType = "role"          // Agent role definition
	TypeRig          IssueType = "rig"           // Rig identity bead (multi-repo workspace)
	TypeConvoy       IssueType = "convoy"        // Cross-project tracking with reactive completion
	TypeEvent        IssueType = "event"         // Operational state change record
	TypeSlot         IssueType = "slot"          // Exclusive access slot (merge-slot gate)
)

// IsValid checks if the issue type value is valid
func (t IssueType) IsValid() bool {
	switch t {
	case TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore, TypeMessage, TypeMergeRequest, TypeMolecule, TypeGate, TypeAgent, TypeRole, TypeRig, TypeConvoy, TypeEvent, TypeSlot:
		return true
	}
	return false
}

// IsBuiltIn returns true if the type is a built-in type (same as IsValid).
// Used during multi-repo hydration to determine trust:
// - Built-in types: validate (catch typos)
// - Custom types (!IsBuiltIn): trust from source repo
func (t IssueType) IsBuiltIn() bool {
	return t.IsValid()
}

// IsValidWithCustom checks if the issue type is valid, including custom types.
// Custom types are user-defined via bd config set types.custom "type1,type2,..."
func (t IssueType) IsValidWithCustom(customTypes []string) bool {
	// First check built-in types
	if t.IsValid() {
		return true
	}
	// Then check custom types
	for _, custom := range customTypes {
		if string(t) == custom {
			return true
		}
	}
	return false
}

// Normalize maps issue type aliases to their canonical form.
// For example, "enhancement" -> "feature".
func (t IssueType) Normalize() IssueType {
	switch t {
	case "enhancement":
		return TypeFeature
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
	default:
		// Chore, message, molecule, gate, agent, role, convoy, event, merge-request
		// have no required sections
		return nil
	}
}

// AgentState represents the self-reported state of an agent
type AgentState string

// Agent state constants
const (
	StateIdle     AgentState = "idle"     // Agent is waiting for work
	StateSpawning AgentState = "spawning" // Agent is starting up
	StateRunning  AgentState = "running"  // Agent is executing (general)
	StateWorking  AgentState = "working"  // Agent is actively working on a task
	StateStuck    AgentState = "stuck"    // Agent is blocked and needs help
	StateDone     AgentState = "done"     // Agent completed its current work
	StateStopped  AgentState = "stopped"  // Agent has cleanly shut down
	StateDead     AgentState = "dead"     // Agent died without clean shutdown (timeout detection)
)

// IsValid checks if the agent state value is valid
func (s AgentState) IsValid() bool {
	switch s {
	case StateIdle, StateSpawning, StateRunning, StateWorking, StateStuck, StateDone, StateStopped, StateDead, "":
		return true // empty is valid (non-agent beads)
	}
	return false
}

// MolType categorizes the molecule type for swarm coordination
type MolType string

// MolType constants
const (
	MolTypeSwarm  MolType = "swarm"  // Swarm molecule: coordinated multi-polecat work
	MolTypePatrol MolType = "patrol" // Patrol molecule: recurring operational work (Witness, Deacon, etc.)
	MolTypeWork   MolType = "work"   // Work molecule: regular polecat work (default)
)

// IsValid checks if the mol type value is valid
func (m MolType) IsValid() bool {
	switch m {
	case MolTypeSwarm, MolTypePatrol, MolTypeWork, "":
		return true // empty is valid (defaults to work)
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
	DependencyCount int `json:"dependency_count"`
	DependentCount  int `json:"dependent_count"`
}

// IssueDetails extends Issue with labels, dependencies, dependents, and comments.
// Used for JSON serialization in bd show and RPC responses.
type IssueDetails struct {
	Issue
	Labels       []string                      `json:"labels,omitempty"`
	Dependencies []*IssueWithDependencyMetadata `json:"dependencies,omitempty"`
	Dependents   []*IssueWithDependencyMetadata `json:"dependents,omitempty"`
	Comments     []*Comment                     `json:"comments,omitempty"`
	Parent       *string                        `json:"parent,omitempty"`
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
	DepRepliesTo  DependencyType = "replies-to"  // Conversation threading
	DepRelatesTo  DependencyType = "relates-to"  // Loose knowledge graph edges
	DepDuplicates DependencyType = "duplicates"  // Deduplication link
	DepSupersedes DependencyType = "supersedes"  // Version chain link

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
	ID        int64     `json:"id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Event represents an audit trail entry
type Event struct {
	ID        int64      `json:"id"`
	IssueID   string     `json:"issue_id"`
	EventType EventType  `json:"event_type"`
	Actor     string     `json:"actor"`
	OldValue  *string    `json:"old_value,omitempty"`
	NewValue  *string    `json:"new_value,omitempty"`
	Comment   *string    `json:"comment,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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

// Statistics provides aggregate metrics
type Statistics struct {
	TotalIssues              int     `json:"total_issues"`
	OpenIssues               int     `json:"open_issues"`
	InProgressIssues         int     `json:"in_progress_issues"`
	ClosedIssues             int     `json:"closed_issues"`
	BlockedIssues            int     `json:"blocked_issues"`
	DeferredIssues           int     `json:"deferred_issues"`  // Issues on ice
	ReadyIssues              int     `json:"ready_issues"`
	TombstoneIssues          int     `json:"tombstone_issues"` // Soft-deleted issues
	PinnedIssues             int     `json:"pinned_issues"`    // Persistent issues
	EpicsEligibleForClosure  int     `json:"epics_eligible_for_closure"`
	AverageLeadTime          float64 `json:"average_lead_time_hours"`
}

// IssueFilter is used to filter issue queries
type IssueFilter struct {
	Status      *Status
	Priority    *int
	IssueType   *IssueType
	Assignee    *string
	Labels      []string  // AND semantics: issue must have ALL these labels
	LabelsAny   []string  // OR semantics: issue must have AT LEAST ONE of these labels
	TitleSearch string
	IDs         []string  // Filter by specific issue IDs
	IDPrefix    string    // Filter by ID prefix (e.g., "bd-" to match "bd-abc123")
	Limit       int

	// Pattern matching
	TitleContains       string
	DescriptionContains string
	NotesContains       string
	
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

	// Tombstone filtering
	IncludeTombstones bool // If false (default), exclude tombstones from results

	// Ephemeral filtering
	Ephemeral *bool // Filter by ephemeral flag (nil = any, true = only ephemeral, false = only persistent)

	// Pinned filtering
	Pinned *bool // Filter by pinned flag (nil = any, true = only pinned, false = only non-pinned)

	// Template filtering
	IsTemplate *bool // Filter by template flag (nil = any, true = only templates, false = exclude templates)

	// Parent filtering: filter children by parent issue ID
	ParentID *string // Filter by parent issue (via parent-child dependency)

	// Molecule type filtering
	MolType *MolType // Filter by molecule type (nil = any, swarm/patrol/work)

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
	Status     Status
	Type       string     // Filter by issue type (task, bug, feature, epic, merge-request, etc.)
	Priority   *int
	Assignee   *string
	Unassigned bool       // Filter for issues with no assignee
	Labels     []string   // AND semantics: issue must have ALL these labels
	LabelsAny  []string   // OR semantics: issue must have AT LEAST ONE of these labels
	Limit      int
	SortPolicy SortPolicy

	// Parent filtering: filter to descendants of a bead/epic (recursive)
	ParentID *string // Show all descendants of this issue

	// Molecule type filtering
	MolType *MolType // Filter by molecule type (nil = any, swarm/patrol/work)

	// Time-based deferral filtering (GH#820)
	IncludeDeferred bool // If true, include issues with future defer_until timestamps
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

// IsCompound returns true if this issue is a compound (bonded from multiple sources).
func (i *Issue) IsCompound() bool {
	return len(i.BondedFrom) > 0
}

// GetConstituents returns the BondRefs for this compound's constituent protos.
// Returns nil for non-compound issues.
func (i *Issue) GetConstituents() []BondRef {
	return i.BondedFrom
}

// EntityRef is a structured reference to an entity (human, agent, or org).
// This is the foundation for HOP entity tracking and CV chains.
// Can be rendered as a URI: entity://hop/<platform>/<org>/<id>
//
// Example usage:
//
//	ref := &EntityRef{
//	    Name:     "polecat/Nux",
//	    Platform: "gastown",
//	    Org:      "steveyegge",
//	    ID:       "polecat-nux",
//	}
//	uri := ref.URI() // "entity://hop/gastown/steveyegge/polecat-nux"
type EntityRef struct {
	// Name is the human-readable identifier (e.g., "polecat/Nux", "mayor")
	Name string `json:"name,omitempty"`

	// Platform identifies the execution context (e.g., "gastown", "github")
	Platform string `json:"platform,omitempty"`

	// Org identifies the organization (e.g., "steveyegge", "anthropics")
	Org string `json:"org,omitempty"`

	// ID is the unique identifier within the platform/org (e.g., "polecat-nux")
	ID string `json:"id,omitempty"`
}

// IsEmpty returns true if all fields are empty.
func (e *EntityRef) IsEmpty() bool {
	if e == nil {
		return true
	}
	return e.Name == "" && e.Platform == "" && e.Org == "" && e.ID == ""
}

// URI returns the entity as a HOP URI.
// Format: entity://hop/<platform>/<org>/<id>
// Returns empty string if Platform, Org, or ID is missing.
func (e *EntityRef) URI() string {
	if e == nil || e.Platform == "" || e.Org == "" || e.ID == "" {
		return ""
	}
	return fmt.Sprintf("entity://hop/%s/%s/%s", e.Platform, e.Org, e.ID)
}

// String returns a human-readable representation.
// Prefers Name if set, otherwise returns URI or ID.
func (e *EntityRef) String() string {
	if e == nil {
		return ""
	}
	if e.Name != "" {
		return e.Name
	}
	if uri := e.URI(); uri != "" {
		return uri
	}
	return e.ID
}

// Validation records who validated/approved work completion.
// This is core to HOP's proof-of-stake concept - validators stake
// their reputation on approvals.
type Validation struct {
	// Validator is who approved/rejected the work
	Validator *EntityRef `json:"validator"`

	// Outcome is the validation result: accepted, rejected, revision_requested
	Outcome string `json:"outcome"`

	// Timestamp is when the validation occurred
	Timestamp time.Time `json:"timestamp"`

	// Score is an optional quality score (0.0-1.0)
	Score *float32 `json:"score,omitempty"`
}

// Validation outcome constants
const (
	ValidationAccepted          = "accepted"
	ValidationRejected          = "rejected"
	ValidationRevisionRequested = "revision_requested"
)

// IsValidOutcome checks if the outcome is a known validation outcome.
func (v *Validation) IsValidOutcome() bool {
	switch v.Outcome {
	case ValidationAccepted, ValidationRejected, ValidationRevisionRequested:
		return true
	}
	return false
}

// ParseEntityURI parses a HOP entity URI into an EntityRef.
// Format: entity://hop/<platform>/<org>/<id>
// Returns nil and error if the URI is invalid.
func ParseEntityURI(uri string) (*EntityRef, error) {
	const prefix = "entity://hop/"
	if !strings.HasPrefix(uri, prefix) {
		return nil, fmt.Errorf("invalid entity URI: must start with %q", prefix)
	}

	rest := uri[len(prefix):]
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("invalid entity URI: expected entity://hop/<platform>/<org>/<id>, got %q", uri)
	}

	return &EntityRef{
		Platform: parts[0],
		Org:      parts[1],
		ID:       parts[2],
	}, nil
}
