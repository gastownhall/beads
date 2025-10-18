#!/usr/bin/env python3
"""
Convert GitHub Issues to bd JSONL format.

Supports two input modes:
1. GitHub API - Fetch issues directly from a repository
2. JSON Export - Parse exported GitHub issues JSON

Usage:
    # From GitHub API
    export GITHUB_TOKEN=ghp_your_token_here
    python gh2jsonl.py --repo owner/repo | bd import
    
    # From exported JSON file
    python gh2jsonl.py --file issues.json | bd import
    
    # Save to file first
    python gh2jsonl.py --repo owner/repo > issues.jsonl
"""

import json
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Dict, Any, Optional
from urllib.request import Request, urlopen
from urllib.error import HTTPError, URLError


class GitHubToBeads:
    """Convert GitHub Issues to bd JSONL format."""

    def __init__(self, prefix: str = "bd", start_id: int = 1):
        self.prefix = prefix
        self.issue_counter = start_id
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
  # From GitHub API
  export GITHUB_TOKEN=ghp_...
  python gh2jsonl.py --repo owner/repo | bd import
  
  # From JSON file
  python gh2jsonl.py --file issues.json > issues.jsonl
  
  # Fetch only open issues
  python gh2jsonl.py --repo owner/repo --state open
  
  # Custom prefix and start ID
  python gh2jsonl.py --repo owner/repo --prefix myproject --start-id 100
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

    args = parser.parse_args()

    # Validate inputs
    if not args.repo and not args.file:
        parser.error("Either --repo or --file is required")

    if args.repo and args.file:
        parser.error("Cannot use both --repo and --file")

    # Create converter
    converter = GitHubToBeads(prefix=args.prefix, start_id=args.start_id)

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
