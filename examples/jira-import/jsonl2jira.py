#!/usr/bin/env python3
"""
Export bd issues to Jira.

Creates new Jira issues from bd issues without external_ref, and optionally
updates existing Jira issues matched by external_ref.

Usage:
    # Export all issues (create new, update existing)
    bd export | python jsonl2jira.py --from-config

    # Create only (don't update existing Jira issues)
    bd export | python jsonl2jira.py --from-config --create-only

    # Dry run (preview what would happen)
    bd export | python jsonl2jira.py --from-config --dry-run

    # From JSONL file
    python jsonl2jira.py --from-config --file issues.jsonl
"""

import base64
import json
import os
import re
import subprocess
import sys
from datetime import datetime
from pathlib import Path
from typing import List, Dict, Any, Optional, Tuple
from urllib.request import Request, urlopen
from urllib.error import HTTPError, URLError


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


def get_all_bd_config() -> Dict[str, str]:
    """Get all configuration values from bd config."""
    try:
        result = subprocess.run(
            ["bd", "config", "list", "--json"],
            capture_output=True,
            text=True,
            timeout=10
        )
        if result.returncode == 0:
            return json.loads(result.stdout)
    except (subprocess.TimeoutExpired, json.JSONDecodeError, FileNotFoundError):
        pass
    return {}


def get_reverse_status_mapping() -> Dict[str, str]:
    """
    Get reverse status mapping (bd status -> Jira status).

    Uses jira.reverse_status_map.* if configured, otherwise inverts jira.status_map.*.
    Falls back to sensible defaults.
    """
    config = get_all_bd_config()

    # Check for explicit reverse mappings first
    reverse_map = {}
    for key, value in config.items():
        if key.startswith("jira.reverse_status_map."):
            bd_status = key[len("jira.reverse_status_map."):]
            reverse_map[bd_status] = value

    if reverse_map:
        return reverse_map

    # Invert the forward mapping
    for key, value in config.items():
        if key.startswith("jira.status_map."):
            jira_status = key[len("jira.status_map."):]
            # Value is bd status, key suffix is jira status
            if value not in reverse_map:
                reverse_map[value] = jira_status.replace("_", " ").title()

    # Add defaults for any missing bd statuses
    defaults = {
        "open": "To Do",
        "in_progress": "In Progress",
        "blocked": "Blocked",
        "closed": "Done",
    }

    for bd_status, jira_status in defaults.items():
        if bd_status not in reverse_map:
            reverse_map[bd_status] = jira_status

    return reverse_map


def get_reverse_type_mapping() -> Dict[str, str]:
    """
    Get reverse type mapping (bd type -> Jira issue type).

    Uses jira.reverse_type_map.* if configured, otherwise inverts jira.type_map.*.
    Falls back to sensible defaults.
    """
    config = get_all_bd_config()

    # Check for explicit reverse mappings first
    reverse_map = {}
    for key, value in config.items():
        if key.startswith("jira.reverse_type_map."):
            bd_type = key[len("jira.reverse_type_map."):]
            reverse_map[bd_type] = value

    if reverse_map:
        return reverse_map

    # Invert the forward mapping
    for key, value in config.items():
        if key.startswith("jira.type_map."):
            jira_type = key[len("jira.type_map."):]
            if value not in reverse_map:
                reverse_map[value] = jira_type.replace("_", " ").title()

    # Add defaults for any missing bd types
    defaults = {
        "bug": "Bug",
        "feature": "Story",
        "task": "Task",
        "epic": "Epic",
        "chore": "Task",
    }

    for bd_type, jira_type in defaults.items():
        if bd_type not in reverse_map:
            reverse_map[bd_type] = jira_type

    return reverse_map


def get_reverse_priority_mapping() -> Dict[int, str]:
    """
    Get reverse priority mapping (bd priority -> Jira priority name).

    Uses jira.reverse_priority_map.* if configured.
    Falls back to sensible defaults.
    """
    config = get_all_bd_config()

    # Check for explicit reverse mappings first
    reverse_map = {}
    for key, value in config.items():
        if key.startswith("jira.reverse_priority_map."):
            try:
                bd_priority = int(key[len("jira.reverse_priority_map."):])
                reverse_map[bd_priority] = value
            except ValueError:
                pass

    if reverse_map:
        return reverse_map

    # Default mapping
    return {
        0: "Highest",
        1: "High",
        2: "Medium",
        3: "Low",
        4: "Lowest",
    }


class BeadsToJira:
    """Export bd issues to Jira."""

    def __init__(
        self,
        jira_url: str,
        project: str,
        username: Optional[str] = None,
        api_token: Optional[str] = None,
        create_only: bool = False,
        dry_run: bool = False
    ):
        self.jira_url = jira_url.rstrip("/")
        self.project = project
        self.username = username
        self.api_token = api_token
        self.create_only = create_only
        self.dry_run = dry_run

        # Determine auth method
        self.is_cloud = "atlassian.net" in jira_url

        if self.is_cloud:
            if not username:
                raise ValueError(
                    "Jira Cloud requires username (email). "
                    "Set JIRA_USERNAME env var or pass --username"
                )
            auth_string = f"{username}:{api_token}"
            self.auth_header = f"Basic {base64.b64encode(auth_string.encode()).decode()}"
        else:
            if username:
                auth_string = f"{username}:{api_token}"
                self.auth_header = f"Basic {base64.b64encode(auth_string.encode()).decode()}"
            else:
                self.auth_header = f"Bearer {api_token}"

        # Load mappings
        self.status_map = get_reverse_status_mapping()
        self.type_map = get_reverse_type_mapping()
        self.priority_map = get_reverse_priority_mapping()

        # Cache for Jira metadata
        self._transitions_cache: Dict[str, List[Dict]] = {}
        self._issue_types_cache: Optional[List[Dict]] = None
        self._priorities_cache: Optional[List[Dict]] = None

        # Results tracking
        self.created: List[Tuple[str, str]] = []  # (bd_id, jira_key)
        self.updated: List[Tuple[str, str]] = []  # (bd_id, jira_key)
        self.skipped: List[Tuple[str, str]] = []  # (bd_id, reason)
        self.errors: List[Tuple[str, str]] = []   # (bd_id, error)

    def _make_request(
        self,
        method: str,
        endpoint: str,
        data: Optional[Dict] = None
    ) -> Optional[Dict]:
        """Make an authenticated request to Jira API."""
        url = f"{self.jira_url}/rest/api/2/{endpoint}"

        headers = {
            "Authorization": self.auth_header,
            "Accept": "application/json",
            "Content-Type": "application/json",
            "User-Agent": "bd-jira-export/1.0",
        }

        body = json.dumps(data).encode() if data else None

        try:
            req = Request(url, data=body, headers=headers, method=method)
            with urlopen(req, timeout=30) as response:
                response_body = response.read().decode()
                if response_body:
                    return json.loads(response_body)
                return {}
        except HTTPError as e:
            error_body = e.read().decode(errors="replace")
            raise RuntimeError(f"Jira API error {e.code}: {error_body}")
        except URLError as e:
            raise RuntimeError(f"Network error: {e.reason}")

    def get_issue_types(self) -> List[Dict]:
        """Get available issue types for the project."""
        if self._issue_types_cache is not None:
            return self._issue_types_cache

        try:
            result = self._make_request("GET", f"project/{self.project}")
            self._issue_types_cache = result.get("issueTypes", [])
        except Exception:
            # Fallback: try createmeta endpoint
            try:
                result = self._make_request(
                    "GET",
                    f"issue/createmeta?projectKeys={self.project}&expand=projects.issuetypes"
                )
                projects = result.get("projects", [])
                if projects:
                    self._issue_types_cache = projects[0].get("issuetypes", [])
                else:
                    self._issue_types_cache = []
            except Exception:
                self._issue_types_cache = []

        return self._issue_types_cache

    def get_priorities(self) -> List[Dict]:
        """Get available priorities."""
        if self._priorities_cache is not None:
            return self._priorities_cache

        try:
            self._priorities_cache = self._make_request("GET", "priority") or []
        except Exception:
            self._priorities_cache = []

        return self._priorities_cache

    def get_transitions(self, issue_key: str) -> List[Dict]:
        """Get available transitions for an issue."""
        if issue_key in self._transitions_cache:
            return self._transitions_cache[issue_key]

        try:
            result = self._make_request("GET", f"issue/{issue_key}/transitions")
            transitions = result.get("transitions", [])
            self._transitions_cache[issue_key] = transitions
            return transitions
        except Exception:
            return []

    def find_issue_type_id(self, bd_type: str) -> Optional[str]:
        """Find Jira issue type ID for a bd type."""
        jira_type_name = self.type_map.get(bd_type, "Task")
        issue_types = self.get_issue_types()

        # Try exact match first
        for it in issue_types:
            if it.get("name", "").lower() == jira_type_name.lower():
                return it.get("id")

        # Try partial match
        for it in issue_types:
            if jira_type_name.lower() in it.get("name", "").lower():
                return it.get("id")

        # Fallback to first non-subtask type
        for it in issue_types:
            if not it.get("subtask", False):
                return it.get("id")

        return None

    def find_priority_id(self, bd_priority: int) -> Optional[str]:
        """Find Jira priority ID for a bd priority."""
        jira_priority_name = self.priority_map.get(bd_priority, "Medium")
        priorities = self.get_priorities()

        # Try exact match first
        for p in priorities:
            if p.get("name", "").lower() == jira_priority_name.lower():
                return p.get("id")

        # Fallback to Medium or first available
        for p in priorities:
            if p.get("name", "").lower() == "medium":
                return p.get("id")

        if priorities:
            return priorities[0].get("id")

        return None

    def find_transition(self, issue_key: str, target_status: str) -> Optional[str]:
        """Find transition ID to move issue to target status."""
        jira_status = self.status_map.get(target_status, "To Do")
        transitions = self.get_transitions(issue_key)

        # Try exact match on target status
        for t in transitions:
            to_status = t.get("to", {}).get("name", "")
            if to_status.lower() == jira_status.lower():
                return t.get("id")

        # Try partial match
        for t in transitions:
            to_status = t.get("to", {}).get("name", "")
            if jira_status.lower() in to_status.lower():
                return t.get("id")

        return None

    def extract_jira_key_from_external_ref(self, external_ref: str) -> Optional[str]:
        """Extract Jira issue key from external_ref URL."""
        # Match patterns like:
        # https://company.atlassian.net/browse/PROJ-123
        # https://jira.company.com/browse/PROJ-123
        match = re.search(r'/browse/([A-Z]+-\d+)', external_ref)
        if match:
            return match.group(1)
        return None

    def create_issue(self, bd_issue: Dict) -> Optional[str]:
        """Create a new Jira issue. Returns the Jira key."""
        issue_type_id = self.find_issue_type_id(bd_issue.get("issue_type", "task"))
        priority_id = self.find_priority_id(bd_issue.get("priority", 2))

        if not issue_type_id:
            raise RuntimeError(f"Could not find issue type for '{bd_issue.get('issue_type')}'")

        fields = {
            "project": {"key": self.project},
            "summary": bd_issue.get("title", "Untitled"),
            "description": bd_issue.get("description", ""),
            "issuetype": {"id": issue_type_id},
        }

        if priority_id:
            fields["priority"] = {"id": priority_id}

        # Add labels if present
        labels = bd_issue.get("labels", [])
        if labels:
            fields["labels"] = labels

        # Add assignee if present (requires account ID for Cloud)
        # This is complex - skip for now as it requires user lookup
        # assignee = bd_issue.get("assignee")

        if self.dry_run:
            print(f"[DRY RUN] Would create: {bd_issue.get('title')}", file=sys.stderr)
            return "DRY-RUN-KEY"

        result = self._make_request("POST", "issue", {"fields": fields})
        return result.get("key")

    def update_issue(self, jira_key: str, bd_issue: Dict) -> bool:
        """Update an existing Jira issue. Returns True if updated."""
        # First, get current issue to compare
        try:
            current = self._make_request("GET", f"issue/{jira_key}")
        except RuntimeError:
            return False

        current_fields = current.get("fields", {})
        updates = {}

        # Check summary
        if bd_issue.get("title") and bd_issue["title"] != current_fields.get("summary"):
            updates["summary"] = bd_issue["title"]

        # Check description
        if bd_issue.get("description") != current_fields.get("description"):
            updates["description"] = bd_issue.get("description", "")

        # Check priority
        current_priority = current_fields.get("priority", {}).get("name", "").lower()
        target_priority = self.priority_map.get(bd_issue.get("priority", 2), "Medium").lower()
        if current_priority != target_priority:
            priority_id = self.find_priority_id(bd_issue.get("priority", 2))
            if priority_id:
                updates["priority"] = {"id": priority_id}

        # Check labels
        current_labels = set(current_fields.get("labels", []))
        new_labels = set(bd_issue.get("labels", []))
        if current_labels != new_labels:
            updates["labels"] = list(new_labels)

        if self.dry_run:
            if updates:
                print(f"[DRY RUN] Would update {jira_key}: {list(updates.keys())}", file=sys.stderr)
            return bool(updates)

        # Apply field updates
        if updates:
            self._make_request("PUT", f"issue/{jira_key}", {"fields": updates})

        # Handle status transition separately
        current_status = current_fields.get("status", {}).get("name", "").lower()
        target_status = bd_issue.get("status", "open")
        target_jira_status = self.status_map.get(target_status, "To Do").lower()

        if current_status != target_jira_status:
            transition_id = self.find_transition(jira_key, target_status)
            if transition_id:
                if self.dry_run:
                    print(f"[DRY RUN] Would transition {jira_key} to {target_jira_status}", file=sys.stderr)
                else:
                    try:
                        self._make_request(
                            "POST",
                            f"issue/{jira_key}/transitions",
                            {"transition": {"id": transition_id}}
                        )
                    except RuntimeError as e:
                        print(f"Warning: Could not transition {jira_key}: {e}", file=sys.stderr)

        return bool(updates) or current_status != target_jira_status

    def process_issue(self, bd_issue: Dict) -> None:
        """Process a single bd issue."""
        bd_id = bd_issue.get("id", "unknown")
        external_ref = bd_issue.get("external_ref", "")

        try:
            # Check if this issue already has a Jira reference
            jira_key = None
            if external_ref:
                jira_key = self.extract_jira_key_from_external_ref(external_ref)

            if jira_key:
                # Issue exists in Jira
                if self.create_only:
                    self.skipped.append((bd_id, f"Already in Jira as {jira_key} (--create-only)"))
                    return

                # Update existing issue
                if self.update_issue(jira_key, bd_issue):
                    self.updated.append((bd_id, jira_key))
                else:
                    self.skipped.append((bd_id, f"No changes for {jira_key}"))
            else:
                # Create new issue
                new_key = self.create_issue(bd_issue)
                if new_key:
                    self.created.append((bd_id, new_key))

                    # Output the mapping for updating external_ref
                    if not self.dry_run:
                        new_ref = f"{self.jira_url}/browse/{new_key}"
                        print(
                            json.dumps({"bd_id": bd_id, "jira_key": new_key, "external_ref": new_ref}),
                            file=sys.stdout
                        )

        except RuntimeError as e:
            self.errors.append((bd_id, str(e)))

    def process_issues(self, issues: List[Dict]) -> None:
        """Process all issues."""
        total = len(issues)
        for i, issue in enumerate(issues, 1):
            print(f"Processing {i}/{total}: {issue.get('id', 'unknown')}...", file=sys.stderr)
            self.process_issue(issue)

    def print_summary(self) -> None:
        """Print summary of operations."""
        print("\n--- Summary ---", file=sys.stderr)
        print(f"Created: {len(self.created)}", file=sys.stderr)
        for bd_id, jira_key in self.created:
            print(f"  {bd_id} -> {jira_key}", file=sys.stderr)

        print(f"Updated: {len(self.updated)}", file=sys.stderr)
        for bd_id, jira_key in self.updated:
            print(f"  {bd_id} -> {jira_key}", file=sys.stderr)

        print(f"Skipped: {len(self.skipped)}", file=sys.stderr)
        for bd_id, reason in self.skipped:
            print(f"  {bd_id}: {reason}", file=sys.stderr)

        if self.errors:
            print(f"Errors: {len(self.errors)}", file=sys.stderr)
            for bd_id, error in self.errors:
                print(f"  {bd_id}: {error}", file=sys.stderr)


def update_bd_external_refs(mappings: List[Dict]) -> None:
    """Update bd issues with external_ref from created Jira issues."""
    for mapping in mappings:
        bd_id = mapping.get("bd_id")
        external_ref = mapping.get("external_ref")

        if bd_id and external_ref:
            try:
                subprocess.run(
                    ["bd", "update", bd_id, f"--external-ref={external_ref}"],
                    capture_output=True,
                    timeout=10
                )
            except (subprocess.TimeoutExpired, FileNotFoundError):
                print(f"Warning: Could not update external_ref for {bd_id}", file=sys.stderr)


def main():
    """Main entry point."""
    import argparse

    parser = argparse.ArgumentParser(
        description="Export bd issues to Jira",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Export all issues (create new, update existing)
  bd export | python jsonl2jira.py --from-config

  # Create only (don't update existing Jira issues)
  bd export | python jsonl2jira.py --from-config --create-only

  # Dry run (preview what would happen)
  bd export | python jsonl2jira.py --from-config --dry-run

  # From JSONL file
  python jsonl2jira.py --from-config --file issues.jsonl

  # Update bd with new external_refs
  bd export | python jsonl2jira.py --from-config | while read line; do
    bd_id=$(echo "$line" | jq -r '.bd_id')
    ext_ref=$(echo "$line" | jq -r '.external_ref')
    bd update "$bd_id" --external-ref="$ext_ref"
  done

Configuration:
  Set up bd config for easier usage:
    bd config set jira.url "https://company.atlassian.net"
    bd config set jira.project "PROJ"
    bd config set jira.api_token "YOUR_TOKEN"
    bd config set jira.username "your_email@company.com"  # For Jira Cloud

  Reverse field mappings (bd -> Jira):
    bd config set jira.reverse_status_map.open "To Do"
    bd config set jira.reverse_status_map.in_progress "In Progress"
    bd config set jira.reverse_status_map.closed "Done"
    bd config set jira.reverse_type_map.feature "Story"
    bd config set jira.reverse_priority_map.0 "Highest"
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
        "--file",
        type=Path,
        help="JSONL file containing bd issues (default: read from stdin)"
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
        "--create-only",
        action="store_true",
        help="Only create new issues, don't update existing ones"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Preview what would happen without making changes"
    )
    parser.add_argument(
        "--update-refs",
        action="store_true",
        help="Automatically update bd issues with external_ref after creation"
    )

    args = parser.parse_args()

    # Resolve configuration
    jira_url = args.url
    project = args.project
    username = args.username
    api_token = args.api_token

    if args.from_config:
        if not jira_url:
            jira_url = get_bd_config("jira.url")
        if not project:
            project = get_bd_config("jira.project")
        if not username:
            username = get_bd_config("jira.username")
        if not api_token:
            api_token = get_bd_config("jira.api_token")

    # Environment variable fallbacks
    if not api_token:
        api_token = os.getenv("JIRA_API_TOKEN")
    if not username:
        username = os.getenv("JIRA_USERNAME")

    # Validate
    if not jira_url:
        parser.error("--url is required (or use --from-config with jira.url configured)")
    if not project:
        parser.error("--project is required (or use --from-config with jira.project configured)")
    if not api_token:
        parser.error("Jira API token required. Set JIRA_API_TOKEN env var or pass --api-token")

    # Load issues
    issues = []
    if args.file:
        with open(args.file, 'r', encoding='utf-8') as f:
            for line in f:
                line = line.strip()
                if line:
                    issues.append(json.loads(line))
    else:
        # Read from stdin
        for line in sys.stdin:
            line = line.strip()
            if line:
                issues.append(json.loads(line))

    if not issues:
        print("No issues to export", file=sys.stderr)
        sys.exit(0)

    print(f"Processing {len(issues)} issues...", file=sys.stderr)

    # Create exporter and process
    exporter = BeadsToJira(
        jira_url=jira_url,
        project=project,
        username=username,
        api_token=api_token,
        create_only=args.create_only,
        dry_run=args.dry_run
    )

    exporter.process_issues(issues)
    exporter.print_summary()

    # Optionally update bd external_refs
    if args.update_refs and exporter.created and not args.dry_run:
        print("\nUpdating bd issues with external_ref...", file=sys.stderr)
        mappings = [
            {"bd_id": bd_id, "external_ref": f"{jira_url}/browse/{jira_key}"}
            for bd_id, jira_key in exporter.created
        ]
        update_bd_external_refs(mappings)

    # Exit with error if there were failures
    if exporter.errors:
        sys.exit(1)


if __name__ == "__main__":
    main()
