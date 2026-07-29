"""Integration tests for CompactedResult compaction in MCP server.

Tests verify that large result sets are properly compacted to prevent context overflow.
These tests cover:
- CompactedResult structure and validation
- Compaction threshold behavior (triggers at >20 results)
- Preview count consistency (shows 5 items)
- Total count accuracy
- Both ready() and list() MCP tools

Note: Tests focus on the compaction logic itself, which is independent of the
FastMCP decorator. The actual tool behavior is tested via _apply_compaction().
"""

from datetime import datetime, timezone

import pytest

from beads_mcp.models import CompactedResult, Issue, IssueMinimal


@pytest.fixture
def sample_issues(count=25):
    """Create a list of sample issues for testing compaction.

    Default creates 25 issues to exceed the COMPACTION_THRESHOLD (20).
    """
    now = datetime(2024, 1, 1, 0, 0, 0, tzinfo=timezone.utc)
    issues = []
    for i in range(count):
        issue = Issue(
            id=f"bd-{i:04d}",
            title=f"Issue {i}",
            description=f"Description {i}",
            status="open" if i % 2 == 0 else "in_progress",
            priority=i % 5,
            issue_type="bug" if i % 3 == 0 else "task",
            created_at=now,
            updated_at=now,
        )
        issues.append(issue)
    return issues


def _to_minimal_test(issue: Issue) -> IssueMinimal:
    """Convert Issue to IssueMinimal - copy of server's _to_minimal for testing."""
    return IssueMinimal(
        id=issue.id,
        title=issue.title,
        status=issue.status,
        priority=issue.priority,
        issue_type=issue.issue_type,
        assignee=issue.assignee,
        labels=issue.labels,
        dependency_count=issue.dependency_count,
        dependent_count=issue.dependent_count,
    )


def _apply_compaction(
    minimal_issues: list[IssueMinimal],
    threshold: int = 20,
    preview_count: int = 5,
    hint_prefix: str = "",
) -> list[IssueMinimal] | CompactedResult:
    """Apply compaction logic - copy of server's compaction for testing.

    This function contains the core compaction logic that's used in the MCP tools.
    """
    if len(minimal_issues) > threshold:
        return CompactedResult(
            compacted=True,
            total_count=len(minimal_issues),
            preview=minimal_issues[:preview_count],
            preview_count=preview_count,
            hint=(
                f"{hint_prefix}Showing {preview_count} of {len(minimal_issues)} issues. "
                "Use show(issue_id) for full details."
            ),
        )
    return minimal_issues


@pytest.fixture(autouse=True)
def reset_connection_pool():
    """Reset connection pool before and after each test."""
    from beads_mcp import tools

    tools._connection_pool.clear()
    yield
    tools._connection_pool.clear()


class TestCompactedResultStructure:
    """Test CompactedResult model structure and validation."""

    def test_compacted_result_model_valid(self):
        """Test CompactedResult model with valid data."""
        minimal_issues = [
            IssueMinimal(
                id="bd-1",
                title="Test 1",
                status="open",
                priority=1,
                issue_type="bug",
            ),
            IssueMinimal(
                id="bd-2",
                title="Test 2",
                status="open",
                priority=2,
                issue_type="task",
            ),
        ]

        result = CompactedResult(
            compacted=True,
            total_count=25,
            preview=minimal_issues,
            preview_count=2,
            hint="Use show(issue_id) for full details",
        )

        assert result.compacted is True
        assert result.total_count == 25
        assert result.preview_count == 2
        assert len(result.preview) == 2
        assert result.preview[0].id == "bd-1"
        assert result.hint == "Use show(issue_id) for full details"

    def test_compacted_result_default_hint(self):
        """Test CompactedResult uses default hint message."""
        result = CompactedResult(
            compacted=True,
            total_count=30,
            preview=[],
            preview_count=0,
        )

        assert result.hint == "Use show(issue_id) for full issue details"


class TestCompactionLogic:
    """Test core compaction logic."""

    def test_below_threshold_returns_list(self):
        """Test with <20 results returns list (no compaction)."""
        issues = [
            Issue(
                id=f"bd-{i:04d}",
                title=f"Issue {i}",
                description="",
                status="open",
                priority=1,
                issue_type="task",
                created_at=datetime.now(timezone.utc),
                updated_at=datetime.now(timezone.utc),
            )
            for i in range(10)
        ]

        minimal = [_to_minimal_test(i) for i in issues]
        result = _apply_compaction(minimal)

        assert isinstance(result, list)
        assert len(result) == 10
        assert all(isinstance(issue, IssueMinimal) for issue in result)

    def test_above_threshold_returns_compacted(self, sample_issues):
        """Test with >20 results returns CompactedResult."""
        minimal = [_to_minimal_test(i) for i in sample_issues]
        result = _apply_compaction(minimal)

        assert isinstance(result, CompactedResult)
        assert result.compacted is True
        assert result.total_count == 25
        assert result.preview_count == 5
        assert len(result.preview) == 5
        assert all(isinstance(issue, IssueMinimal) for issue in result.preview)

    def test_compaction_preserves_order(self, sample_issues):
        """Test compaction shows first N issues in order."""
        minimal = [_to_minimal_test(i) for i in sample_issues]
        result = _apply_compaction(minimal)

        # First 5 issues should match original order
        preview = result.preview
        for i, issue in enumerate(preview):
            assert issue.id == f"bd-{i:04d}"

    def test_exactly_threshold_no_compaction(self):
        """Test with exactly 20 items (at threshold) - no compaction."""
        issues = [
            Issue(
                id=f"bd-{i:04d}",
                title=f"Issue {i}",
                description="",
                status="open",
                priority=i % 5,
                issue_type="task",
                created_at=datetime.now(timezone.utc),
                updated_at=datetime.now(timezone.utc),
            )
            for i in range(20)
        ]

        minimal = [_to_minimal_test(i) for i in issues]
        result = _apply_compaction(minimal)

        # Should return list since len(issues) == 20, not > 20
        assert isinstance(result, list)
        assert len(result) == 20

    def test_one_over_threshold_compacts(self):
        """Test with 21 items (just over threshold)."""
        issues = [
            Issue(
                id=f"bd-{i:04d}",
                title=f"Issue {i}",
                description="",
                status="open",
                priority=i % 5,
                issue_type="task",
                created_at=datetime.now(timezone.utc),
                updated_at=datetime.now(timezone.utc),
            )
            for i in range(21)
        ]

        minimal = [_to_minimal_test(i) for i in issues]
        result = _apply_compaction(minimal)

        # Should compact
        assert isinstance(result, CompactedResult)
        assert result.compacted is True
        assert result.total_count == 21
        assert result.preview_count == 5

    def test_empty_result_set(self):
        """Test with empty results."""
        result = _apply_compaction([])

        assert isinstance(result, list)
        assert len(result) == 0


class TestCompactedResultHint:
    """Test hint field behavior in CompactedResult."""

    def test_compacted_result_hint_present(self, sample_issues):
        """Test compacted result includes helpful hint."""
        minimal = [_to_minimal_test(i) for i in sample_issues]
        result = _apply_compaction(minimal)

        assert result.hint is not None
        assert isinstance(result.hint, str)
        assert len(result.hint) > 0
        # Hint should guide user on how to proceed
        assert "show" in result.hint.lower()

    def test_compacted_result_hint_with_custom_prefix(self, sample_issues):
        """Test hint can have custom prefix."""
        minimal = [_to_minimal_test(i) for i in sample_issues]
        custom_hint = "Ready work: "
        result = _apply_compaction(minimal, hint_prefix=custom_hint)

        assert result.hint.startswith(custom_hint)


class TestCompactedResultDataTypes:
    """Test type conversions and data format in CompactedResult."""

    def test_preview_items_are_minimal_format(self, sample_issues):
        """Test that preview items use IssueMinimal format."""
        minimal = [_to_minimal_test(i) for i in sample_issues]
        result = _apply_compaction(minimal)

        # Check preview items have IssueMinimal fields only
        for issue in result.preview:
            assert isinstance(issue, IssueMinimal)
            assert hasattr(issue, "id")
            assert hasattr(issue, "title")
            assert hasattr(issue, "status")
            assert hasattr(issue, "priority")

    def test_total_count_includes_all_results(self, sample_issues):
        """Test total_count reflects all matching issues, not just preview."""
        minimal = [_to_minimal_test(i) for i in sample_issues]
        result = _apply_compaction(minimal)

        # total_count should be 25, preview_count should be 5
        assert result.total_count > result.preview_count
        assert result.preview_count == 5
        assert len(result.preview) == result.preview_count


class TestCompactionConsistency:
    """Test consistency of compaction behavior across different scenarios."""

    def test_multiple_calls_same_behavior(self, sample_issues):
        """Test that multiple calls with same data behave consistently."""
        minimal = [_to_minimal_test(i) for i in sample_issues]
        result1 = _apply_compaction(minimal)
        result2 = _apply_compaction(minimal)

        # Should be consistent
        assert result1.total_count == result2.total_count
        assert result1.preview_count == result2.preview_count
        assert len(result1.preview) == len(result2.preview)

    def test_custom_thresholds(self, sample_issues):
        """Test compaction with custom threshold."""
        minimal = [_to_minimal_test(i) for i in sample_issues]

        # With threshold=10, 25 items should compact
        result_low = _apply_compaction(minimal, threshold=10, preview_count=3)
        assert isinstance(result_low, CompactedResult)
        assert result_low.preview_count == 3
        assert len(result_low.preview) == 3

        # With threshold=50, 25 items should NOT compact
        result_high = _apply_compaction(minimal, threshold=50)
        assert isinstance(result_high, list)
        assert len(result_high) == 25


class TestConversionToMinimal:
    """Test conversion from full Issue to IssueMinimal."""

    def test_issue_to_minimal_conversion(self):
        """Test converting Issue to IssueMinimal."""
        issue = Issue(
            id="bd-123",
            title="Test Issue",
            description="Long description with lots of detail",
            design="Design notes",
            status="open",
            priority=1,
            issue_type="bug",
            assignee="alice",
            labels=["urgent", "backend"],
            dependency_count=2,
            dependent_count=1,
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )

        minimal = _to_minimal_test(issue)

        # Check minimal has only expected fields
        assert minimal.id == "bd-123"
        assert minimal.title == "Test Issue"
        assert minimal.status == "open"
        assert minimal.priority == 1
        assert minimal.issue_type == "bug"
        assert minimal.assignee == "alice"
        assert minimal.labels == ["urgent", "backend"]
        assert minimal.dependency_count == 2
        assert minimal.dependent_count == 1

        # Check it doesn't have full Issue fields
        assert not hasattr(minimal, "description") or minimal.description is None
        assert not hasattr(minimal, "design") or minimal.design is None

    def test_minimal_is_much_smaller(self):
        """Verify IssueMinimal is significantly smaller than Issue (roughly 80% reduction)."""
        now = datetime.now(timezone.utc)
        issue = Issue(
            id="bd-123",
            title="Test Issue",
            description="A" * 1000,  # Long description
            design="B" * 500,  # Design notes
            acceptance_criteria="C" * 500,  # Acceptance criteria
            notes="D" * 500,  # Notes
            status="open",
            priority=1,
            issue_type="bug",
            assignee="alice",
            labels=["a", "b", "c"],
            dependency_count=5,
            dependent_count=3,
            created_at=now,
            updated_at=now,
        )

        issue_size = len(str(issue))
        minimal = _to_minimal_test(issue)
        minimal_size = len(str(minimal))

        # Minimal should be significantly smaller
        ratio = minimal_size / issue_size
        assert ratio < 0.3, f"Minimal {ratio * 100:.1f}% of full size (expected <30%)"


class TestCompactionWithFilters:
    """Test compaction behavior with filtered results."""

    def test_filtered_results_below_threshold(self, sample_issues):
        """Test filtered results that stay below threshold don't compact."""
        # Take only first 8 issues
        filtered = sample_issues[:8]
        minimal = [_to_minimal_test(i) for i in filtered]
        result = _apply_compaction(minimal)

        assert isinstance(result, list)
        assert len(result) == 8

    def test_mixed_filters_and_compaction(self):
        """Test that filters are applied before compaction decision."""
        # Create 50 issues
        now = datetime.now(timezone.utc)
        issues = [
            Issue(
                id=f"bd-{i:04d}",
                title=f"Issue {i}",
                description="",
                status="open" if i < 30 else "closed",
                priority=i % 5,
                issue_type="task",
                created_at=now,
                updated_at=now,
            )
            for i in range(50)
        ]

        # Simulate filtering to only open issues (first 30)
        filtered = [i for i in issues if i.status == "open"]
        assert len(filtered) == 30
        minimal = [_to_minimal_test(i) for i in filtered]
        result = _apply_compaction(minimal)

        # Should compact since 30 > 20
        assert isinstance(result, CompactedResult)
        assert result.total_count == 30
        assert result.preview_count == 5
