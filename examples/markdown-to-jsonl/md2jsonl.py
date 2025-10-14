#!/usr/bin/env python3
"""
Convert markdown files to bd JSONL format.

This is a simple example converter that demonstrates the pattern.
Users can customize this for their specific markdown conventions.

Supported markdown patterns:
1. YAML frontmatter for metadata
2. H1/H2 headings as issue titles
3. Task lists as sub-issues
4. Inline issue references (e.g., "blocks: bd-10")

Usage:
    python md2jsonl.py feature.md | bd import
    python md2jsonl.py feature.md > issues.jsonl
"""

import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Dict, Any, Optional


class MarkdownToIssues:
    """Convert markdown to bd JSONL format."""

    def __init__(self, prefix: str = "bd"):
        self.prefix = prefix
        self.issue_counter = 1
        self.issues: List[Dict[str, Any]] = []

    def parse_frontmatter(self, content: str) -> tuple[Optional[Dict], str]:
        """Extract YAML frontmatter if present."""
        # Simple frontmatter detection (--- ... ---)
        if not content.startswith('---\n'):
            return None, content

        end = content.find('\n---\n', 4)
        if end == -1:
            return None, content

        frontmatter_text = content[4:end]
        body = content[end + 5:]

        # Parse simple YAML (key: value)
        metadata = {}
        for line in frontmatter_text.split('\n'):
            line = line.strip()
            if ':' in line:
                key, value = line.split(':', 1)
                metadata[key.strip()] = value.strip()

        return metadata, body

    def extract_issue_from_heading(
        self,
        heading: str,
        level: int,
        content: str,
        metadata: Optional[Dict] = None
    ) -> Dict[str, Any]:
        """Create an issue from a markdown heading and its content."""
        # Generate ID
        issue_id = f"{self.prefix}-{self.issue_counter}"
        self.issue_counter += 1

        # Extract title (remove markdown formatting)
        title = heading.strip('#').strip()

        # Parse metadata from frontmatter or defaults
        if metadata is None:
            metadata = {}

        # Build issue
        issue = {
            "id": issue_id,
            "title": title,
            "description": content.strip(),
            "status": metadata.get("status", "open"),
            "priority": int(metadata.get("priority", 2)),
            "issue_type": metadata.get("type", "task"),
            "created_at": datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
            "updated_at": datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
        }

        # Optional fields
        if "assignee" in metadata:
            issue["assignee"] = metadata["assignee"]

        if "design" in metadata:
            issue["design"] = metadata["design"]

        # Extract dependencies from description
        dependencies = self.extract_dependencies(content)
        if dependencies:
            issue["dependencies"] = dependencies

        return issue

    def extract_dependencies(self, text: str) -> List[Dict[str, str]]:
        """Extract dependency references from text."""
        dependencies = []

        # Pattern: "blocks: bd-10" or "depends-on: bd-5, bd-6"
        # Pattern: "discovered-from: bd-20"
        dep_pattern = r'(blocks|related|parent-child|discovered-from):\s*((?:bd-\d+(?:\s*,\s*)?)+)'

        for match in re.finditer(dep_pattern, text, re.IGNORECASE):
            dep_type = match.group(1).lower()
            dep_ids = [id.strip() for id in match.group(2).split(',')]

            for dep_id in dep_ids:
                dependencies.append({
                    "issue_id": "",  # Will be filled by import
                    "depends_on_id": dep_id.strip(),
                    "type": dep_type
                })

        return dependencies

    def parse_task_list(self, content: str) -> List[Dict[str, Any]]:
        """Extract task list items as separate issues."""
        issues = []

        # Pattern: - [ ] Task or - [x] Task
        task_pattern = r'^-\s+\[([ x])\]\s+(.+)$'

        for line in content.split('\n'):
            match = re.match(task_pattern, line.strip())
            if match:
                is_done = match.group(1) == 'x'
                task_text = match.group(2)

                issue_id = f"{self.prefix}-{self.issue_counter}"
                self.issue_counter += 1

                issue = {
                    "id": issue_id,
                    "title": task_text,
                    "description": "",
                    "status": "closed" if is_done else "open",
                    "priority": 2,
                    "issue_type": "task",
                    "created_at": datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
                    "updated_at": datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
                }

                issues.append(issue)

        return issues

    def parse_markdown(self, content: str, global_metadata: Optional[Dict] = None):
        """Parse markdown content into issues."""
        # Extract frontmatter
        frontmatter, body = self.parse_frontmatter(content)

        # Merge metadata
        metadata = global_metadata or {}
        if frontmatter:
            metadata.update(frontmatter)

        # Split by headings
        heading_pattern = r'^(#{1,6})\s+(.+)$'
        lines = body.split('\n')

        current_heading = None
        current_level = 0
        current_content = []

        for line in lines:
            match = re.match(heading_pattern, line)

            if match:
                # Save previous section
                if current_heading:
                    content_text = '\n'.join(current_content)

                    # Check for task lists
                    task_issues = self.parse_task_list(content_text)
                    if task_issues:
                        self.issues.extend(task_issues)
                    else:
                        # Create issue from heading
                        issue = self.extract_issue_from_heading(
                            current_heading,
                            current_level,
                            content_text,
                            metadata
                        )
                        self.issues.append(issue)

                # Start new section
                current_level = len(match.group(1))
                current_heading = match.group(2)
                current_content = []
            else:
                current_content.append(line)

        # Save final section
        if current_heading:
            content_text = '\n'.join(current_content)
            task_issues = self.parse_task_list(content_text)
            if task_issues:
                self.issues.extend(task_issues)
            else:
                issue = self.extract_issue_from_heading(
                    current_heading,
                    current_level,
                    content_text,
                    metadata
                )
                self.issues.append(issue)

    def to_jsonl(self) -> str:
        """Convert issues to JSONL format."""
        lines = []
        for issue in self.issues:
            lines.append(json.dumps(issue, ensure_ascii=False))
        return '\n'.join(lines)


def main():
    """Main entry point."""
    if len(sys.argv) < 2:
        print("Usage: python md2jsonl.py <markdown-file>", file=sys.stderr)
        print("", file=sys.stderr)
        print("Examples:", file=sys.stderr)
        print("  python md2jsonl.py feature.md | bd import", file=sys.stderr)
        print("  python md2jsonl.py feature.md > issues.jsonl", file=sys.stderr)
        sys.exit(1)

    markdown_file = Path(sys.argv[1])

    if not markdown_file.exists():
        print(f"Error: File not found: {markdown_file}", file=sys.stderr)
        sys.exit(1)

    # Read markdown
    content = markdown_file.read_text()

    # Convert to issues
    converter = MarkdownToIssues(prefix="bd")
    converter.parse_markdown(content)

    # Output JSONL
    print(converter.to_jsonl())


if __name__ == "__main__":
    main()
