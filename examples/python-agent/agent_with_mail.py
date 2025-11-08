#!/usr/bin/env python3
"""
Beads Agent with Agent Mail Integration Example

Demonstrates how to use bd with optional Agent Mail coordination for multi-agent workflows.
Shows collision handling, graceful degradation, and best practices.
"""

import json
import os
import subprocess
import sys
import time
from typing import Optional, Dict, Any, List


class BeadsAgent:
    """A simple agent that uses bd with optional Agent Mail coordination."""
    
    def __init__(self, agent_name: str, project_id: str, agent_mail_url: Optional[str] = None):
        """
        Initialize the agent.
        
        Args:
            agent_name: Unique identifier for this agent (e.g., "assistant-alpha")
            project_id: Project namespace for Agent Mail
            agent_mail_url: Agent Mail server URL (optional, e.g., "http://127.0.0.1:8765")
        """
        self.agent_name = agent_name
        self.project_id = project_id
        self.agent_mail_url = agent_mail_url
        
        # Configure environment for Agent Mail if URL provided
        if self.agent_mail_url:
            os.environ["BEADS_AGENT_MAIL_URL"] = self.agent_mail_url
            os.environ["BEADS_AGENT_NAME"] = self.agent_name
            os.environ["BEADS_PROJECT_ID"] = self.project_id
            print(f"✨ Agent Mail enabled: {agent_name} @ {agent_mail_url}")
        else:
            print(f"📝 Git-only mode: {agent_name}")
    
    def run_bd(self, *args) -> Dict[str, Any]:
        """
        Run a bd command and return parsed JSON output.
        
        Args:
            *args: Command arguments (e.g., "ready", "--json")
        
        Returns:
            Parsed JSON output from bd
        """
        cmd = ["bd"] + list(args)
        if "--json" not in args:
            cmd.append("--json")
        
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                check=False  # Don't raise on non-zero exit
            )
            
            # Handle reservation conflicts gracefully
            if result.returncode != 0:
                # Check if it's a reservation conflict
                if "already reserved" in result.stderr or "reservation conflict" in result.stderr:
                    print(f"⚠️  Reservation conflict: {result.stderr.strip()}")
                    return {"error": "reservation_conflict", "stderr": result.stderr}
                else:
                    print(f"❌ Command failed: {' '.join(cmd)}")
                    print(f"   Error: {result.stderr}")
                    return {"error": "command_failed", "stderr": result.stderr}
            
            # Parse JSON output
            if result.stdout.strip():
                return json.loads(result.stdout)
            else:
                return {}
                
        except json.JSONDecodeError as e:
            print(f"❌ Failed to parse JSON from bd: {e}")
            print(f"   Output: {result.stdout}")
            return {"error": "json_parse_failed"}
        except Exception as e:
            print(f"❌ Failed to run bd: {e}")
            return {"error": str(e)}
    
    def get_ready_work(self) -> List[Dict[str, Any]]:
        """Get list of unblocked issues ready to work on."""
        result = self.run_bd("ready", "--json")
        
        if "error" in result:
            return []
        
        # bd ready returns array of issues
        if isinstance(result, list):
            return result
        else:
            return []
    
    def claim_issue(self, issue_id: str) -> bool:
        """
        Claim an issue by setting status to in_progress.
        
        Returns:
            True if successful, False if reservation conflict or error
        """
        print(f"📋 Claiming issue: {issue_id}")
        result = self.run_bd("update", issue_id, "--status", "in_progress")
        
        if "error" in result:
            if result["error"] == "reservation_conflict":
                print(f"   ⚠️  Issue {issue_id} already claimed by another agent")
                return False
            else:
                print(f"   ❌ Failed to claim {issue_id}")
                return False
        
        print(f"   ✅ Successfully claimed {issue_id}")
        return True
    
    def complete_issue(self, issue_id: str, reason: str = "Completed") -> bool:
        """
        Complete an issue and release reservation.
        
        Returns:
            True if successful, False otherwise
        """
        print(f"✅ Completing issue: {issue_id}")
        result = self.run_bd("close", issue_id, "--reason", reason)
        
        if "error" in result:
            print(f"   ❌ Failed to complete {issue_id}")
            return False
        
        print(f"   ✅ Issue {issue_id} completed")
        return True
    
    def create_discovered_issue(
        self, 
        title: str, 
        parent_id: str, 
        priority: int = 2,
        issue_type: str = "task"
    ) -> Optional[str]:
        """
        Create an issue discovered during work on another issue.
        
        Args:
            title: Issue title
            parent_id: ID of the issue this was discovered from
            priority: Priority level (0-4)
            issue_type: Issue type (bug, feature, task, etc.)
        
        Returns:
            New issue ID if successful, None otherwise
        """
        print(f"💡 Creating discovered issue: {title}")
        result = self.run_bd(
            "create",
            title,
            "-t", issue_type,
            "-p", str(priority),
            "--deps", f"discovered-from:{parent_id}"
        )
        
        if "error" in result or "id" not in result:
            print(f"   ❌ Failed to create issue")
            return None
        
        new_id = result["id"]
        print(f"   ✅ Created {new_id}")
        return new_id
    
    def simulate_work(self, issue: Dict[str, Any]) -> None:
        """Simulate working on an issue."""
        print(f"🤖 Working on: {issue['title']} ({issue['id']})")
        print(f"   Priority: {issue['priority']}, Type: {issue['issue_type']}")
        time.sleep(1)  # Simulate work
    
    def run(self, max_iterations: int = 10) -> None:
        """
        Main agent loop: find work, claim it, complete it.
        
        Args:
            max_iterations: Maximum number of issues to process
        """
        print(f"\n🚀 Agent '{self.agent_name}' starting...")
        print(f"   Project: {self.project_id}")
        print(f"   Agent Mail: {'Enabled' if self.agent_mail_url else 'Disabled (git-only mode)'}\n")
        
        for iteration in range(1, max_iterations + 1):
            print("=" * 60)
            print(f"Iteration {iteration}/{max_iterations}")
            print("=" * 60)
            
            # Get ready work
            ready_issues = self.get_ready_work()
            
            if not ready_issues:
                print("📭 No ready work available. Stopping.")
                break
            
            # Sort by priority (lower number = higher priority)
            ready_issues.sort(key=lambda x: x.get("priority", 99))
            
            # Try to claim the highest priority issue
            claimed = False
            for issue in ready_issues:
                if self.claim_issue(issue["id"]):
                    claimed = True
                    
                    # Simulate work
                    self.simulate_work(issue)
                    
                    # Randomly discover new work (33% chance)
                    import random
                    if random.random() < 0.33:
                        discovered_title = f"Follow-up work for {issue['title']}"
                        new_id = self.create_discovered_issue(
                            discovered_title,
                            issue["id"],
                            priority=issue.get("priority", 2)
                        )
                        if new_id:
                            print(f"🔗 Linked {new_id} ← discovered-from ← {issue['id']}")
                    
                    # Complete the issue
                    self.complete_issue(issue["id"], "Implemented successfully")
                    break
            
            if not claimed:
                print("⚠️  All ready issues are reserved by other agents. Waiting...")
                time.sleep(2)  # Wait before retrying
            
            print()
        
        print(f"🏁 Agent '{self.agent_name}' finished after {iteration} iterations.")


def main():
    """Main entry point."""
    # Parse command line arguments
    import argparse
    parser = argparse.ArgumentParser(
        description="Beads agent with optional Agent Mail coordination"
    )
    parser.add_argument(
        "--agent-name",
        default=os.getenv("BEADS_AGENT_NAME", f"agent-{os.getpid()}"),
        help="Unique agent identifier (default: agent-<pid>)"
    )
    parser.add_argument(
        "--project-id",
        default=os.getenv("BEADS_PROJECT_ID", "default"),
        help="Project namespace for Agent Mail"
    )
    parser.add_argument(
        "--agent-mail-url",
        default=os.getenv("BEADS_AGENT_MAIL_URL"),
        help="Agent Mail server URL (optional, e.g., http://127.0.0.1:8765)"
    )
    parser.add_argument(
        "--max-iterations",
        type=int,
        default=10,
        help="Maximum number of issues to process (default: 10)"
    )
    
    args = parser.parse_args()
    
    # Create and run agent
    agent = BeadsAgent(
        agent_name=args.agent_name,
        project_id=args.project_id,
        agent_mail_url=args.agent_mail_url
    )
    
    try:
        agent.run(max_iterations=args.max_iterations)
    except KeyboardInterrupt:
        print("\n\n⚠️  Agent interrupted by user. Exiting...")
        sys.exit(0)


if __name__ == "__main__":
    main()
