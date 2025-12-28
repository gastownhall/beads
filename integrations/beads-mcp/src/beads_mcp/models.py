"""Pydantic models for beads issue tracker types."""

from datetime import datetime
from typing import Literal, Any

from pydantic import BaseModel, Field, field_validator

# Type aliases for issue statuses, types, and dependencies
IssueStatus = Literal["open", "in_progress", "blocked", "deferred", "closed"]
IssueType = Literal["bug", "feature", "task", "epic", "chore"]
DependencyType = Literal["blocks", "related", "parent-child", "discovered-from"]
OperationAction = Literal["created", "updated", "closed", "reopened"]


# =============================================================================
# CONTEXT ENGINEERING: Minimal Models for List Views
# =============================================================================
# These lightweight models reduce context window usage by ~80% for list operations.
# Use full Issue model only when detailed information is needed (show command).

class IssueMinimal(BaseModel):
    """Minimal issue model for list views (~80% smaller than full Issue).
    
    Use this for ready_work, list_issues, and other bulk operations.
    For full details including dependencies, use Issue model via show().
    """
    id: str
    title: str
    status: IssueStatus
    priority: int = Field(ge=0, le=4)
    issue_type: IssueType
    assignee: str | None = None
    labels: list[str] = Field(default_factory=list)
    dependency_count: int = 0
    dependent_count: int = 0

    @field_validator("priority")
    @classmethod
    def validate_priority(cls, v: int) -> int:
        if not 0 <= v <= 4:
            raise ValueError("Priority must be between 0 and 4")
        return v


class CompactedResult(BaseModel):
    """Result container for compacted list responses.

    When results exceed threshold, returns preview + metadata instead of full data.
    This prevents context window overflow for large issue lists.
    """
    compacted: bool = True
    total_count: int
    preview: list[IssueMinimal]
    preview_count: int
    hint: str = "Use show(issue_id) for full issue details"


class BriefIssue(BaseModel):
    """Ultra-minimal issue for scanning (4 fields).

    Use for quick scans where only identification + priority needed.
    ~95% smaller than full Issue.
    """
    id: str
    title: str
    status: IssueStatus
    priority: int = Field(ge=0, le=4)


class BriefDep(BaseModel):
    """Brief dependency for overview (5 fields).

    Use with brief_deps=True to get full issue but compact dependencies.
    ~90% smaller than full LinkedIssue.
    """
    id: str
    title: str
    status: IssueStatus
    priority: int = Field(ge=0, le=4)
    dependency_type: DependencyType | None = None


class OperationResult(BaseModel):
    """Minimal confirmation for write operations.

    Default response for create/update/close/reopen when verbose=False.
    ~97% smaller than returning full Issue object.
    """
    id: str
    action: OperationAction
    message: str | None = None


# =============================================================================
# ORIGINAL MODELS (unchanged for backward compatibility)
# =============================================================================

class IssueBase(BaseModel):
    """Base issue model with shared fields."""

    id: str
    title: str
    description: str = ""
    design: str | None = None
    acceptance_criteria: str | None = None
    notes: str | None = None
    external_ref: str | None = None
    status: IssueStatus
    priority: int = Field(ge=0, le=4)
    issue_type: IssueType
    created_at: datetime
    updated_at: datetime
    closed_at: datetime | None = None
    assignee: str | None = None
    labels: list[str] = Field(default_factory=list)
    dependency_count: int = 0
    dependent_count: int = 0

    @field_validator("priority")
    @classmethod
    def validate_priority(cls, v: int) -> int:
        """Validate priority is 0-4."""
        if not 0 <= v <= 4:
            raise ValueError("Priority must be between 0 and 4")
        return v


class LinkedIssue(IssueBase):
    """Issue reference in dependencies/dependents (avoids recursion)."""

    dependency_type: DependencyType | None = None


class Issue(IssueBase):
    """Issue model matching bd JSON output."""

    dependencies: list[LinkedIssue] = Field(default_factory=list)
    dependents: list[LinkedIssue] = Field(default_factory=list)


class Dependency(BaseModel):
    """Dependency relationship model."""

    from_id: str
    to_id: str
    dep_type: DependencyType


class CreateIssueParams(BaseModel):
    """Parameters for creating an issue."""

    title: str
    description: str = ""
    design: str | None = None
    acceptance: str | None = None
    external_ref: str | None = None
    priority: int = Field(default=2, ge=0, le=4)
    issue_type: IssueType = "task"
    assignee: str | None = None
    labels: list[str] = Field(default_factory=list)
    id: str | None = None
    deps: list[str] = Field(default_factory=list)


class UpdateIssueParams(BaseModel):
    """Parameters for updating an issue."""

    issue_id: str
    status: IssueStatus | None = None
    priority: int | None = Field(default=None, ge=0, le=4)
    assignee: str | None = None
    title: str | None = None
    description: str | None = None
    design: str | None = None
    acceptance_criteria: str | None = None
    notes: str | None = None
    external_ref: str | None = None


class CloseIssueParams(BaseModel):
    """Parameters for closing an issue."""

    issue_id: str
    reason: str = "Completed"


class ReopenIssueParams(BaseModel):
    """Parameters for reopening issues."""

    issue_ids: list[str]
    reason: str | None = None


class AddDependencyParams(BaseModel):
    """Parameters for adding a dependency."""

    issue_id: str
    depends_on_id: str
    dep_type: DependencyType = "blocks"


class ReadyWorkParams(BaseModel):
    """Parameters for querying ready work."""

    limit: int = Field(default=10, ge=1, le=100)
    priority: int | None = Field(default=None, ge=0, le=4)
    assignee: str | None = None
    labels: list[str] | None = None  # AND: must have ALL labels
    labels_any: list[str] | None = None  # OR: must have at least one
    unassigned: bool = False  # Filter to only unassigned issues
    sort_policy: str | None = None  # hybrid, priority, oldest
    parent_id: str | None = None  # Filter to descendants of this bead/epic


class BlockedParams(BaseModel):
    """Parameters for querying blocked issues."""

    parent_id: str | None = None  # Filter to descendants of this bead/epic


class ListIssuesParams(BaseModel):
    """Parameters for listing issues."""

    status: IssueStatus | None = None
    priority: int | None = Field(default=None, ge=0, le=4)
    issue_type: IssueType | None = None
    assignee: str | None = None
    labels: list[str] | None = None  # AND: must have ALL labels
    labels_any: list[str] | None = None  # OR: must have at least one
    query: str | None = None  # Search in title (case-insensitive)
    unassigned: bool = False  # Filter to only unassigned issues
    limit: int = Field(default=20, ge=1, le=100)  # Reduced to avoid MCP buffer overflow


class ShowIssueParams(BaseModel):
    """Parameters for showing issue details."""

    issue_id: str


class Stats(BaseModel):
    """Beads task statistics."""

    total_issues: int
    open_issues: int
    in_progress_issues: int
    closed_issues: int
    blocked_issues: int
    ready_issues: int
    average_lead_time_hours: float


class BlockedIssue(Issue):
    """Blocked issue with blocking information."""

    blocked_by_count: int
    blocked_by: list[str]


class InitParams(BaseModel):
    """Parameters for initializing bd."""

    prefix: str | None = None


class InitResult(BaseModel):
    """Result from bd init command."""

    database: str
    prefix: str
    message: str
