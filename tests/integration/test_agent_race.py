#!/usr/bin/env python3
"""
Multi-agent race condition test for bd (beads) issue tracker.

Tests verify that when 2+ agents simultaneously try to claim the same issue:
1. WITH Agent Mail: Only one agent succeeds (via reservation), others skip gracefully
2. WITHOUT Agent Mail: Both agents may succeed (demonstrating the collision problem)

This test validates the collision prevention mechanism provided by Agent Mail.
"""

import json
import subprocess
import tempfile
import shutil
import os
import sys
import time
from pathlib import Path
from multiprocessing import Process, Queue
from typing import List, Tuple

# Add lib directory for beads_mail_adapter
lib_path = Path(__file__).parent.parent.parent / "lib"
sys.path.insert(0, str(lib_path))

from beads_mail_adapter import AgentMailAdapter


class RaceTestAgent:
    """Minimal agent implementation for race condition testing."""
    
    def __init__(self, agent_name: str, workspace: str, mail_enabled: bool = True):
        self.agent_name = agent_name
        self.workspace = workspace
        self.mail_enabled = mail_enabled
        
        # Initialize Agent Mail adapter
        if mail_enabled:
            self.mail = AgentMailAdapter(agent_name=agent_name)
        else:
            self.mail = None
    
    def run_bd(self, *args) -> dict:
        """Run bd command in the test workspace."""
        cmd = ["bd"] + list(args) + ["--json"]
        result = subprocess.run(
            cmd,
            cwd=self.workspace,
            capture_output=True,
            text=True
        )
        
        if result.returncode != 0:
            return {"error": result.stderr}
        
        if result.stdout.strip():
            try:
                return json.loads(result.stdout)
            except json.JSONDecodeError:
                return {"error": "Invalid JSON", "output": result.stdout}
        return {}
    
    def try_claim_issue(self, issue_id: str) -> Tuple[bool, str]:
        """
        Attempt to claim an issue.
        
        Returns:
            (success: bool, message: str)
        """
        # Integration Point 2: Reserve before claiming (if Agent Mail enabled)
        if self.mail and self.mail.enabled:
            reserved = self.mail.reserve_issue(issue_id)
            if not reserved:
                return False, f"Reservation failed for {issue_id}"
        
        # Claim the issue
        result = self.run_bd("update", issue_id, "--status", "in_progress")
        
        if "error" in result:
            if self.mail and self.mail.enabled:
                self.mail.release_issue(issue_id)
            return False, f"Update failed: {result['error']}"
        
        return True, f"Successfully claimed {issue_id}"
    
    def release_issue(self, issue_id: str):
        """Release an issue after claiming."""
        if self.mail and self.mail.enabled:
            self.mail.release_issue(issue_id)


def agent_worker(agent_name: str, workspace: str, target_issue_id: str, 
                 mail_enabled: bool, result_queue: Queue):
    """
    Worker function for multiprocessing.
    
    Each worker tries to claim the same issue. Result is put in queue.
    """
    try:
        agent = RaceTestAgent(agent_name, workspace, mail_enabled)
        
        # Small random delay to increase likelihood of collision
        time.sleep(0.01 * hash(agent_name) % 10)
        
        success, message = agent.try_claim_issue(target_issue_id)
        
        result_queue.put({
            "agent": agent_name,
            "success": success,
            "message": message,
            "mail_enabled": mail_enabled
        })
    except Exception as e:
        result_queue.put({
            "agent": agent_name,
            "success": False,
            "message": f"Exception: {str(e)}",
            "mail_enabled": mail_enabled
        })


def run_race_test(num_agents: int, mail_enabled: bool) -> List[dict]:
    """
    Run a race test with N agents trying to claim the same issue.
    
    Args:
        num_agents: Number of agents to spawn
        mail_enabled: Whether Agent Mail is enabled
    
    Returns:
        List of result dicts from each agent
    """
    # Create temporary workspace
    workspace = tempfile.mkdtemp(prefix="bd-race-test-")
    
    try:
        # Initialize bd in workspace
        subprocess.run(
            ["bd", "init", "--quiet", "--prefix", "test"],
            cwd=workspace,
            check=True,
            capture_output=True
        )
        
        # Create a test issue
        result = subprocess.run(
            ["bd", "create", "Contested issue", "-p", "1", "--json"],
            cwd=workspace,
            capture_output=True,
            text=True,
            check=True
        )
        issue_data = json.loads(result.stdout)
        issue_id = issue_data["id"]
        
        # Spawn agents in parallel
        result_queue = Queue()
        processes = []
        
        for i in range(num_agents):
            agent_name = f"agent-{i+1}"
            p = Process(
                target=agent_worker,
                args=(agent_name, workspace, issue_id, mail_enabled, result_queue)
            )
            processes.append(p)
        
        # Start all processes simultaneously
        start_time = time.time()
        for p in processes:
            p.start()
        
        # Wait for completion
        for p in processes:
            p.join(timeout=10)
        
        elapsed = time.time() - start_time
        
        # Collect results
        results = []
        while not result_queue.empty():
            results.append(result_queue.get())
        
        # Verify JSONL for duplicate claims
        jsonl_path = Path(workspace) / ".beads" / "issues.jsonl"
        jsonl_claims = verify_jsonl_claims(jsonl_path, issue_id)
        
        return {
            "issue_id": issue_id,
            "agents": results,
            "elapsed_seconds": elapsed,
            "jsonl_status_changes": jsonl_claims,
            "mail_enabled": mail_enabled
        }
    
    finally:
        # Cleanup
        shutil.rmtree(workspace, ignore_errors=True)


def verify_jsonl_claims(jsonl_path: Path, issue_id: str) -> List[dict]:
    """
    Parse JSONL and count how many times the issue status was changed to in_progress.
    
    Returns list of status change events.
    """
    if not jsonl_path.exists():
        return []
    
    status_changes = []
    
    with open(jsonl_path) as f:
        for line in f:
            if not line.strip():
                continue
            
            try:
                record = json.loads(line)
                if record.get("id") == issue_id and record.get("status") == "in_progress":
                    status_changes.append({
                        "updated_at": record.get("updated_at"),
                        "assignee": record.get("assignee")
                    })
            except json.JSONDecodeError:
                continue
    
    return status_changes


def test_agent_race_with_mail():
    """Test that WITH Agent Mail, only one agent succeeds."""
    print("\n" + "="*70)
    print("TEST 1: Race condition WITH Agent Mail (collision prevention)")
    print("="*70)
    
    num_agents = 3
    result = run_race_test(num_agents, mail_enabled=True)
    
    # Analyze results
    successful_agents = [a for a in result["agents"] if a["success"]]
    failed_agents = [a for a in result["agents"] if not a["success"]]
    
    print(f"\n📊 Results ({result['elapsed_seconds']:.3f}s):")
    print(f"   • Total agents: {num_agents}")
    print(f"   • Successful claims: {len(successful_agents)}")
    print(f"   • Failed claims: {len(failed_agents)}")
    print(f"   • JSONL status changes: {len(result['jsonl_status_changes'])}")
    
    for agent in result["agents"]:
        status = "✅" if agent["success"] else "❌"
        print(f"   {status} {agent['agent']}: {agent['message']}")
    
    # Verify: Only one agent should succeed
    assert len(successful_agents) == 1, \
        f"Expected 1 successful claim, got {len(successful_agents)}"
    
    # Verify: JSONL should have exactly 1 in_progress status
    assert len(result['jsonl_status_changes']) == 1, \
        f"Expected 1 JSONL status change, got {len(result['jsonl_status_changes'])}"
    
    print("\n✅ PASS: Agent Mail prevented duplicate claims")
    return True


def test_agent_race_without_mail():
    """Test that WITHOUT Agent Mail, multiple agents may succeed (collision)."""
    print("\n" + "="*70)
    print("TEST 2: Race condition WITHOUT Agent Mail (collision demonstration)")
    print("="*70)
    print("⚠️  Note: This test may occasionally pass if timing prevents collision")
    
    num_agents = 3
    result = run_race_test(num_agents, mail_enabled=False)
    
    # Analyze results
    successful_agents = [a for a in result["agents"] if a["success"]]
    failed_agents = [a for a in result["agents"] if not a["success"]]
    
    print(f"\n📊 Results ({result['elapsed_seconds']:.3f}s):")
    print(f"   • Total agents: {num_agents}")
    print(f"   • Successful claims: {len(successful_agents)}")
    print(f"   • Failed claims: {len(failed_agents)}")
    print(f"   • JSONL status changes: {len(result['jsonl_status_changes'])}")
    
    for agent in result["agents"]:
        status = "✅" if agent["success"] else "❌"
        print(f"   {status} {agent['agent']}: {agent['message']}")
    
    # Without Agent Mail, we expect potential for duplicates
    # (though timing may occasionally prevent it)
    if len(successful_agents) > 1:
        print(f"\n⚠️  EXPECTED: Multiple agents ({len(successful_agents)}) claimed same issue")
        print("   This demonstrates the collision problem Agent Mail prevents")
    else:
        print("\n⚠️  NOTE: Only one agent succeeded (timing prevented collision this run)")
        print("   Without Agent Mail, collisions are possible but not guaranteed")
    
    return True


def test_agent_race_stress_test():
    """Stress test with many agents."""
    print("\n" + "="*70)
    print("TEST 3: Stress test with 10 agents (Agent Mail enabled)")
    print("="*70)
    
    num_agents = 10
    result = run_race_test(num_agents, mail_enabled=True)
    
    successful_agents = [a for a in result["agents"] if a["success"]]
    
    print(f"\n📊 Results ({result['elapsed_seconds']:.3f}s):")
    print(f"   • Total agents: {num_agents}")
    print(f"   • Successful claims: {len(successful_agents)}")
    print(f"   • JSONL status changes: {len(result['jsonl_status_changes'])}")
    
    # Verify: Exactly one winner
    assert len(successful_agents) == 1, \
        f"Expected 1 successful claim, got {len(successful_agents)}"
    assert len(result['jsonl_status_changes']) == 1, \
        f"Expected 1 JSONL status change, got {len(result['jsonl_status_changes'])}"
    
    print(f"\n✅ PASS: Only {successful_agents[0]['agent']} succeeded")
    return True


def check_agent_mail_server() -> bool:
    """Check if Agent Mail server is running."""
    try:
        import urllib.request
        req = urllib.request.Request("http://localhost:8765/api/health")
        with urllib.request.urlopen(req, timeout=1) as response:
            return response.status == 200
    except:
        return False


def main():
    """Run all race condition tests."""
    print("🧪 Multi-Agent Race Condition Test Suite")
    print("Testing collision prevention with Agent Mail")
    
    try:
        # Check if bd is available
        subprocess.run(["bd", "--version"], capture_output=True, check=True)
    except (subprocess.CalledProcessError, FileNotFoundError):
        print("❌ ERROR: bd command not found")
        print("   Install: go install github.com/steveyegge/beads/cmd/bd@latest")
        sys.exit(1)
    
    # Check if Agent Mail server is running
    agent_mail_running = check_agent_mail_server()
    if not agent_mail_running:
        print("\n⚠️  WARNING: Agent Mail server is not running")
        print("   Tests will fall back to beads-only mode (demonstrating collision)")
        print("\n   To enable full collision prevention testing:")
        print("   $ cd ~/src/mcp_agent_mail")
        print("   $ source .venv/bin/activate")
        print("   $ uv run python -m mcp_agent_mail.cli serve-http")
        print()
        
        # Check if running in non-interactive mode (CI/automation)
        if not sys.stdin.isatty():
            print("   Running in non-interactive mode, continuing with tests...")
        else:
            print("   Press Enter to continue or Ctrl+C to exit")
            try:
                input()
            except KeyboardInterrupt:
                print("\n\n👋 Exiting - start Agent Mail server and try again")
                sys.exit(0)
    else:
        print("\n✅ Agent Mail server is running on http://localhost:8765")
    
    # Run tests
    tests = [
        ("Agent Mail enabled (collision prevention)", test_agent_race_with_mail),
        ("Agent Mail disabled (collision demonstration)", test_agent_race_without_mail),
        ("Stress test (10 agents)", test_agent_race_stress_test),
    ]
    
    passed = 0
    failed = 0
    
    for name, test_func in tests:
        try:
            if test_func():
                passed += 1
        except AssertionError as e:
            print(f"\n❌ FAIL: {name}")
            print(f"   {e}")
            failed += 1
        except Exception as e:
            print(f"\n💥 ERROR in {name}: {e}")
            failed += 1
    
    # Summary
    print("\n" + "="*70)
    print("SUMMARY")
    print("="*70)
    print(f"✅ Passed: {passed}/{len(tests)}")
    print(f"❌ Failed: {failed}/{len(tests)}")
    
    if failed == 0:
        print("\n🎉 All tests passed!")
        sys.exit(0)
    else:
        print(f"\n⚠️  {failed} test(s) failed")
        sys.exit(1)


if __name__ == "__main__":
    main()
