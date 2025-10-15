"""Tests for beads_mcp.config module."""

from pathlib import Path
from unittest.mock import patch

import pytest

from beads_mcp.config import Config, ConfigError, load_config


class TestConfig:
    """Tests for Config class."""

    def test_default_beads_path_auto_detection(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that bd is auto-detected from PATH when BEADS_PATH not set."""
        # Clear BEADS_PATH if set
        monkeypatch.delenv("BEADS_PATH", raising=False)

        # Mock shutil.which to return a test path
        with patch("shutil.which", return_value="/usr/local/bin/bd"), patch("os.access", return_value=True):
            config = Config()
            assert config.beads_path == "/usr/local/bin/bd"

    def test_beads_path_from_env(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that BEADS_PATH environment variable is respected."""
        # Create a fake bd executable
        bd_path = tmp_path / "bd"
        bd_path.touch(mode=0o755)

        monkeypatch.setenv("BEADS_PATH", str(bd_path))
        config = Config()
        assert config.beads_path == str(bd_path)

    def test_beads_path_command_name_resolution(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that command names like 'bd' are resolved via PATH."""
        # Set BEADS_PATH to just "bd" (command name, not path)
        monkeypatch.setenv("BEADS_PATH", "bd")

        # Mock shutil.which to simulate finding bd in PATH
        with patch("shutil.which", return_value="/usr/local/bin/bd"), patch("os.access", return_value=True):
            config = Config()
            assert config.beads_path == "/usr/local/bin/bd"

    def test_beads_path_not_found(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that invalid BEADS_PATH raises ValueError."""
        monkeypatch.setenv("BEADS_PATH", "/nonexistent/bd")

        with pytest.raises(ValueError, match="bd executable not found"):
            Config()

    def test_beads_path_not_executable(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that non-executable bd raises ValueError."""
        # Create a non-executable file
        bd_path = tmp_path / "bd"
        bd_path.touch(mode=0o644)  # rw-r--r--

        monkeypatch.setenv("BEADS_PATH", str(bd_path))

        with pytest.raises(ValueError, match="not executable"):
            Config()

    def test_beads_db_validation(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that BEADS_DB must point to existing file."""
        # Create valid bd executable
        bd_path = tmp_path / "bd"
        bd_path.touch(mode=0o755)
        monkeypatch.setenv("BEADS_PATH", str(bd_path))

        # Set BEADS_DB to non-existent file
        monkeypatch.setenv("BEADS_DB", "/nonexistent/db.sqlite")

        with pytest.raises(ValueError, match="non-existent file"):
            Config()

    def test_beads_db_none_allowed(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that BEADS_DB can be unset (None)."""
        # Create valid bd executable
        bd_path = tmp_path / "bd"
        bd_path.touch(mode=0o755)
        monkeypatch.setenv("BEADS_PATH", str(bd_path))
        monkeypatch.delenv("BEADS_DB", raising=False)

        config = Config()
        assert config.beads_db is None

    def test_beads_actor_from_env(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that BEADS_ACTOR is read from environment."""
        bd_path = tmp_path / "bd"
        bd_path.touch(mode=0o755)
        monkeypatch.setenv("BEADS_PATH", str(bd_path))
        monkeypatch.setenv("BEADS_ACTOR", "test-user")

        config = Config()
        assert config.beads_actor == "test-user"

    def test_auto_flags_default_false(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that auto-flush and auto-import default to False."""
        bd_path = tmp_path / "bd"
        bd_path.touch(mode=0o755)
        monkeypatch.setenv("BEADS_PATH", str(bd_path))

        config = Config()
        assert config.beads_no_auto_flush is False
        assert config.beads_no_auto_import is False


class TestLoadConfig:
    """Tests for load_config function."""

    def test_load_config_success(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that load_config returns valid Config."""
        bd_path = tmp_path / "bd"
        bd_path.touch(mode=0o755)
        monkeypatch.setenv("BEADS_PATH", str(bd_path))

        config = load_config()
        assert isinstance(config, Config)
        assert config.beads_path == str(bd_path)

    def test_load_config_error_handling(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that load_config raises ConfigError with helpful message."""
        monkeypatch.setenv("BEADS_PATH", "/nonexistent/bd")

        with pytest.raises(ConfigError, match="Configuration Error"):
            load_config()
