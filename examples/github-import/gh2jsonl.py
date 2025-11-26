#!/usr/bin/env python3
"""
Convert GitHub Issues to bd JSONL format.

Supports two input modes:
1. GitHub API - Fetch issues directly from a repository
2. JSON Export - Parse exported GitHub issues JSON

ID Modes:
1. Sequential - Traditional numeric IDs (bd-1, bd-2, ...)
2. Hash - Content-based hash IDs (bd-a3f2dd, bd-7k9p1x, ...)

Usage:
    # From GitHub API (sequential IDs)
    export GITHUB_TOKEN=ghp_your_token_here
    python gh2jsonl.py --repo owner/repo | bd import

    # Hash-based IDs (matches bd create behavior)
    python gh2jsonl.py --repo owner/repo --id-mode hash | bd import

    # From exported JSON file
    python gh2jsonl.py --file issues.json | bd import

    # Hash IDs with custom length (4-8 chars)
    python gh2jsonl.py --repo owner/repo --id-mode hash --hash-length 4 | bd import

    # Save to file first
    python gh2jsonl.py --repo owner/repo > issues.jsonl
"""

import hashlib
import json
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Dict, Any, Optional
from urllib.request import Request, urlopen
from urllib.error import HTTPError, URLError


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


class GitHubToBeads:
    """Convert GitHub Issues to bd JSONL format."""

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
        self.gh_id_to_bd_id: Dict[int, str] = {}

    def fetch_from_api(self, repo: str, token: Optional[str] = None, state: str = "all"):
        """Fetch issues from GitHub API."""
        if not token:
            token = os.getenv("GITHUB_TOKEN")
            if not token:
                raise ValueError(
                    "GitHub token required. Set GITHUB_TOKEN env var or pass --token"
                )

        # Parse repo
        if "/" not in repo:
            raise ValueError("Repository must be in format: owner/repo")

        # Fetch all issues (paginated)
        page = 1
        per_page = 100
        all_issues = []

        while True:
            url = f"https://api.github.com/repos/{repo}/issues?state={state}&per_page={per_page}&page={page}"
            headers = {
                "Authorization": f"token {token}",
                "Accept": "application/vnd.github.v3+json",
                "User-Agent": "bd-gh-import/1.0",
            }

            try:
                req = Request(url, headers=headers)
                with urlopen(req) as response:
                    data = json.loads(response.read().decode())

                    if not data:
                        break

                    # Filter out pull requests (they appear in issues endpoint)
                    issues = [issue for issue in data if "pull_request" not in issue]
                    all_issues.extend(issues)

                    if len(data) < per_page:
                        break

                    page += 1

            except HTTPError as e:
                error_body = e.read().decode(errors="replace")
                remaining = e.headers.get("X-RateLimit-Remaining")
                reset = e.headers.get("X-RateLimit-Reset")
                msg = f"GitHub API error: {e.code} - {error_body}"
                if e.code == 403 and remaining == "0":
                    msg += f"\nRate limit exceeded. Resets at Unix timestamp: {reset}"
                raise RuntimeError(msg)
            except URLError as e:
                raise RuntimeError(f"Network error calling GitHub: {e.reason}")

        print(f"Fetched {len(all_issues)} issues from {repo}", file=sys.stderr)
        return all_issues

    def parse_json_file(self, filepath: Path) -> List[Dict[str, Any]]:
        """Parse GitHub issues from JSON file."""
        with open(filepath, 'r', encoding='utf-8') as f:
            try:
                data = json.load(f)
            except json.JSONDecodeError as e:
                raise ValueError(f"Invalid JSON in {filepath}: {e}")

        # Handle both single issue and array of issues
        if isinstance(data, dict):
            # Filter out PRs
            if "pull_request" in data:
                return []
            return [data]
        elif isinstance(data, list):
            # Filter out PRs
            return [issue for issue in data if "pull_request" not in issue]
        else:
            raise ValueError("JSON must be a single issue object or array of issues")

    def map_priority(self, labels: List[str]) -> int:
        """Map GitHub labels to bd priority."""
        label_names = [label.get("name", "").lower() if isinstance(label, dict) else label.lower() for label in labels]

        # Priority labels (customize for your repo)
        if any(l in label_names for l in ["critical", "p0", "urgent"]):
            return 0
        elif any(l in label_names for l in ["high", "p1", "important"]):
            return 1
        elif any(l in label_names for l in ["low", "p3", "minor"]):
            return 3
        elif any(l in label_names for l in ["backlog", "p4", "someday"]):
            return 4
        else:
            return 2  # Default medium

    def map_issue_type(self, labels: List[str]) -> str:
        """Map GitHub labels to bd issue type."""
        label_names = [label.get("name", "").lower() if isinstance(label, dict) else label.lower() for label in labels]

        # Type labels (customize for your repo)
        if any(l in label_names for l in ["bug", "defect"]):
            return "bug"
        elif any(l in label_names for l in ["feature", "enhancement"]):
            return "feature"
        elif any(l in label_names for l in ["epic", "milestone"]):
            return "epic"
        elif any(l in label_names for l in ["chore", "maintenance", "dependencies"]):
            return "chore"
        else:
            return "task"

    def map_status(self, state: str, labels: List[str]) -> str:
        """Map GitHub state to bd status."""
        label_names = [label.get("name", "").lower() if isinstance(label, dict) else label.lower() for label in labels]

        if state == "closed":
            return "closed"
        elif any(l in label_names for l in ["in progress", "in-progress", "wip"]):
            return "in_progress"
        elif any(l in label_names for l in ["blocked"]):
            return "blocked"
        else:
            return "open"

    def extract_labels(self, gh_labels: List) -> List[str]:
        """Extract label names from GitHub label objects."""
        labels = []
        for label in gh_labels:
            if isinstance(label, dict):
                name = label.get("name", "")
            else:
                name = str(label)

            # Filter out labels we use for mapping
            skip_labels = {
                "bug", "feature", "epic", "chore", "enhancement", "defect",
                "critical", "high", "low", "p0", "p1", "p2", "p3", "p4",
                "urgent", "important", "minor", "backlog", "someday",
                "in progress", "in-progress", "wip", "blocked"
            }

            if name.lower() not in skip_labels:
                labels.append(name)

        return labels

    def extract_dependencies_from_body(self, body: str) -> List[str]:
        """Extract issue references from body text."""
        if not body:
            return []

        refs = []

        # Pattern: #123 or owner/repo#123
        issue_pattern = r'(?:^|\s)#(\d+)|(?:[\w-]+/[\w-]+)#(\d+)'

        for match in re.finditer(issue_pattern, body):
            issue_num = match.group(1) or match.group(2)
            if issue_num:
                refs.append(int(issue_num))

        return list(set(refs))  # Deduplicate

    def convert_issue(self, gh_issue: Dict[str, Any]) -> Dict[str, Any]:
        """Convert a single GitHub issue to bd format."""
        gh_id = gh_issue["number"]

        # Generate ID based on mode
        if self.id_mode == "hash":
            # Extract creator (use "github-import" as fallback)
            creator = "github-import"
            if gh_issue.get("user"):
                if isinstance(gh_issue["user"], dict):
                    creator = gh_issue["user"].get("login", "github-import")

            # Parse created_at timestamp
            created_at_str = gh_issue["created_at"]
            # Handle both ISO format with Z and +00:00
            if created_at_str.endswith('Z'):
                created_at_str = created_at_str[:-1] + '+00:00'
            created_at = datetime.fromisoformat(created_at_str)

            # Generate hash ID
            bd_id = generate_hash_id(
                prefix=self.prefix,
                title=gh_issue["title"],
                description=gh_issue.get("body") or "",
                creator=creator,
                timestamp=created_at,
                length=self.hash_length,
                nonce=0
            )
        else:
            # Sequential mode (existing behavior)
            bd_id = f"{self.prefix}-{self.issue_counter}"
            self.issue_counter += 1

        # Store mapping
        self.gh_id_to_bd_id[gh_id] = bd_id

        labels = gh_issue.get("labels", [])

        # Build bd issue
        issue = {
            "id": bd_id,
            "title": gh_issue["title"],
            "description": gh_issue.get("body") or "",
            "status": self.map_status(gh_issue["state"], labels),
            "priority": self.map_priority(labels),
            "issue_type": self.map_issue_type(labels),
            "created_at": gh_issue["created_at"],
            "updated_at": gh_issue["updated_at"],
        }

        # Add external reference
        issue["external_ref"] = gh_issue["html_url"]

        # Add assignee if present
        if gh_issue.get("assignee"):
            issue["assignee"] = gh_issue["assignee"]["login"]

        # Add labels (filtered)
        bd_labels = self.extract_labels(labels)
        if bd_labels:
            issue["labels"] = bd_labels

        # Add closed timestamp if closed
        if gh_issue.get("closed_at"):
            issue["closed_at"] = gh_issue["closed_at"]

        return issue

    def add_dependencies(self):
        """Add dependencies based on issue references in body text."""
        for gh_issue_data in getattr(self, '_gh_issues', []):
            gh_id = gh_issue_data["number"]
            bd_id = self.gh_id_to_bd_id.get(gh_id)

            if not bd_id:
                continue

            body = gh_issue_data.get("body") or ""
            referenced_gh_ids = self.extract_dependencies_from_body(body)

            dependencies = []
            for ref_gh_id in referenced_gh_ids:
                ref_bd_id = self.gh_id_to_bd_id.get(ref_gh_id)
                if ref_bd_id:
                    dependencies.append({
                        "issue_id": "",
                        "depends_on_id": ref_bd_id,
                        "type": "related"
                    })

            # Find the bd issue and add dependencies
            if dependencies:
                for issue in self.issues:
                    if issue["id"] == bd_id:
                        issue["dependencies"] = dependencies
                        break

    def convert(self, gh_issues: List[Dict[str, Any]]):
        """Convert all GitHub issues to bd format."""
        # Store for dependency processing
        self._gh_issues = gh_issues

        # Sort by issue number for consistent ID assignment
        sorted_issues = sorted(gh_issues, key=lambda x: x["number"])

        # Convert each issue
        for gh_issue in sorted_issues:
            bd_issue = self.convert_issue(gh_issue)
            self.issues.append(bd_issue)

        # Add cross-references
        self.add_dependencies()

        print(
            f"Converted {len(self.issues)} issues. Mapping: GH #{min(self.gh_id_to_bd_id.keys())} -> {self.gh_id_to_bd_id[min(self.gh_id_to_bd_id.keys())]}",
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
        description="Convert GitHub Issues to bd JSONL format",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # From GitHub API (sequential IDs)
  export GITHUB_TOKEN=ghp_...
  python gh2jsonl.py --repo owner/repo | bd import

  # Hash-based IDs (matches bd create behavior)
  python gh2jsonl.py --repo owner/repo --id-mode hash | bd import

  # From JSON file
  python gh2jsonl.py --file issues.json > issues.jsonl

  # Hash IDs with custom length
  python gh2jsonl.py --repo owner/repo --id-mode hash --hash-length 4 | bd import

  # Fetch only open issues
  python gh2jsonl.py --repo owner/repo --state open

  # Custom prefix with hash IDs
  python gh2jsonl.py --repo owner/repo --prefix myproject --id-mode hash
        """
    )

    parser.add_argument(
        "--repo",
        help="GitHub repository (owner/repo)"
    )
    parser.add_argument(
        "--file",
        type=Path,
        help="JSON file containing GitHub issues export"
    )
    parser.add_argument(
        "--token",
        help="GitHub personal access token (or set GITHUB_TOKEN env var)"
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
        help="Starting issue number (default: 1)"
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

    # Validate inputs
    if not args.repo and not args.file:
        parser.error("Either --repo or --file is required")

    if args.repo and args.file:
        parser.error("Cannot use both --repo and --file")

    # Create converter
    converter = GitHubToBeads(
        prefix=args.prefix,
        start_id=args.start_id,
        id_mode=args.id_mode,
        hash_length=args.hash_length
    )

    # Load issues
    if args.repo:
        gh_issues = converter.fetch_from_api(args.repo, args.token, args.state)
    else:
        gh_issues = converter.parse_json_file(args.file)

    if not gh_issues:
        print("No issues found", file=sys.stderr)
        sys.exit(0)

    # Convert
    converter.convert(gh_issues)

    # Output JSONL
    print(converter.to_jsonl())


if __name__ == "__main__":
    main()
