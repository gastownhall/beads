package backend

import (
	"github.com/steveyegge/beads/internal/types"
)

// Domain types reached by the engine interface's method signatures, aliased
// from the internal domain package. Request/response types from the guarded
// issue-operations surface (issueops.Lifecycle, issueops.Reader) are already
// public in github.com/steveyegge/beads/issueops and are not re-exported
// here.
type (
	BlockedIssue                = types.BlockedIssue
	BondRef                     = types.BondRef
	Comment                     = types.Comment
	CompactionCandidate         = types.CompactionCandidate
	CustomStatus                = types.CustomStatus
	DeleteIssuesResult          = types.DeleteIssuesResult
	Dependency                  = types.Dependency
	DependencyCounts            = types.DependencyCounts
	DependencyType              = types.DependencyType
	EpicStatus                  = types.EpicStatus
	Event                       = types.Event
	EventType                   = types.EventType
	Issue                       = types.Issue
	IssueDetails                = types.IssueDetails
	IssueFilter                 = types.IssueFilter
	IssueSnapshot               = types.IssueSnapshot
	IssueType                   = types.IssueType
	IssueWithCounts             = types.IssueWithCounts
	IssueWithDependencyMetadata = types.IssueWithDependencyMetadata
	Label                       = types.Label
	MolType                     = types.MolType
	MoleculeLastActivity        = types.MoleculeLastActivity
	MoleculeProgressStats       = types.MoleculeProgressStats
	PersistenceMode             = types.PersistenceMode
	ReclaimFilter               = types.ReclaimFilter
	ReclaimedLease              = types.ReclaimedLease
	SortPolicy                  = types.SortPolicy
	StaleFilter                 = types.StaleFilter
	Statistics                  = types.Statistics
	Status                      = types.Status
	StatusCategory              = types.StatusCategory
	StorageClass                = types.StorageClass
	TreeNode                    = types.TreeNode
	WispFilter                  = types.WispFilter
	WispType                    = types.WispType
	WorkFilter                  = types.WorkFilter
	WorkType                    = types.WorkType
)

// Status values.
const (
	StatusOpen       = types.StatusOpen
	StatusInProgress = types.StatusInProgress
	StatusBlocked    = types.StatusBlocked
	StatusDeferred   = types.StatusDeferred
	StatusClosed     = types.StatusClosed
	StatusHooked     = types.StatusHooked
	StatusPinned     = types.StatusPinned
)

// IssueType values.
const (
	TypeBug       = types.TypeBug
	TypeChore     = types.TypeChore
	TypeDecision  = types.TypeDecision
	TypeEpic      = types.TypeEpic
	TypeEvent     = types.TypeEvent
	TypeFeature   = types.TypeFeature
	TypeGate      = types.TypeGate
	TypeMessage   = types.TypeMessage
	TypeMilestone = types.TypeMilestone
	TypeMolecule  = types.TypeMolecule
	TypeSpike     = types.TypeSpike
	TypeStory     = types.TypeStory
	TypeTask      = types.TypeTask
)

// DependencyType values.
const (
	DepApprovedBy        = types.DepApprovedBy
	DepAssignedTo        = types.DepAssignedTo
	DepAttests           = types.DepAttests
	DepAuthoredBy        = types.DepAuthoredBy
	DepBlocks            = types.DepBlocks
	DepCausedBy          = types.DepCausedBy
	DepConditionalBlocks = types.DepConditionalBlocks
	DepDelegatedFrom     = types.DepDelegatedFrom
	DepDiscoveredFrom    = types.DepDiscoveredFrom
	DepDuplicates        = types.DepDuplicates
	DepParentChild       = types.DepParentChild
	DepRelated           = types.DepRelated
	DepRelatesTo         = types.DepRelatesTo
	DepRepliesTo         = types.DepRepliesTo
	DepSupersedes        = types.DepSupersedes
	DepTracks            = types.DepTracks
	DepUntil             = types.DepUntil
	DepValidates         = types.DepValidates
	DepWaitsFor          = types.DepWaitsFor
)

// EventType values.
const (
	EventCreated           = types.EventCreated
	EventUpdated           = types.EventUpdated
	EventClaimed           = types.EventClaimed
	EventStatusChanged     = types.EventStatusChanged
	EventCommented         = types.EventCommented
	EventClosed            = types.EventClosed
	EventReopened          = types.EventReopened
	EventDependencyAdded   = types.EventDependencyAdded
	EventDependencyRemoved = types.EventDependencyRemoved
	EventLabelAdded        = types.EventLabelAdded
	EventLabelRemoved      = types.EventLabelRemoved
	EventLeaseReclaimed    = types.EventLeaseReclaimed
	EventCompacted         = types.EventCompacted
)

// SortPolicy values.
const (
	SortPolicyHybrid   = types.SortPolicyHybrid
	SortPolicyPriority = types.SortPolicyPriority
	SortPolicyOldest   = types.SortPolicyOldest
)

// StatusCategory values.
const (
	CategoryUnspecified = types.CategoryUnspecified
	CategoryActive      = types.CategoryActive
	CategoryWIP         = types.CategoryWIP
	CategoryFrozen      = types.CategoryFrozen
	CategoryDone        = types.CategoryDone
)

// StorageClass values.
const (
	StorageClassVersioned   = types.StorageClassVersioned
	StorageClassUnversioned = types.StorageClassUnversioned
	StorageClassEphemeral   = types.StorageClassEphemeral
)

// WispType values.
const (
	WispTypeHeartbeat  = types.WispTypeHeartbeat
	WispTypePing       = types.WispTypePing
	WispTypePatrol     = types.WispTypePatrol
	WispTypeGCReport   = types.WispTypeGCReport
	WispTypeRecovery   = types.WispTypeRecovery
	WispTypeError      = types.WispTypeError
	WispTypeEscalation = types.WispTypeEscalation
)

// PersistenceMode values.
const (
	PersistenceModePersistent = types.PersistenceModePersistent
	PersistenceModeEphemeral  = types.PersistenceModeEphemeral
	PersistenceModeNoHistory  = types.PersistenceModeNoHistory
)

// MolType values.
const (
	MolTypeWork   = types.MolTypeWork
	MolTypeSwarm  = types.MolTypeSwarm
	MolTypePatrol = types.MolTypePatrol
)

// WorkType values.
const (
	WorkTypeMutex           = types.WorkTypeMutex
	WorkTypeOpenCompetition = types.WorkTypeOpenCompetition
)

// MaxFieldLen is the maximum length (in characters) of the assignee, owner,
// and label fields; violations pair with ErrFieldTooLong.
const MaxFieldLen = types.MaxFieldLen

// Enumerations of the built-in values, in canonical order.
var (
	AllStatuses        = types.AllStatuses
	AllIssueTypes      = types.AllIssueTypes
	AllDependencyTypes = types.AllDependencyTypes
)
