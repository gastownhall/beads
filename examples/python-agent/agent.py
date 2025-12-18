#!/usr/bin/env python3
"""
Simple AI agent workflow using bd (Beads issue tracker).

This demonstrates how an agent can:
1. Find ready work
2. Claim and execute tasks
3. Discover new issues during work
4. Link discoveries back to parent tasks
5. Complete work and move on
"""

import json
import subprocess
import sys
from typing import Optional


class BeadsAgent:
    """Simple agent that manages tasks using bd."""

    def __init__(self):
        self.current_task = None

    def run_bd(self, *args) -> dict:
        """Run bd command and parse JSON output."""
        cmd = ["bd"] + list(args) + ["--json"]
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)

        if result.stdout.strip():
            return json.loads(result.stdout)
        return {}

    def find_ready_work(self) -> Optional[dict]:
        """Find the highest priority ready work."""
        ready = self.run_bd("ready", "--limit", "1")

        if isinstance(ready, list) and len(ready) > 0:
            return ready[0]
        return None

    def claim_task(self, issue_id: str) -> dict:
        """Claim a task by setting status to in_progress."""
        print(f"📋 Claiming task: {issue_id}")
        result = self.run_bd("update", issue_id, "--status", "in_progress")
        return result

    def create_issue(self, title: str, description: str = "",
                     priority: int = 2, issue_type: str = "task") -> dict:
        """Create a new issue."""
        print(f"✨ Creating issue: {title}")
        args = ["create", title, "-p", str(priority), "-t", issue_type]
        if description:
            args.extend(["-d", description])
        return self.run_bd(*args)

    def link_discovery(self, discovered_id: str, parent_id: str):
        """Link a discovered issue back to its parent."""
        print(f"🔗 Linking {discovered_id} ← discovered-from ← {parent_id}")
        subprocess.run(
            ["bd", "dep", "add", discovered_id, parent_id, "--type", "discovered-from"],
            check=True
        )

    def complete_task(self, issue_id: str, reason: str = "Completed"):
        """Mark task as complete."""
        print(f"✅ Completing task: {issue_id} - {reason}")
        result = self.run_bd("close", issue_id, "--reason", reason)
        return result

    def simulate_work(self, issue: dict) -> bool:
        """Simulate doing work on an issue.

        In a real agent, this would call an LLM, execute code, etc.
        Returns True if work discovered new issues.
        """
        issue_id = issue["id"]
        title = issue["title"]

        print(f"\n🤖 Working on: {title} ({issue_id})")
        print(f"   Priority: {issue['priority']}, Type: {issue['issue_type']}")

        # Simulate discovering a bug while working
        if "implement" in title.lower() or "add" in title.lower():
            print("\n💡 Discovered: Missing test coverage for this feature")
            new_issue = self.create_issue(
                f"Add tests for {title}",
                description=f"While implementing {issue_id}, noticed missing tests",
                priority=1,
                issue_type="task"
            )
            self.link_discovery(new_issue["id"], issue_id)
            return True

        return False

    def run_once(self) -> bool:
        """Execute one work cycle. Returns True if work was found."""
        # Find ready work
        issue = self.find_ready_work()

        if not issue:
            print("📭 No ready work found.")
            return False

        # Claim the task
        self.claim_task(issue["id"])

        # Do the work (simulated)
        discovered_new_work = self.simulate_work(issue)

        # Complete the task
        self.complete_task(issue["id"], "Implemented successfully")

        if discovered_new_work:
            print("\n🔄 New work discovered and linked. Running another cycle...")

        return True

    def run(self, max_iterations: int = 10):
        """Run the agent for multiple iterations."""
        print("🚀 Beads Agent starting...\n")

        for i in range(max_iterations):
            print(f"\n{'='*60}")
            print(f"Iteration {i+1}/{max_iterations}")
            print(f"{'='*60}")

            if not self.run_once():
                break

        print("\n✨ Agent finished!")


def main():
    """Main entry point."""
    try:
        agent = BeadsAgent()
        agent.run()
    except subprocess.CalledProcessError as e:
        print(f"Error running bd: {e}", file=sys.stderr)
        print(f"Make sure bd is installed: go install github.com/steveyegge/beads/cmd/bd@latest")
        sys.exit(1)
    except KeyboardInterrupt:
        print("\n\n👋 Agent interrupted by user")
        sys.exit(0)


if __name__ == "__main__":
    main()
