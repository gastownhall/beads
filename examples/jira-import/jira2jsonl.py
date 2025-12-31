#!/usr/bin/env python3
"""
Convert Jira Issues to bd JSONL format.

Supports two input modes:
1. Jira REST API - Fetch issues directly from Jira Cloud or Server
2. JSON Export - Parse exported Jira issues JSON

ID Modes:
1. Sequential - Traditional numeric IDs (bd-1, bd-2, ...)
2. Hash - Content-based hash IDs (bd-a3f2dd, bd-7k9p1x, ...)

Usage:
    # From Jira API
    export JIRA_API_TOKEN=your_token_here
    python jira2jsonl.py --url https://company.atlassian.net --project PROJ | bd import

    # Using bd config (reads jira.url, jira.project, jira.api_token)
    python jira2jsonl.py --from-config | bd import

    # With JQL query
    python jira2jsonl.py --url https://company.atlassian.net --jql "project=PROJ AND status!=Done" | bd import

    # Hash-based IDs (matches bd create behavior)
    python jira2jsonl.py --from-config --id-mode hash | bd import

    # From exported JSON file
    python jira2jsonl.py --file issues.json | bd import

    # Save to file first
    python jira2jsonl.py --from-config > issues.jsonl
"""

import base64
import hashlib
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Dict, Any, Optional, Tuple
from urllib.request import Request, urlopen
from urllib.error import HTTPError, URLError
from urllib.parse import quote


def encode_base36(data: bytes, length: int) -> str:
    """
    Convert bytes to base36 string of specified length.

    Matches the Go implementation in internal/storage/sqlite/ids.go:encodeBase36
    Uses lowercase alphanumeric characters (0-9, a-z) for encoding.
    """
    # Convert bytes to integer (big-endian)
    num = int.from_bytes(data, byteorder='big')

    # Base36 alphabet (0-9, a-z)
    alphabet = '0123456789abcdefghijklmnopqrstuvwxyz'

    # Convert to base36
    if num == 0:
        result = '0'
    else:
        result = ''
        while num > 0:
            num, remainder = divmod(num, 36)
            result = alphabet[remainder] + result

    # Pad with zeros if needed
    result = result.zfill(length)

    # Truncate to exact length (keep rightmost/least significant digits)
    if len(result) > length:
        result = result[-length:]

    return result


def generate_hash_id(
    prefix: str,
    title: str,
    description: str,
    creator: str,
    timestamp: datetime,
    length: int = 6,
    nonce: int = 0
) -> str:
    """
    Generate hash-based ID matching bd's algorithm.

    Matches the Go implementation in internal/storage/sqlite/ids.go:generateHashID

    Args:
        prefix: Issue prefix (e.g., "bd", "myproject")
        title: Issue title
        description: Issue description/body
        creator: Issue creator username
        timestamp: Issue creation timestamp
        length: Hash length in characters (3-8)
        nonce: Nonce for collision handling (default: 0)

    Returns:
        Formatted ID like "bd-a3f2dd" or "myproject-7k9p1x"
    """
    # Convert timestamp to nanoseconds (matching Go's UnixNano())
    timestamp_nano = int(timestamp.timestamp() * 1_000_000_000)

    # Combine inputs with pipe delimiter (matching Go format string)
    content = f"{title}|{description}|{creator}|{timestamp_nano}|{nonce}"

    # SHA256 hash
    hash_bytes = hashlib.sha256(content.encode('utf-8')).digest()

    # Determine byte count based on length (from ids.go:258-273)
    num_bytes_map = {
        3: 2,  # 2 bytes = 16 bits ≈ 3.09 base36 chars
        4: 3,  # 3 bytes = 24 bits ≈ 4.63 base36 chars
        5: 4,  # 4 bytes = 32 bits ≈ 6.18 base36 chars
        6: 4,  # 4 bytes = 32 bits ≈ 6.18 base36 chars
        7: 5,  # 5 bytes = 40 bits ≈ 7.73 base36 chars
        8: 5,  # 5 bytes = 40 bits ≈ 7.73 base36 chars
    }
    num_bytes = num_bytes_map.get(length, 3)

    # Encode first num_bytes to base36
    short_hash = encode_base36(hash_bytes[:num_bytes], length)

    return f"{prefix}-{short_hash}"


def adf_to_text(node: Any) -> str:
    """
    Convert Atlassian Document Format (ADF) to plain text/markdown.

    ADF is returned by Jira API v3 for rich text fields like description.
    """
    if node is None:
        return ""

    if isinstance(node, str):
        return node

    if not isinstance(node, dict):
        return ""

    node_type = node.get("type", "")
    content = node.get("content", [])
    text = node.get("text", "")

    # Text node - just return the text
    if node_type == "text":
        return text

    # Recursively process content
    children_text = "".join(adf_to_text(child) for child in content)

    # Handle different node types
    if node_type == "doc":
        return children_text.strip()
    elif node_type == "paragraph":
        return children_text + "\n\n"
    elif node_type == "heading":
        level = node.get("attrs", {}).get("level", 1)
        prefix = "#" * level
        return f"{prefix} {children_text}\n\n"
    elif node_type == "bulletList":
        return children_text
    elif node_type == "orderedList":
        return children_text
    elif node_type == "listItem":
        return f"- {children_text.strip()}\n"
    elif node_type == "codeBlock":
        lang = node.get("attrs", {}).get("language", "")
        return f"```{lang}\n{children_text}```\n\n"
    elif node_type == "blockquote":
        lines = children_text.strip().split("\n")
        return "\n".join(f"> {line}" for line in lines) + "\n\n"
    elif node_type == "hardBreak":
        return "\n"
    elif node_type == "rule":
        return "---\n\n"
    elif node_type == "inlineCard":
        url = node.get("attrs", {}).get("url", "")
        return url
    elif node_type == "mention":
        return f"@{node.get('attrs', {}).get('text', '')}"
    else:
        # For unknown types, just return children text
        return children_text


def get_bd_config(key: str) -> Optional[str]:
    """Get a configuration value from bd config."""
    try:
        result = subprocess.run(
            ["bd", "config", "get", "--json", key],
            capture_output=True,
            text=True,
            timeout=10
        )
        if result.returncode == 0:
            data = json.loads(result.stdout)
            return data.get("value")
    except (subprocess.TimeoutExpired, json.JSONDecodeError, FileNotFoundError):
        pass
    return None


def get_status_mapping() -> Dict[str, str]:
    """
    Get status mapping from bd config.

    Maps Jira status names (lowercase) to bd status values.
    Falls back to sensible defaults if not configured.
    """
    # Default mappings (Jira status -> bd status)
    defaults = {
        # Common Jira statuses
        "to do": "open",
        "todo": "open",
        "open": "open",
        "backlog": "open",
        "new": "open",
        "in progress": "in_progress",
        "in development": "in_progress",
        "in review": "in_progress",
        "review": "in_progress",
        "blocked": "blocked",
        "on hold": "blocked",
        "done": "closed",
        "closed": "closed",
        "resolved": "closed",
        "complete": "closed",
        "completed": "closed",
        "won't do": "closed",
        "won't fix": "closed",
        "duplicate": "closed",
        "cannot reproduce": "closed",
    }

    # Try to read custom mappings from bd config
    # Format: jira.status_map.<jira_status> = <bd_status>
    try:
        result = subprocess.run(
            ["bd", "config", "list", "--json"],
            capture_output=True,
            text=True,
            timeout=10
        )
        if result.returncode == 0:
            config = json.loads(result.stdout)
            for key, value in config.items():
                if key.startswith("jira.status_map."):
                    jira_status = key[len("jira.status_map."):].lower()
                    defaults[jira_status] = value
    except (subprocess.TimeoutExpired, json.JSONDecodeError, FileNotFoundError):
        pass

    return defaults


def get_type_mapping() -> Dict[str, str]:
    """
    Get issue type mapping from bd config.

    Maps Jira issue type names (lowercase) to bd issue types.
    Falls back to sensible defaults if not configured.
    """
    # Default mappings (Jira type -> bd type)
    defaults = {
        "bug": "bug",
        "defect": "bug",
        "story": "feature",
        "feature": "feature",
        "new feature": "feature",
        "improvement": "feature",
        "enhancement": "feature",
        "task": "task",
        "sub-task": "task",
        "subtask": "task",
        "epic": "epic",
        "initiative": "epic",
        "technical task": "chore",
        "technical debt": "chore",
        "maintenance": "chore",
        "chore": "chore",
    }

    # Try to read custom mappings from bd config
    try:
        result = subprocess.run(
            ["bd", "config", "list", "--json"],
            capture_output=True,
            text=True,
            timeout=10
        )
        if result.returncode == 0:
            config = json.loads(result.stdout)
            for key, value in config.items():
                if key.startswith("jira.type_map."):
                    jira_type = key[len("jira.type_map."):].lower()
                    defaults[jira_type] = value
    except (subprocess.TimeoutExpired, json.JSONDecodeError, FileNotFoundError):
        pass

    return defaults


def get_priority_mapping() -> Dict[str, int]:
    """
    Get priority mapping from bd config.

    Maps Jira priority names (lowercase) to bd priority values (0-4).
    Falls back to sensible defaults if not configured.
    """
    # Default mappings (Jira priority -> bd priority)
    defaults = {
        "highest": 0,
        "critical": 0,
        "blocker": 0,
        "high": 1,
        "major": 1,
        "medium": 2,
        "normal": 2,
        "low": 3,
        "minor": 3,
        "lowest": 4,
        "trivial": 4,
    }

    # Try to read custom mappings from bd config
    try:
        result = subprocess.run(
            ["bd", "config", "list", "--json"],
            capture_output=True,
            text=True,
            timeout=10
        )
        if result.returncode == 0:
            config = json.loads(result.stdout)
            for key, value in config.items():
                if key.startswith("jira.priority_map."):
                    jira_priority = key[len("jira.priority_map."):].lower()
                    try:
                        defaults[jira_priority] = int(value)
                    except ValueError:
                        pass
    except (subprocess.TimeoutExpired, json.JSONDecodeError, FileNotFoundError):
        pass

    return defaults


class JiraToBeads:
    """Convert Jira Issues to bd JSONL format."""

    def __init__(
        self,
        prefix: str = "bd",
        start_id: int = 1,
        id_mode: str = "sequential",
        hash_length: int = 6
    ):
        self.prefix = prefix
        self.issue_counter = start_id
        self.id_mode = id_mode  # "sequential" or "hash"
        self.hash_length = hash_length  # 3-8 chars for hash mode
        self.issues: List[Dict[str, Any]] = []
        self.jira_key_to_bd_id: Dict[str, str] = {}
        self.used_ids: set = set()  # Track generated IDs for collision detection

        # Load mappings
        self.status_map = get_status_mapping()
        self.type_map = get_type_mapping()
        self.priority_map = get_priority_mapping()

    def fetch_from_api(
        self,
        url: str,
        project: Optional[str] = None,
        jql: Optional[str] = None,
        username: Optional[str] = None,
        api_token: Optional[str] = None,
        state: str = "all"
    ) -> List[Dict[str, Any]]:
        """Fetch issues from Jira REST API."""
        # Get credentials
        if not api_token:
            api_token = os.getenv("JIRA_API_TOKEN")
        if not username:
            username = os.getenv("JIRA_USERNAME")

        if not api_token:
            raise ValueError(
                "Jira API token required. Set JIRA_API_TOKEN env var or pass --api-token"
            )

        # Normalize URL
        url = url.rstrip("/")

        # Build JQL query
        if jql:
            query = jql
        elif project:
            query = f"project = {project}"
            if state == "open":
                query += " AND status != Done AND status != Closed"
            elif state == "closed":
                query += " AND (status = Done OR status = Closed)"
        else:
            raise ValueError("Either --project or --jql is required")

        # Determine API version and auth method
        # Jira Cloud uses email + API token with Basic auth
        # Jira Server/DC can use username + password or PAT
        is_cloud = "atlassian.net" in url

        if is_cloud:
            if not username:
                raise ValueError(
                    "Jira Cloud requires username (email). "
                    "Set JIRA_USERNAME env var or pass --username"
                )
            # Basic auth with email:api_token
            auth_string = f"{username}:{api_token}"
            auth_header = f"Basic {base64.b64encode(auth_string.encode()).decode()}"
        else:
            # Server/DC - try Bearer token first (PAT), fall back to Basic
            if username:
                auth_string = f"{username}:{api_token}"
                auth_header = f"Basic {base64.b64encode(auth_string.encode()).decode()}"
            else:
                auth_header = f"Bearer {api_token}"

        # Fetch all issues (paginated)
        start_at = 0
        max_results = 100
        all_issues = []

        while True:
            # Use API v3 (v2 deprecated and returns HTTP 410 Gone)
            # See: https://developer.atlassian.com/changelog/#CHANGE-2046
            api_url = f"{url}/rest/api/3/search/jql"
            params = f"jql={quote(query)}&startAt={start_at}&maxResults={max_results}&fields=*all&expand=changelog"
            full_url = f"{api_url}?{params}"

            headers = {
                "Authorization": auth_header,
                "Accept": "application/json",
                "Content-Type": "application/json",
                "User-Agent": "bd-jira-import/1.0",
            }

            try:
                req = Request(full_url, headers=headers)
                with urlopen(req, timeout=30) as response:
                    data = json.loads(response.read().decode())

                    issues = data.get("issues", [])
                    all_issues.extend(issues)

                    total = data.get("total", 0)
                    start_at += len(issues)

                    print(
                        f"Fetched {len(all_issues)}/{total} issues...",
                        file=sys.stderr
                    )

                    if start_at >= total or len(issues) == 0:
                        break

            except HTTPError as e:
                error_body = e.read().decode(errors="replace")
                msg = f"Jira API error: {e.code}"

                if e.code == 401:
                    msg += "\nAuthentication failed. Check your credentials."
                    if is_cloud:
                        msg += "\nFor Jira Cloud, use your email as username and an API token."
                        msg += "\nCreate a token at: https://id.atlassian.com/manage-profile/security/api-tokens"
                    else:
                        msg += "\nFor Jira Server/DC, use a Personal Access Token or username/password."
                elif e.code == 403:
                    msg += f"\nAccess forbidden. Check permissions for project.\n{error_body}"
                elif e.code == 400:
                    msg += f"\nBad request (invalid JQL?): {error_body}"
                else:
                    msg += f"\n{error_body}"

                raise RuntimeError(msg)
            except URLError as e:
                raise RuntimeError(f"Network error connecting to Jira: {e.reason}")

        print(f"Fetched {len(all_issues)} issues total", file=sys.stderr)
        return all_issues

    def parse_json_file(self, filepath: Path) -> List[Dict[str, Any]]:
        """Parse Jira issues from JSON file."""
        with open(filepath, 'r', encoding='utf-8') as f:
            try:
                data = json.load(f)
            except json.JSONDecodeError as e:
                raise ValueError(f"Invalid JSON in {filepath}: {e}")

        # Handle various export formats
        if isinstance(data, dict):
            # Could be a search result or single issue
            if "issues" in data:
                return data["issues"]
            elif "key" in data and "fields" in data:
                return [data]
            else:
                raise ValueError("Unrecognized Jira JSON format")
        elif isinstance(data, list):
            return data
        else:
            raise ValueError("JSON must be an object or array of issues")

    def map_priority(self, jira_priority: Optional[Dict[str, Any]]) -> int:
        """Map Jira priority to bd priority (0-4)."""
        if not jira_priority:
            return 2  # Default medium

        name = jira_priority.get("name", "").lower()
        return self.priority_map.get(name, 2)

    def map_issue_type(self, jira_type: Optional[Dict[str, Any]]) -> str:
        """Map Jira issue type to bd issue type."""
        if not jira_type:
            return "task"

        name = jira_type.get("name", "").lower()
        return self.type_map.get(name, "task")

    def map_status(self, jira_status: Optional[Dict[str, Any]]) -> str:
        """Map Jira status to bd status."""
        if not jira_status:
            return "open"

        name = jira_status.get("name", "").lower()
        return self.status_map.get(name, "open")

    def extract_labels(self, jira_labels: List[str]) -> List[str]:
        """Extract and filter labels from Jira."""
        if not jira_labels:
            return []

        # Jira labels are just strings
        return [label for label in jira_labels if label]

    def parse_jira_timestamp(self, timestamp: Optional[str]) -> Optional[datetime]:
        """Parse Jira timestamp format to datetime."""
        if not timestamp:
            return None

        # Jira uses ISO 8601 with timezone: 2024-01-15T10:30:00.000+0000
        # or sometimes: 2024-01-15T10:30:00.000Z
        try:
            # Try parsing with timezone offset
            if timestamp.endswith('Z'):
                timestamp = timestamp[:-1] + '+00:00'
            # Handle +0000 format (no colon)
            if re.match(r'.*[+-]\d{4}$', timestamp):
                timestamp = timestamp[:-2] + ':' + timestamp[-2:]
            return datetime.fromisoformat(timestamp)
        except ValueError:
            # Fallback: try without microseconds
            try:
                clean = re.sub(r'\.\d+', '', timestamp)
                if clean.endswith('Z'):
                    clean = clean[:-1] + '+00:00'
                if re.match(r'.*[+-]\d{4}$', clean):
                    clean = clean[:-2] + ':' + clean[-2:]
                return datetime.fromisoformat(clean)
            except ValueError:
                return None

    def format_timestamp(self, dt: Optional[datetime]) -> Optional[str]:
        """Format datetime to ISO 8601 string for bd."""
        if not dt:
            return None
        return dt.strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + dt.strftime("%z")[:3] + ":" + dt.strftime("%z")[3:]

    def convert_issue(self, jira_issue: Dict[str, Any], jira_url: str) -> Dict[str, Any]:
        """Convert a single Jira issue to bd format."""
        key = jira_issue["key"]
        fields = jira_issue.get("fields", {})

        # Generate ID based on mode
        if self.id_mode == "hash":
            # Extract creator
            creator = "jira-import"
            reporter = fields.get("reporter")
            if reporter and isinstance(reporter, dict):
                creator = reporter.get("displayName") or reporter.get("name") or "jira-import"

            # Parse created timestamp
            created_str = fields.get("created", "")
            created_at = self.parse_jira_timestamp(created_str)
            if not created_at:
                created_at = datetime.now(timezone.utc)

            # Generate hash ID with collision detection
            bd_id = None
            max_length = 8
            title = fields.get("summary", "")
            raw_desc = fields.get("description")
            description = adf_to_text(raw_desc) if isinstance(raw_desc, dict) else (raw_desc or "")

            for length in range(self.hash_length, max_length + 1):
                for nonce in range(10):
                    candidate = generate_hash_id(
                        prefix=self.prefix,
                        title=title,
                        description=description,
                        creator=creator,
                        timestamp=created_at,
                        length=length,
                        nonce=nonce
                    )
                    if candidate not in self.used_ids:
                        bd_id = candidate
                        break
                if bd_id:
                    break

            if not bd_id:
                raise RuntimeError(
                    f"Failed to generate unique ID for issue {key} after trying "
                    f"lengths {self.hash_length}-{max_length} with 10 nonces each"
                )
        else:
            # Sequential mode
            bd_id = f"{self.prefix}-{self.issue_counter}"
            self.issue_counter += 1

        # Track used ID
        self.used_ids.add(bd_id)

        # Store mapping for dependency resolution
        self.jira_key_to_bd_id[key] = bd_id

        # Parse timestamps
        created_at = self.parse_jira_timestamp(fields.get("created"))
        updated_at = self.parse_jira_timestamp(fields.get("updated"))
        resolved_at = self.parse_jira_timestamp(fields.get("resolutiondate"))

        # Build bd issue - convert ADF description to text
        raw_desc = fields.get("description")
        desc_text = adf_to_text(raw_desc) if isinstance(raw_desc, dict) else (raw_desc or "")
        issue = {
            "id": bd_id,
            "title": fields.get("summary", ""),
            "description": desc_text,
            "status": self.map_status(fields.get("status")),
            "priority": self.map_priority(fields.get("priority")),
            "issue_type": self.map_issue_type(fields.get("issuetype")),
        }

        # Add timestamps
        if created_at:
            issue["created_at"] = self.format_timestamp(created_at)
        if updated_at:
            issue["updated_at"] = self.format_timestamp(updated_at)

        # Add external reference (URL to Jira issue)
        jira_url_base = jira_url.rstrip("/")
        issue["external_ref"] = f"{jira_url_base}/browse/{key}"

        # Add assignee if present
        assignee = fields.get("assignee")
        if assignee and isinstance(assignee, dict):
            issue["assignee"] = assignee.get("displayName") or assignee.get("name") or ""

        # Add labels
        labels = self.extract_labels(fields.get("labels", []))
        if labels:
            issue["labels"] = labels

        # Add closed timestamp if resolved
        if issue["status"] == "closed" and resolved_at:
            issue["closed_at"] = self.format_timestamp(resolved_at)

        return issue

    def extract_issue_links(self, jira_issue: Dict[str, Any]) -> List[Tuple[str, str, str]]:
        """
        Extract issue links from a Jira issue.

        Returns list of (this_key, linked_key, link_type) tuples.
        """
        links = []
        key = jira_issue["key"]
        fields = jira_issue.get("fields", {})

        for link in fields.get("issuelinks", []):
            link_type = link.get("type", {}).get("name", "related").lower()

            # Jira links have either inwardIssue or outwardIssue
            if "inwardIssue" in link:
                linked_key = link["inwardIssue"]["key"]
                # Inward means the other issue has this relationship TO us
                # e.g., "is blocked by" means linked_key blocks us
                if "block" in link_type:
                    links.append((key, linked_key, "blocks"))
                else:
                    links.append((key, linked_key, "related"))
            elif "outwardIssue" in link:
                linked_key = link["outwardIssue"]["key"]
                # Outward means we have this relationship TO the other issue
                # e.g., "blocks" means we block linked_key
                if "block" in link_type:
                    links.append((linked_key, key, "blocks"))
                else:
                    links.append((key, linked_key, "related"))

        # Check for parent (epic link or parent field)
        parent = fields.get("parent")
        if parent:
            parent_key = parent.get("key")
            if parent_key:
                links.append((key, parent_key, "parent-child"))

        # Epic link (older Jira versions)
        epic_link = fields.get("customfield_10014")  # Common epic link field
        if not epic_link:
            epic_link = fields.get("epic", {}).get("key") if isinstance(fields.get("epic"), dict) else None
        if epic_link:
            links.append((key, epic_link, "parent-child"))

        return links

    def add_dependencies(self, jira_issues: List[Dict[str, Any]]):
        """Add dependencies based on Jira issue links."""
        for jira_issue in jira_issues:
            key = jira_issue["key"]
            bd_id = self.jira_key_to_bd_id.get(key)

            if not bd_id:
                continue

            links = self.extract_issue_links(jira_issue)
            dependencies = []

            for this_key, linked_key, link_type in links:
                # Only add if this issue is the "depending" one
                if this_key != key:
                    continue

                linked_bd_id = self.jira_key_to_bd_id.get(linked_key)
                if linked_bd_id:
                    dependencies.append({
                        "issue_id": "",  # Will be filled by bd import
                        "depends_on_id": linked_bd_id,
                        "type": link_type
                    })

            # Find the bd issue and add dependencies
            if dependencies:
                for issue in self.issues:
                    if issue["id"] == bd_id:
                        issue["dependencies"] = dependencies
                        break

    def convert(self, jira_issues: List[Dict[str, Any]], jira_url: str):
        """Convert all Jira issues to bd format."""
        # Sort by key for consistent ID assignment
        sorted_issues = sorted(jira_issues, key=lambda x: x["key"])

        # Convert each issue
        for jira_issue in sorted_issues:
            bd_issue = self.convert_issue(jira_issue, jira_url)
            self.issues.append(bd_issue)

        # Add dependencies (second pass after all IDs are assigned)
        self.add_dependencies(jira_issues)

        if self.jira_key_to_bd_id:
            first_key = min(self.jira_key_to_bd_id.keys())
            print(
                f"Converted {len(self.issues)} issues. "
                f"Mapping: {first_key} -> {self.jira_key_to_bd_id[first_key]}",
                file=sys.stderr
            )

    def to_jsonl(self) -> str:
        """Convert issues to JSONL format."""
        lines = []
        for issue in self.issues:
            lines.append(json.dumps(issue, ensure_ascii=False))
        return '\n'.join(lines)


def main():
    """Main entry point."""
    import argparse

    parser = argparse.ArgumentParser(
        description="Convert Jira Issues to bd JSONL format",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # From Jira API (sequential IDs)
  export JIRA_API_TOKEN=your_token
  export JIRA_USERNAME=your_email@company.com
  python jira2jsonl.py --url https://company.atlassian.net --project PROJ | bd import

  # Using bd config (reads jira.url, jira.project, jira.api_token)
  python jira2jsonl.py --from-config | bd import

  # Hash-based IDs (matches bd create behavior)
  python jira2jsonl.py --from-config --id-mode hash | bd import

  # With JQL query
  python jira2jsonl.py --url https://company.atlassian.net \\
    --jql "project=PROJ AND status!=Done" | bd import

  # From JSON file
  python jira2jsonl.py --file issues.json > issues.jsonl

  # Fetch only open issues
  python jira2jsonl.py --from-config --state open

  # Custom prefix with hash IDs
  python jira2jsonl.py --from-config --prefix myproject --id-mode hash

Configuration:
  Set up bd config for easier usage:
    bd config set jira.url "https://company.atlassian.net"
    bd config set jira.project "PROJ"
    bd config set jira.api_token "YOUR_TOKEN"
    bd config set jira.username "your_email@company.com"  # For Jira Cloud

  Custom field mappings:
    bd config set jira.status_map.backlog "open"
    bd config set jira.status_map.in_review "in_progress"
    bd config set jira.type_map.story "feature"
    bd config set jira.priority_map.critical "0"
        """
    )

    parser.add_argument(
        "--url",
        help="Jira instance URL (e.g., https://company.atlassian.net)"
    )
    parser.add_argument(
        "--project",
        help="Jira project key (e.g., PROJ)"
    )
    parser.add_argument(
        "--jql",
        help="JQL query to filter issues"
    )
    parser.add_argument(
        "--file",
        type=Path,
        help="JSON file containing Jira issues export"
    )
    parser.add_argument(
        "--from-config",
        action="store_true",
        help="Read Jira settings from bd config"
    )
    parser.add_argument(
        "--username",
        help="Jira username/email (or set JIRA_USERNAME env var)"
    )
    parser.add_argument(
        "--api-token",
        help="Jira API token (or set JIRA_API_TOKEN env var)"
    )
    parser.add_argument(
        "--state",
        choices=["open", "closed", "all"],
        default="all",
        help="Issue state to fetch (default: all)"
    )
    parser.add_argument(
        "--prefix",
        default="bd",
        help="Issue ID prefix (default: bd)"
    )
    parser.add_argument(
        "--start-id",
        type=int,
        default=1,
        help="Starting issue number for sequential mode (default: 1)"
    )
    parser.add_argument(
        "--id-mode",
        choices=["sequential", "hash"],
        default="sequential",
        help="ID generation mode: sequential (bd-1, bd-2) or hash (bd-a3f2dd) (default: sequential)"
    )
    parser.add_argument(
        "--hash-length",
        type=int,
        default=6,
        choices=[3, 4, 5, 6, 7, 8],
        help="Hash ID length in characters when using --id-mode hash (default: 6)"
    )

    args = parser.parse_args()

    # Resolve configuration
    jira_url = args.url
    project = args.project
    username = args.username
    api_token = args.api_token
    jql = args.jql

    if args.from_config:
        if not jira_url:
            jira_url = get_bd_config("jira.url")
        if not project:
            project = get_bd_config("jira.project")
        if not username:
            username = get_bd_config("jira.username")
        if not api_token:
            api_token = get_bd_config("jira.api_token")

    # Validate inputs
    if args.file:
        if args.url or args.project or args.jql:
            parser.error("Cannot use --file with --url, --project, or --jql")
    else:
        if not jira_url:
            parser.error("--url is required (or use --from-config with jira.url configured)")
        if not project and not jql:
            parser.error("Either --project or --jql is required")

    # Create converter
    converter = JiraToBeads(
        prefix=args.prefix,
        start_id=args.start_id,
        id_mode=args.id_mode,
        hash_length=args.hash_length
    )

    # Load issues
    if args.file:
        jira_issues = converter.parse_json_file(args.file)
        # For file mode, try to get URL from config for external_ref
        jira_url = jira_url or get_bd_config("jira.url") or "https://jira.example.com"
    else:
        jira_issues = converter.fetch_from_api(
            url=jira_url,
            project=project,
            jql=jql,
            username=username,
            api_token=api_token,
            state=args.state
        )

    if not jira_issues:
        print("No issues found", file=sys.stderr)
        sys.exit(0)

    # Convert
    converter.convert(jira_issues, jira_url)

    # Output JSONL
    print(converter.to_jsonl())


if __name__ == "__main__":
    main()
