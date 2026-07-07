"""Tests for configurable compaction settings via environment variables.

These tests verify that BEADS_MCP_COMPACTION_THRESHOLD and BEADS_MCP_PREVIEW_COUNT
can be configured via environment variables.
"""

import os
import subprocess
import sys
from pathlib import Path

import pytest

MCP_ROOT = Path(__file__).resolve().parents[1]


def _python_env() -> dict[str, str]:
    """Return an environment that imports the local source tree in subprocesses."""
    env = os.environ.copy()
    src_path = str(MCP_ROOT / "src")
    existing_pythonpath = env.get("PYTHONPATH")
    env["PYTHONPATH"] = src_path if not existing_pythonpath else f"{src_path}{os.pathsep}{existing_pythonpath}"
    return env


class TestCompactionConfigEnvironmentVariables:
    """Test environment variable configuration for compaction settings."""

    def test_default_compaction_threshold(self):
        """Test default COMPACTION_THRESHOLD is 20."""
        # Import in a clean environment with the local source tree importable
        env = _python_env()
        env.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)
        env.pop("BEADS_MCP_PREVIEW_COUNT", None)

        code = """
import sys
import os
# Make sure env vars are not set
os.environ.pop('BEADS_MCP_COMPACTION_THRESHOLD', None)
os.environ.pop('BEADS_MCP_PREVIEW_COUNT', None)

# Import server module (will load defaults)
from beads_mcp import server
print(f"threshold={server.COMPACTION_THRESHOLD}")
print(f"preview={server.PREVIEW_COUNT}")
"""
        result = subprocess.run(
            [sys.executable, "-c", code],
            env=env,
            capture_output=True,
            text=True,
            cwd=MCP_ROOT,
        )

        assert "threshold=20" in result.stdout
        assert "preview=5" in result.stdout

    def test_get_compaction_settings_with_defaults(self):
        """Test _get_compaction_settings() returns defaults when no env vars set."""
        # Save original env vars
        orig_threshold = os.environ.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)
        orig_preview = os.environ.pop("BEADS_MCP_PREVIEW_COUNT", None)

        try:
            # Import and call the function
            from beads_mcp.server import _get_compaction_settings

            threshold, preview = _get_compaction_settings()

            assert threshold == 20
            assert preview == 5
        finally:
            # Restore env vars
            if orig_threshold:
                os.environ["BEADS_MCP_COMPACTION_THRESHOLD"] = orig_threshold
            if orig_preview:
                os.environ["BEADS_MCP_PREVIEW_COUNT"] = orig_preview

    def test_get_compaction_settings_with_custom_values(self):
        """Test _get_compaction_settings() respects custom values."""
        # Set custom values
        os.environ["BEADS_MCP_COMPACTION_THRESHOLD"] = "100"
        os.environ["BEADS_MCP_PREVIEW_COUNT"] = "15"

        try:
            from beads_mcp.server import _get_compaction_settings

            threshold, preview = _get_compaction_settings()

            assert threshold == 100
            assert preview == 15
        finally:
            # Clean up
            os.environ.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)
            os.environ.pop("BEADS_MCP_PREVIEW_COUNT", None)

    def test_get_compaction_settings_validates_threshold_minimum(self):
        """Test validation: threshold must be >= 1."""
        os.environ["BEADS_MCP_COMPACTION_THRESHOLD"] = "0"

        try:
            from beads_mcp.server import _get_compaction_settings

            with pytest.raises(ValueError, match="BEADS_MCP_COMPACTION_THRESHOLD must be >= 1"):
                _get_compaction_settings()
        finally:
            os.environ.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)

    def test_get_compaction_settings_validates_preview_minimum(self):
        """Test validation: preview_count must be >= 1."""
        os.environ["BEADS_MCP_COMPACTION_THRESHOLD"] = "20"
        os.environ["BEADS_MCP_PREVIEW_COUNT"] = "0"

        try:
            from beads_mcp.server import _get_compaction_settings

            with pytest.raises(ValueError, match="BEADS_MCP_PREVIEW_COUNT must be >= 1"):
                _get_compaction_settings()
        finally:
            os.environ.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)
            os.environ.pop("BEADS_MCP_PREVIEW_COUNT", None)

    def test_get_compaction_settings_validates_preview_not_greater_than_threshold(self):
        """Test validation: preview_count must be <= threshold."""
        os.environ["BEADS_MCP_COMPACTION_THRESHOLD"] = "10"
        os.environ["BEADS_MCP_PREVIEW_COUNT"] = "20"

        try:
            from beads_mcp.server import _get_compaction_settings

            with pytest.raises(
                ValueError, match="BEADS_MCP_PREVIEW_COUNT must be <= BEADS_MCP_COMPACTION_THRESHOLD"
            ):
                _get_compaction_settings()
        finally:
            os.environ.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)
            os.environ.pop("BEADS_MCP_PREVIEW_COUNT", None)

    def test_get_compaction_settings_with_edge_case_values(self):
        """Test edge case: preview_count == threshold."""
        os.environ["BEADS_MCP_COMPACTION_THRESHOLD"] = "5"
        os.environ["BEADS_MCP_PREVIEW_COUNT"] = "5"

        try:
            from beads_mcp.server import _get_compaction_settings

            threshold, preview = _get_compaction_settings()

            assert threshold == 5
            assert preview == 5
        finally:
            os.environ.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)
            os.environ.pop("BEADS_MCP_PREVIEW_COUNT", None)

    def test_get_compaction_settings_with_large_values(self):
        """Test large custom values."""
        os.environ["BEADS_MCP_COMPACTION_THRESHOLD"] = "1000"
        os.environ["BEADS_MCP_PREVIEW_COUNT"] = "100"

        try:
            from beads_mcp.server import _get_compaction_settings

            threshold, preview = _get_compaction_settings()

            assert threshold == 1000
            assert preview == 100
        finally:
            os.environ.pop("BEADS_MCP_COMPACTION_THRESHOLD", None)
            os.environ.pop("BEADS_MCP_PREVIEW_COUNT", None)


class TestCompactionConfigDocumentation:
    """Test that compaction configuration is documented."""

    def test_environment_variables_documented_in_code(self):
        """Test that environment variables are documented in server.py comments."""
        with open(MCP_ROOT / "src" / "beads_mcp" / "server.py") as f:
            content = f.read()

        assert "BEADS_MCP_COMPACTION_THRESHOLD" in content
        assert "BEADS_MCP_PREVIEW_COUNT" in content

    def test_environment_variables_documented_in_context_engineering_md(self):
        """Test that configuration is documented in CONTEXT_ENGINEERING.md."""
        with open(MCP_ROOT / "CONTEXT_ENGINEERING.md") as f:
            content = f.read()

        # Should mention that settings are configurable or reference bd-4u2b
        assert "configurable" in content.lower() or "bd-4u2b" in content or "environment" in content.lower()
