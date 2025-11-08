#!/usr/bin/env python3
"""
Multi-Agent Coordination Test Suite

Fast tests (<30s total) covering critical multi-agent scenarios:
- Fairness: N agents claiming M issues
- Notifications: End-to-end message passing
- Handoff: Release → immediate claim by another agent
- Idempotency: Double operations by same agent
"""

import json
import subprocess
import tempfile
import shutil
import sys
import time
from pathlib import Path
from multiprocessing import Process, Queue
from threading import Thread, Lock
from http.server import HTTPServer, BaseHTTPRequestHandler
import socket

# Add lib directory for beads_mail_adapter
lib_path = Path(__file__).parent.parent.parent / "lib"
sys.path.insert(0, str(lib_path))

from beads_mail_adapter import AgentMailAdapter


class MockAgentMailServer:
    """Lightweight mock server with reservations and notifications."""
    
    def __init__(self, port: int = 0):
        self.port = port
        self.server = None
        self.thread = None
        self.reservations = {}  # file_path -> agent_name
        self.notifications = {}  # agent_name -> [messages]
        self.lock = Lock()
        
    def start(self) -> int:
        """Start server and return port."""
        handler = self._create_handler()
        
        if self.port == 0:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.bind(('', 0))
                s.listen(1)
                self.port = s.getsockname()[1]
        
        self.server = HTTPServer(('127.0.0.1', self.port), handler)
        self.thread = Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        time.sleep(0.1)
        return self.port
    
    def stop(self):
        if self.server:
            self.server.shutdown()
            self.server.server_close()
    
    def _create_handler(self):
        parent = self
        
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            
            def do_GET(self):
                if self.path == "/api/health":
                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.end_headers()
                    self.wfile.write(b'{"status": "ok"}')
                
                # Get inbox: /api/notifications/{agent_name}
                elif self.path.startswith("/api/notifications/"):
                    agent_name = self.path.split('/')[-1]
                    with parent.lock:
                        messages = parent.notifications.get(agent_name, [])
                        parent.notifications[agent_name] = []  # Clear after read
                    
                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.end_headers()
                    self.wfile.write(json.dumps(messages).encode())
                
                elif self.path == "/api/reservations":
                    with parent.lock:
                        res_list = [
                            {"file_path": fp, "agent_name": agent}
                            for fp, agent in parent.reservations.items()
                        ]
                    
                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.end_headers()
                    self.wfile.write(json.dumps(res_list).encode())
                
                else:
                    self.send_response(404)
                    self.end_headers()
            
            def do_POST(self):
                content_length = int(self.headers.get('Content-Length', 0))
                body = self.rfile.read(content_length) if content_length > 0 else b'{}'
                data = json.loads(body.decode('utf-8'))
                
                # Reserve: /api/reservations
                if self.path == "/api/reservations":
                    file_path = data.get("file_path")
                    agent_name = data.get("agent_name")
                    
                    with parent.lock:
                        if file_path in parent.reservations:
                            existing = parent.reservations[file_path]
                            if existing != agent_name:
                                # Conflict
                                self.send_response(409)
                                self.send_header('Content-Type', 'application/json')
                                self.end_headers()
                                self.wfile.write(json.dumps({
                                    "error": f"Already reserved by {existing}"
                                }).encode())
                                return
                            # else: same agent re-reserving (idempotent)
                        
                        parent.reservations[file_path] = agent_name
                    
                    self.send_response(201)
                    self.send_header('Content-Type', 'application/json')
                    self.end_headers()
                    self.wfile.write(b'{"status": "reserved"}')
                
                # Notify: /api/notifications
                elif self.path == "/api/notifications":
                    from_agent = data.get("from_agent")
                    event_type = data.get("event_type")
                    payload = data.get("payload", {})
                    
                    # Broadcast to all OTHER agents
                    with parent.lock:
                        for agent_name in list(parent.notifications.keys()):
                            if agent_name != from_agent:
                                parent.notifications[agent_name].append({
                                    "from": from_agent,
                                    "event": event_type,
                                    "data": payload
                                })
                        
                        # If target agent specified, ensure they get it
                        to_agent = payload.get("to_agent")
                        if to_agent and to_agent not in parent.notifications:
                            parent.notifications[to_agent] = [{
                                "from": from_agent,
                                "event": event_type,
                                "data": payload
                            }]
                    
                    self.send_response(201)
                    self.send_header('Content-Type', 'application/json')
                    self.end_headers()
                    self.wfile.write(b'{"status": "sent"}')
                
                else:
                    self.send_response(404)
                    self.end_headers()
            
            def do_DELETE(self):
                # Release: /api/reservations/{agent}/{issue_id}
                parts = self.path.split('/')
                if len(parts) >= 5:
                    agent_name = parts[3]
                    issue_id = parts[4]
                    file_path = f".beads/issues/{issue_id}"
                    
                    with parent.lock:
                        if file_path in parent.reservations:
                            if parent.reservations[file_path] == agent_name:
                                del parent.reservations[file_path]
                    
                    self.send_response(204)
                    self.end_headers()
                else:
                    self.send_response(404)
                    self.end_headers()
        
        return Handler


class TestAgent:
    """Minimal test agent."""
    
    def __init__(self, workspace: str, agent_name: str, mail_url: str):
        self.workspace = workspace
        self.agent_name = agent_name
        self.mail = AgentMailAdapter(url=mail_url, agent_name=agent_name, timeout=2)
    
    def run_bd(self, *args):
        cmd = ["bd", "--no-daemon"] + list(args) + ["--json"]
        result = subprocess.run(cmd, cwd=self.workspace, capture_output=True, text=True)
        if result.returncode != 0:
            return {"error": result.stderr}
        if result.stdout.strip():
            try:
                return json.loads(result.stdout)
            except:
                return {"error": "Invalid JSON"}
        return {}
    
    def create_issue(self, title: str) -> str:
        result = self.run_bd("create", title, "-p", "1")
        return result.get("id")
    
    def claim_issue(self, issue_id: str) -> bool:
        if self.mail.enabled and not self.mail.reserve_issue(issue_id):
            return False
        result = self.run_bd("update", issue_id, "--status", "in_progress")
        return "error" not in result
    
    def release_issue(self, issue_id: str):
        if self.mail.enabled:
            self.mail.release_issue(issue_id)


def agent_claim_worker(agent_name: str, workspace: str, issue_id: str, 
                       mail_url: str, result_queue: Queue):
    """Worker that tries to claim a single issue."""
    try:
        agent = TestAgent(workspace, agent_name, mail_url)
        success = agent.claim_issue(issue_id)
        result_queue.put({"agent": agent_name, "issue": issue_id, "success": success})
    except Exception as e:
        result_queue.put({"agent": agent_name, "issue": issue_id, "success": False, "error": str(e)})


def test_fairness_n_agents_m_issues():
    """Test that N agents competing for M issues results in exactly M claims."""
    print("\n" + "="*70)
    print("TEST 1: Fairness - 10 agents, 5 issues")
    print("="*70)
    
    workspace = tempfile.mkdtemp(prefix="bd-test-fairness-")
    server = MockAgentMailServer()
    
    try:
        subprocess.run(["bd", "init", "--quiet", "--prefix", "test"], 
                      cwd=workspace, check=True, capture_output=True)
        
        port = server.start()
        mail_url = f"http://127.0.0.1:{port}"
        
        # Create 5 issues
        agent = TestAgent(workspace, "setup", mail_url)
        issues = [agent.create_issue(f"Issue {i+1}") for i in range(5)]
        
        # Spawn 10 agents trying to claim all 5 issues
        result_queue = Queue()
        processes = []
        
        for agent_num in range(10):
            for issue_id in issues:
                p = Process(target=agent_claim_worker, 
                          args=(f"agent-{agent_num}", workspace, issue_id, mail_url, result_queue))
                processes.append(p)
        
        # Start all at once
        for p in processes:
            p.start()
        
        for p in processes:
            p.join(timeout=10)
        
        # Collect results
        results = []
        while not result_queue.empty():
            results.append(result_queue.get())
        
        # Count successful claims per issue
        claims_per_issue = {}
        for r in results:
            if r["success"]:
                issue = r["issue"]
                claims_per_issue[issue] = claims_per_issue.get(issue, 0) + 1
        
        print(f"   • Total attempts: {len(results)}")
        print(f"   • Successful claims: {sum(claims_per_issue.values())}")
        print(f"   • Claims per issue: {claims_per_issue}")
        
        # Verify exactly 1 claim per issue
        for issue_id in issues:
            claims = claims_per_issue.get(issue_id, 0)
            assert claims == 1, f"Issue {issue_id} claimed {claims} times (expected 1)"
        
        print("✅ PASS: Each issue claimed exactly once")
        return True
        
    finally:
        server.stop()
        shutil.rmtree(workspace, ignore_errors=True)


def test_notification_end_to_end():
    """Test notifications from agent1 to agent2."""
    print("\n" + "="*70)
    print("TEST 2: Notification End-to-End")
    print("="*70)
    
    workspace = tempfile.mkdtemp(prefix="bd-test-notify-")
    server = MockAgentMailServer()
    
    try:
        subprocess.run(["bd", "init", "--quiet", "--prefix", "test"], 
                      cwd=workspace, check=True, capture_output=True)
        
        port = server.start()
        mail_url = f"http://127.0.0.1:{port}"
        
        # Create two agents
        agent1 = TestAgent(workspace, "agent1", mail_url)
        agent2 = TestAgent(workspace, "agent2", mail_url)
        
        # Register agent2's inbox
        server.notifications["agent2"] = []
        
        # Agent1 sends notification
        sent = agent1.mail.notify("task_completed", {
            "issue_id": "bd-123",
            "status": "done",
            "to_agent": "agent2"
        })
        
        assert sent, "Should send notification"
        
        # Agent2 checks inbox
        messages = agent2.mail.check_inbox()
        
        print(f"   • Agent1 sent notification")
        print(f"   • Agent2 received {len(messages)} message(s)")
        
        assert len(messages) == 1, f"Expected 1 message, got {len(messages)}"
        assert messages[0]["from"] == "agent1"
        assert messages[0]["event"] == "task_completed"
        assert messages[0]["data"]["issue_id"] == "bd-123"
        
        # Second check should be empty (messages consumed)
        messages2 = agent2.mail.check_inbox()
        assert len(messages2) == 0, "Inbox should be empty after read"
        
        print("✅ PASS: Notification delivered correctly")
        return True
        
    finally:
        server.stop()
        shutil.rmtree(workspace, ignore_errors=True)


def test_reservation_handoff():
    """Test immediate claim after release (handoff scenario)."""
    print("\n" + "="*70)
    print("TEST 3: Reservation Handoff")
    print("="*70)
    
    workspace = tempfile.mkdtemp(prefix="bd-test-handoff-")
    server = MockAgentMailServer()
    
    try:
        subprocess.run(["bd", "init", "--quiet", "--prefix", "test"], 
                      cwd=workspace, check=True, capture_output=True)
        
        port = server.start()
        mail_url = f"http://127.0.0.1:{port}"
        
        agent1 = TestAgent(workspace, "agent1", mail_url)
        agent2 = TestAgent(workspace, "agent2", mail_url)
        
        # Agent1 creates and claims issue
        issue_id = agent1.create_issue("Handoff test")
        claimed1 = agent1.claim_issue(issue_id)
        assert claimed1, "Agent1 should claim issue"
        
        # Agent2 tries to claim (should fail - reserved)
        claimed2_before = agent2.claim_issue(issue_id)
        assert not claimed2_before, "Agent2 should be blocked"
        
        # Agent1 releases
        agent1.release_issue(issue_id)
        
        # Agent2 immediately claims (handoff)
        claimed2_after = agent2.claim_issue(issue_id)
        assert claimed2_after, "Agent2 should claim after release"
        
        # Verify reservation ownership
        reservations = agent2.mail.get_reservations()
        assert len(reservations) == 1
        assert reservations[0]["agent_name"] == "agent2"
        
        print("✅ PASS: Clean handoff from agent1 to agent2")
        return True
        
    finally:
        server.stop()
        shutil.rmtree(workspace, ignore_errors=True)


def test_idempotent_operations():
    """Test double reserve and double release by same agent."""
    print("\n" + "="*70)
    print("TEST 4: Idempotent Operations")
    print("="*70)
    
    workspace = tempfile.mkdtemp(prefix="bd-test-idem-")
    server = MockAgentMailServer()
    
    try:
        subprocess.run(["bd", "init", "--quiet", "--prefix", "test"], 
                      cwd=workspace, check=True, capture_output=True)
        
        port = server.start()
        mail_url = f"http://127.0.0.1:{port}"
        
        agent = TestAgent(workspace, "agent1", mail_url)
        issue_id = agent.create_issue("Idempotency test")
        
        # Reserve twice (idempotent)
        reserve1 = agent.mail.reserve_issue(issue_id)
        reserve2 = agent.mail.reserve_issue(issue_id)
        
        assert reserve1, "First reserve should succeed"
        assert reserve2, "Second reserve should be idempotent (same agent)"
        
        # Verify only one reservation
        reservations = agent.mail.get_reservations()
        assert len(reservations) == 1, f"Should have 1 reservation, got {len(reservations)}"
        
        # Release twice (idempotent)
        release1 = agent.mail.release_issue(issue_id)
        release2 = agent.mail.release_issue(issue_id)
        
        assert release1, "First release should succeed"
        assert release2, "Second release should be idempotent (no error)"
        
        # Verify no reservations
        reservations_after = agent.mail.get_reservations()
        assert len(reservations_after) == 0, "Should have 0 reservations after release"
        
        print("✅ PASS: Double reserve and release are idempotent")
        return True
        
    finally:
        server.stop()
        shutil.rmtree(workspace, ignore_errors=True)


def main():
    """Run coordination tests."""
    print("🧪 Multi-Agent Coordination Test Suite")
    print("Fast tests for critical coordination scenarios")
    
    try:
        subprocess.run(["bd", "--version"], capture_output=True, check=True)
    except (subprocess.CalledProcessError, FileNotFoundError):
        print("❌ ERROR: bd command not found")
        sys.exit(1)
    
    tests = [
        ("Fairness (10 agents, 5 issues)", test_fairness_n_agents_m_issues),
        ("Notification end-to-end", test_notification_end_to_end),
        ("Reservation handoff", test_reservation_handoff),
        ("Idempotent operations", test_idempotent_operations),
    ]
    
    passed = 0
    failed = 0
    start_time = time.time()
    
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
            import traceback
            traceback.print_exc()
            failed += 1
    
    elapsed = time.time() - start_time
    
    print("\n" + "="*70)
    print("SUMMARY")
    print("="*70)
    print(f"✅ Passed: {passed}/{len(tests)}")
    print(f"❌ Failed: {failed}/{len(tests)}")
    print(f"⏱️  Total time: {elapsed:.1f}s")
    
    if failed == 0:
        print("\n🎉 All coordination tests passed!")
        sys.exit(0)
    else:
        print(f"\n⚠️  {failed} test(s) failed")
        sys.exit(1)


if __name__ == "__main__":
    main()
